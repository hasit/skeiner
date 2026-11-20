// Package dispatch translates approved catalog operations into bounded HTTP requests.
package dispatch

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/hasit/skeiner/internal/catalog"
)

const (
	DefaultMaxResponseBytes = 256 << 10
	MaxArgumentBytes        = 32 << 10
)

type Config struct {
	BaseURL           string
	BearerToken       string
	CredentialHeaders map[string]string
	MaxResponseBytes  int64
}

type Client struct {
	catalog        *catalog.Catalog
	base           *url.URL
	http           *http.Client
	token          string
	headers        map[string]string
	secrets        []string
	replaceSecrets *strings.Replacer
	limit          int64
}

// Error is safe to expose to the guest. It never contains HTTP headers, bodies,
// URLs, argument values, or the underlying network error.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func Status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func New(cat *catalog.Catalog, cfg Config) (*Client, error) {
	if cat == nil {
		return nil, errors.New("missing operation catalog")
	}
	rawURL := cfg.BaseURL
	if rawURL == "" {
		if len(cfg.CredentialHeaders) != 0 {
			return nil, errors.New("credential headers require an explicitly configured base URL")
		}
		rawURL = cat.BaseURL
	}
	base, err := url.Parse(rawURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") ||
		base.Hostname() == "" || base.User != nil || base.RawQuery != "" ||
		base.ForceQuery || base.Fragment != "" || strings.Contains(rawURL, "#") ||
		base.RawPath != "" || strings.Contains(base.Path, "//") || strings.ContainsAny(base.Path, `\%;`) ||
		strings.IndexFunc(base.Path, unicode.IsControl) >= 0 {
		return nil, errors.New("base URL must be an absolute HTTP(S) origin with an optional plain path prefix")
	}
	base.Path = strings.TrimSuffix(base.Path, "/")
	if base.Path != "" && path.Clean(base.Path) != base.Path {
		return nil, errors.New("base URL must use a canonical path prefix")
	}
	if cfg.MaxResponseBytes < 0 || cfg.MaxResponseBytes > 1<<30 {
		return nil, errors.New("max response bytes must be between 0 and 1 GiB")
	}
	if cfg.MaxResponseBytes == 0 {
		cfg.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if strings.IndexFunc(cfg.BearerToken, unicode.IsControl) >= 0 {
		return nil, errors.New("invalid bearer credential")
	}
	headers := make(map[string]string, len(cfg.CredentialHeaders))
	secrets := []string{cfg.BearerToken}
	if len(cfg.CredentialHeaders) > 0 {
		host := base.Hostname()
		if base.Scheme != "https" && !net.ParseIP(host).IsLoopback() && strings.ToLower(host) != "localhost" {
			return nil, errors.New("credential headers require HTTPS or a loopback base URL")
		}
		for name, value := range cfg.CredentialHeaders {
			canonical, ok := catalog.CredentialHeaderName(name)
			if !ok {
				return nil, errors.New("unsupported credential header name")
			}
			if _, exists := headers[canonical]; exists {
				return nil, errors.New("duplicate credential header binding")
			}
			if value == "" || strings.TrimSpace(value) != value || strings.IndexFunc(value, func(r rune) bool { return r < ' ' || r > '~' }) >= 0 {
				return nil, errors.New("credential header values must be nonempty printable ASCII without surrounding whitespace")
			}
			headers[canonical] = value
			secrets = append(secrets, value)
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	transport.MaxResponseHeaderBytes = 64 << 10
	return &Client{
		catalog:        cat,
		base:           base,
		token:          cfg.BearerToken,
		headers:        headers,
		secrets:        secrets,
		replaceSecrets: secretReplacer(secrets),
		limit:          cfg.MaxResponseBytes,
		http: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func invalid(message string) error { return &Error{Code: "invalid_arguments", Message: message} }

var (
	placeholder     = regexp.MustCompile(`\{([^{}]+)\}`)
	sensitiveHeader = regexp.MustCompile(`(?i)\b(authorization|proxy-authorization|cookie|set-cookie|x-api-key|api-key|api-username|x-auth-token)\s*[:=]\s*[^\r\n]*`)
)

func secretReplacer(secrets []string) *strings.Replacer {
	// A configured username can also be the prefix of a longer credential.
	ordered := slices.Clone(secrets)
	slices.SortFunc(ordered, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	pairs := make([]string, 0, 2*len(ordered))
	for _, secret := range ordered {
		if secret != "" {
			pairs = append(pairs, secret, "[REDACTED]")
		}
	}
	return strings.NewReplacer(pairs...)
}

// Redact cleans plain diagnostic text, including credentials embedded by an
// upstream service in a log line. Use the structured scrubber for JSON.
func Redact(s string, secrets ...string) string {
	return redactText(s, secretReplacer(secrets))
}

func (c *Client) Redact(s string) string {
	return redactText(s, c.replaceSecrets)
}

func redactText(s string, replacer *strings.Replacer) string {
	return replacer.Replace(sensitiveHeader.ReplaceAllString(s, "${1}: [REDACTED]"))
}

// scrub keeps field names intact. Rewriting a key could merge distinct API fields.
func (c *Client) scrub(v any) (any, bool) {
	var literal string
	switch x := v.(type) {
	case string:
		return c.Redact(x), true
	case json.Number:
		literal = x.String()
	case bool:
		literal = strconv.FormatBool(x)
	case nil:
		literal = "null"
	case []any:
		for i, value := range x {
			next, safe := c.scrub(value)
			if !safe {
				return nil, false
			}
			x[i] = next
		}
	case map[string]any:
		key := c.headers["Api-Key"]
		for name, value := range x {
			// A username may be a real schema field, such as Discourse's admin boolean.
			// Bearer tokens, API keys and header values are always confidential.
			if (c.token != "" && strings.Contains(name, c.token)) ||
				(key != "" && strings.Contains(name, key)) || sensitiveHeader.MatchString(name) {
				return nil, false
			}
			switch strings.ToLower(strings.ReplaceAll(name, "_", "-")) {
			case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key", "api-username", "x-auth-token":
				x[name] = "[REDACTED]"
			default:
				next, safe := c.scrub(value)
				if !safe {
					return nil, false
				}
				x[name] = next
			}
		}
	}
	if literal != "" && slices.Contains(c.secrets, literal) {
		return "[REDACTED]", true
	}
	return v, true
}

func scalar(p catalog.Parameter, raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 {
		switch p.Type {
		case "string":
			var s string
			if raw[0] == '"' && json.Unmarshal(raw, &s) == nil {
				return s, nil
			}
		case "boolean":
			if string(raw) == "true" || string(raw) == "false" {
				return string(raw), nil
			}
		case "integer", "number":
			if raw[0] == '-' || raw[0] >= '0' && raw[0] <= '9' {
				if p.Type == "number" || !bytes.ContainsAny(raw, ".eE") {
					return string(raw), nil
				}
			}
		}
	}
	return "", invalid(fmt.Sprintf("%s.%s requires %s", p.In, p.Name, p.Type))
}

func safeSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\%;{}") &&
		strings.IndexFunc(s, unicode.IsControl) < 0
}

func (c *Client) endpoint(op *catalog.Operation, args json.RawMessage) (*url.URL, error) {
	if len(args) > MaxArgumentBytes {
		return nil, invalid("arguments exceed 32 KiB")
	}
	var groups map[string]map[string]json.RawMessage
	if err := json.Unmarshal(args, &groups); err != nil || groups == nil {
		return nil, invalid("expected an object with optional path and query objects")
	}
	for k, g := range groups {
		if (k != "path" && k != "query") || g == nil {
			return nil, invalid("expected an object with optional path and query objects")
		}
	}

	query := url.Values{}
	pathValues := make(map[string]string)
	for _, p := range op.Parameters {
		raw, ok := groups[p.In][p.Name]
		if !ok {
			if p.Required {
				return nil, invalid("missing " + p.In + "." + p.Name)
			}
			continue
		}
		delete(groups[p.In], p.Name)
		value, err := scalar(p, raw)
		if err != nil {
			return nil, err
		}
		switch p.In {
		case "path":
			if !safeSegment(value) {
				return nil, invalid("unsafe path segment for " + p.Name)
			}
			pathValues[p.Name] = value
		case "query":
			query.Set(p.Name, value)
		default:
			return nil, invalid("unsupported parameter location")
		}
	}
	if len(groups["path"]) != 0 || len(groups["query"]) != 0 {
		return nil, invalid("unknown parameter; consult search for supported inputs")
	}

	opPath := placeholder.ReplaceAllStringFunc(op.Path, func(part string) string {
		return pathValues[part[1:len(part)-1]]
	})
	if !strings.HasPrefix(opPath, "/") || strings.Contains(opPath, "//") ||
		strings.ContainsAny(opPath, "\\%;{}") || path.Clean(opPath) != strings.TrimSuffix(opPath, "/") && opPath != "/" {
		return nil, invalid("operation produced a noncanonical path")
	}
	if !c.catalog.MatchesPath(op, opPath) {
		return nil, &Error{Code: "denied", Message: "input selects a different upstream route"}
	}
	// Copy the pinned URL. Assign its Path and RawQuery fields separately so
	// neither a spec template nor a guest value can set the authority.
	endpoint := *c.base
	endpoint.Path += opPath
	endpoint.RawQuery = strings.ReplaceAll(query.Encode(), "+", "%20")
	return &endpoint, nil
}

func contextError(ctx context.Context) error {
	switch ctx.Err() {
	case context.DeadlineExceeded:
		return &Error{Code: "timeout", Message: "execution deadline expired during the HTTP call"}
	case context.Canceled:
		return &Error{Code: "canceled", Message: "execution canceled during the HTTP call"}
	}
	return nil
}

// Call returns just the successful JSON body. Authorization is checked on
// every invocation, including calls issued before any search.
func (c *Client) Call(ctx context.Context, id string, args json.RawMessage) (json.RawMessage, error) {
	out, _, err := c.CallWithStatus(ctx, id, args)
	return out, err
}

// CallWithStatus also reports the upstream HTTP status when a response was
// received. Local validation, policy, and transport errors have status zero.
func (c *Client) CallWithStatus(ctx context.Context, id string, args json.RawMessage) (json.RawMessage, int, error) {
	return c.callWithStatus(ctx, id, args, false)
}

// ReadPageWithStatus dispatches an approved GET for a paginated read.
func (c *Client) ReadPageWithStatus(ctx context.Context, id string, args json.RawMessage) (json.RawMessage, int, error) {
	return c.callWithStatus(ctx, id, args, true)
}

func (c *Client) callWithStatus(ctx context.Context, id string, args json.RawMessage, getOnly bool) (result json.RawMessage, status int, err error) {
	defer func() {
		if e, ok := err.(*Error); ok {
			e.Message = c.Redact(e.Message)
		}
	}()
	op, ok := c.catalog.Lookup(id)
	if !ok || op.NeedsBearer && c.token == "" || getOnly && op.Method != http.MethodGet {
		return nil, 0, &Error{Code: "denied", Message: "operation is unknown or unavailable"}
	}
	for _, name := range op.CredentialHeaders {
		if op.Method != http.MethodGet || c.headers[name] == "" {
			return nil, 0, &Error{Code: "denied", Message: "operation is unknown or unavailable"}
		}
	}
	endpoint, err := c.endpoint(op, args)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, op.Method, endpoint.String(), nil)
	if err != nil {
		return nil, 0, invalid("operation produced an invalid request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for _, name := range op.CredentialHeaders {
		req.Header.Set(name, c.headers[name])
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if e := contextError(ctx); e != nil {
			return nil, 0, e
		}
		return nil, 0, &Error{Code: "upstream_error", Message: "upstream HTTP request failed"}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, resp.StatusCode, &Error{Code: "redirect_denied", Message: "redirects are disabled", Status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, &Error{
			Code: "upstream_error", Message: fmt.Sprintf("upstream returned HTTP %d", resp.StatusCode), Status: resp.StatusCode,
		}
	}
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusResetContent || op.Method == http.MethodHead {
		return json.RawMessage("null"), resp.StatusCode, nil
	}
	if enc := resp.Header.Get("Content-Encoding"); enc != "" && !strings.EqualFold(enc, "identity") {
		return nil, resp.StatusCode, &Error{Code: "upstream_error", Message: "upstream ignored Accept-Encoding: identity; configure the endpoint to return uncompressed JSON"}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.limit+1))
	if err != nil {
		if e := contextError(ctx); e != nil {
			return nil, resp.StatusCode, e
		}
		return nil, resp.StatusCode, &Error{Code: "upstream_error", Message: "could not read upstream response"}
	}
	if int64(len(body)) > c.limit {
		return nil, resp.StatusCode, &Error{Code: "response_truncated", Message: "response byte limit exceeded; request a smaller page"}
	}
	if !json.Valid(body) {
		return nil, resp.StatusCode, &Error{Code: "upstream_error", Message: "operation returned invalid JSON", Status: resp.StatusCode}
	}
	var data any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&data); err != nil {
		return nil, resp.StatusCode, &Error{Code: "upstream_error", Message: "could not decode upstream JSON", Status: resp.StatusCode}
	}
	clean, safe := c.scrub(data)
	if !safe {
		return nil, resp.StatusCode, &Error{Code: "sensitive_response", Message: "upstream response contains a credential in an object field name", Status: resp.StatusCode}
	}
	out, err := json.Marshal(clean)
	return out, resp.StatusCode, err
}
