// Package catalog compiles an immutable, caller-scoped OpenAPI operation index.
package catalog

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/hasit/skeiner/internal/yamljson"
)

type Parameter struct {
	In            string          `json:"in"`
	Name          string          `json:"name"`
	Type          string          `json:"type"`
	Required      bool            `json:"required"`
	Description   string          `json:"description,omitempty"`
	Enum          json.RawMessage `json:"enum,omitempty"`
	EnumTruncated bool            `json:"enumTruncated,omitempty"`
}
type Operation struct {
	ID                string         `json:"operationId"`
	NeedsBearer       bool           `json:"-"`
	CredentialHeaders []string       `json:"-"`
	Method            string         `json:"method"`
	Path              string         `json:"path"`
	Summary           string         `json:"summary"`
	Description       string         `json:"description,omitempty"`
	Tags              []string       `json:"tags,omitempty"`
	Parameters        []Parameter    `json:"parameters"`
	Response          map[string]any `json:"response"`
}

// OperationInfo records the compiler's decision for one declared route. Reason
// is empty when the operation is exposed under the caller's policy.
type OperationInfo struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	ID     string `json:"operationId"`
	Reason string `json:"reason,omitempty"`
}

type Catalog struct {
	BaseURL      string
	Warnings     []string
	declared     []OperationInfo
	ops          map[string]*Operation
	noBearer     bool
	boundHeaders map[string]bool
	index        map[string]map[string]int
	routes       map[string][]route
}

type route struct {
	template string
	segments []routeSegment
	fixed    int
}

type routeSegment struct {
	text     string // Exact literal, or the required suffix of a variable segment.
	variable bool
}

func (s routeSegment) matches(value string) bool {
	if !s.variable {
		return s.text == value
	}
	return len(value) > len(s.text) && strings.HasSuffix(value, s.text)
}

// WithoutBearer returns a view limited to operations that can be called
// without a bearer credential. It shares the lexical index and route table.
func (c *Catalog) WithoutBearer() *Catalog {
	out := *c
	out.noBearer = true
	return &out
}

// CredentialHeaderName recognizes only the two supported HTTP credential fields.
func CredentialHeaderName(name string) (string, bool) {
	switch canonical := http.CanonicalHeaderKey(name); canonical {
	case "Api-Key", "Api-Username":
		return canonical, true
	}
	return "", false
}

// WithCredentials binds a caller's credential availability without storing values in the catalog.
func (c *Catalog) WithCredentials(hasBearer bool, headers map[string]string) *Catalog {
	out := *c
	out.noBearer = !hasBearer
	out.boundHeaders = map[string]bool{}
	for name, value := range headers {
		canonical, ok := CredentialHeaderName(name)
		if ok {
			_, duplicate := out.boundHeaders[canonical]
			out.boundHeaders[canonical] = !duplicate && value != ""
		}
	}
	return &out
}

func (c *Catalog) unavailable(op *Operation) string {
	if op.NeedsBearer && c.noBearer {
		return "requires a configured bearer token"
	}
	var missing []string
	for _, name := range op.CredentialHeaders {
		if !c.boundHeaders[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return "requires configured credential headers: " + strings.Join(missing, ", ")
	}
	return ""
}

func (c *Catalog) Len() int {
	n := 0
	for _, op := range c.ops {
		if c.unavailable(op) == "" {
			n++
		}
	}
	return n
}

// Inventory includes omitted operations for the local API owner. It is never
// exposed through MCP search. Parsing establishes path/method order.
func (c *Catalog) Inventory() []OperationInfo {
	items := append([]OperationInfo(nil), c.declared...)
	for i := range items {
		if items[i].Reason == "" {
			items[i].Reason = c.unavailable(c.ops[items[i].ID])
		}
	}
	return items
}

func (c *Catalog) Lookup(id string) (*Operation, bool) {
	op, ok := c.ops[id]
	if !ok || c.unavailable(op) != "" {
		return nil, false
	}
	return op, true
}

const maxSpecBytes = 16 << 20

func Load(path string, allow []string) (*Catalog, error) {
	return LoadSelected(path, allow, nil)
}

// LoadSelected loads a catalog with the policy described by ParseSelected.
func LoadSelected(path string, allowIDs, getPaths []string) (*Catalog, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSpecBytes+1))
	if err != nil {
		return nil, err
	}
	return ParseSelected(b, allowIDs, getPaths)
}

type obj = map[string]any
type document struct{ root obj }

func asObj(v any) obj  { o, _ := v.(map[string]any); return o }
func str(v any) string { s, _ := v.(string); return s }
func keys(o obj) []string {
	a := make([]string, 0, len(o))
	for k := range o {
		a = append(a, k)
	}
	sort.Strings(a)
	return a
}

var openapiVersion = regexp.MustCompile(`^3\.[0-2]\.[0-9]+$`)
var methods = map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true, "head": true, "options": true, "trace": true, "query": true}

// Parse excludes unsupported operations individually and reports them in Warnings.
// A nil allowlist exposes all compiled operations. An empty non-nil list exposes none.
func Parse(data []byte, allow []string) (*Catalog, error) {
	return ParseSelected(data, allow, nil)
}

// ParseSelected admits operations by declared ID or exact GET path template.
// A selected GET without an operationId uses "GET <template>" as its ID.
// File-extension templates require explicit selection even with a native ID.
func ParseSelected(data []byte, allowIDs, getPaths []string) (*Catalog, error) {
	if len(data) > maxSpecBytes {
		return nil, fmt.Errorf("spec exceeds %d bytes", maxSpecBytes)
	}
	var d document
	if err := yamljson.Decode(data, &d.root); err != nil {
		return nil, fmt.Errorf("decode spec: %w", err)
	}
	if !openapiVersion.MatchString(str(d.root["openapi"])) {
		return nil, fmt.Errorf("expected OpenAPI 3.0.x, 3.1.x or 3.2.x")
	}
	paths := asObj(d.root["paths"])
	if paths == nil {
		return nil, fmt.Errorf("spec must have a paths object")
	}
	c := &Catalog{ops: map[string]*Operation{}, index: map[string]map[string]int{}, routes: map[string][]route{}}
	if servers, ok := d.root["servers"].([]any); ok && len(servers) > 0 {
		c.BaseURL = str(asObj(servers[0])["url"])
	}
	if strings.ContainsAny(c.BaseURL, "{}") {
		c.Warnings = append(c.Warnings, "root server URL requires variable substitution: configure an explicit base URL")
		c.BaseURL = ""
	}
	ids := map[string][]int{}
	declare := func(op OperationInfo) int {
		i := len(c.declared)
		c.declared = append(c.declared, op)
		if strings.TrimSpace(op.ID) != "" {
			ids[op.ID] = append(ids[op.ID], i)
		}
		return i
	}
	enabled := map[string]bool{}
	for _, id := range allowIDs {
		enabled[id] = true
	}
	restricted := allowIDs != nil || getPaths != nil
	// Each selector records whether the spec declared a GET at that exact path.
	selectedGETs := map[string]bool{}
	for _, path := range getPaths {
		selectedGETs[path] = false
	}
	for _, path := range keys(paths) {
		if strings.HasPrefix(path, "x-") {
			continue
		}
		item, err := d.resolve(paths[path])
		if err != nil {
			// An unresolved path can still shadow a templated route for any method.
			for method := range methods {
				c.addRoute(method, path)
			}
			c.Warnings = append(c.Warnings, path+": "+err.Error())
			continue
		}
		for _, method := range keys(item) {
			if method == "additionalOperations" {
				additional := asObj(item[method])
				for _, verb := range keys(additional) {
					raw := asObj(additional[verb])
					declare(OperationInfo{Method: verb, Path: path, ID: str(raw["operationId"]), Reason: "additionalOperations is unsupported"})
				}
				c.Warnings = append(c.Warnings, path+": additionalOperations is unsupported")
				continue
			}
			if !methods[method] {
				continue
			}
			c.addRoute(method, path)
			raw := asObj(item[method])
			methodName := strings.ToUpper(method)
			location := methodName + " " + path
			rawID, hasID := raw["operationId"]
			id := str(rawID)
			_, requested := selectedGETs[path]
			selectedGET := method == "get" && requested
			if selectedGET {
				selectedGETs[path] = true
				if raw != nil && !hasID {
					id = location
				}
			}
			index := declare(OperationInfo{Method: methodName, Path: path, ID: id})
			if !selectedGET && strings.TrimSpace(id) == "" {
				reason := "missing operationId"
				if method == "get" && raw != nil && !hasID {
					reason += " (select this exact template with --allow-get)"
				}
				c.declared[index].Reason = reason
				c.Warnings = append(c.Warnings, location+": "+reason)
				continue
			}
			if restricted && !selectedGET && !enabled[id] {
				c.declared[index].Reason = "outside the configured allowlist"
				continue
			}
			op, err := d.compile(path, method, id, item, raw, selectedGET || enabled[id])
			if err != nil {
				c.declared[index].Reason = err.Error()
				if id != "" && id != location {
					location += " (" + id + ")"
				}
				c.Warnings = append(c.Warnings, location+": "+err.Error())
				continue
			}
			c.ops[id] = op
		}
	}
	for id, indices := range ids {
		if len(indices) > 1 {
			delete(c.ops, id)
			locations := make([]string, 0, len(indices))
			for _, i := range indices {
				c.declared[i].Reason = "ambiguous operationId"
				locations = append(locations, c.declared[i].Method+" "+c.declared[i].Path)
			}
			c.Warnings = append(c.Warnings, fmt.Sprintf("ambiguous operationId %q at %s: excluded all matches", id, strings.Join(locations, ", ")))
		}
	}
	for _, id := range allowIDs {
		if _, ok := c.ops[id]; !ok {
			c.Warnings = append(c.Warnings, fmt.Sprintf("allowlist operation %q unavailable", id))
		}
	}
	for path, found := range selectedGETs {
		if !found {
			c.Warnings = append(c.Warnings, fmt.Sprintf("GET path selector %q: no matching GET operation", path))
		}
	}
	sort.Strings(c.Warnings)
	for _, op := range c.ops {
		c.indexOperation(op)
	}
	return c, nil
}
func (c *Catalog) addRoute(method, path string) {
	parts := strings.Split(path, "/")
	r := route{template: path, segments: make([]routeSegment, len(parts))}
	for i, part := range parts {
		if !strings.ContainsAny(part, "{}") {
			r.segments[i] = routeSegment{text: part}
			r.fixed++
			continue
		}
		// Unknown template forms can still shadow a selected operation.
		segment := routeSegment{variable: true}
		if parameter := pathParameterSegment.FindStringSubmatch(part); parameter != nil {
			segment.text = parameter[2]
		}
		r.segments[i] = segment
	}
	m := strings.ToUpper(method)
	c.routes[m] = append(c.routes[m], r)
}

// MatchesPath rejects expansions that could select another equally or more
// specific upstream route, including a route omitted from the allowlist.
func (c *Catalog) MatchesPath(op *Operation, path string) bool {
	parts := strings.Split(path, "/")
	maxFixed := -1
	best := ""
	for _, r := range c.routes[op.Method] {
		if len(r.segments) != len(parts) {
			continue
		}
		match := true
		for i, s := range r.segments {
			if !s.matches(parts[i]) {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		if r.fixed > maxFixed {
			maxFixed, best = r.fixed, r.template
		} else if r.fixed == maxFixed && best != r.template {
			best = ""
		}
	}
	return best == op.Path
}

// Resolve local JSON Pointer references only. No document can cause network or
// filesystem reads. $ref siblings are rejected rather than silently overridden.
func (d document) resolve(raw any) (obj, error) {
	o := asObj(raw)
	if o == nil {
		return nil, fmt.Errorf("expected object")
	}
	seen := map[string]bool{}
	for o["$ref"] != nil {
		ref := str(o["$ref"])
		if !strings.HasPrefix(ref, "#/") {
			return nil, fmt.Errorf("external or invalid $ref %q unsupported", ref)
		}
		if len(o) > 1 {
			return nil, fmt.Errorf("$ref siblings unsupported at %q", ref)
		}
		if seen[ref] || len(seen) >= 64 {
			return nil, fmt.Errorf("cyclic or deep $ref %q", ref)
		}
		seen[ref] = true
		var value any = d.root
		for _, segment := range strings.Split(ref[2:], "/") {
			key := strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
			value = asObj(value)[key]
		}
		o = asObj(value)
		if o == nil {
			return nil, fmt.Errorf("unresolved $ref %q", ref)
		}
	}
	return o, nil
}
