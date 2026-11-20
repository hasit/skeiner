package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hasit/skeiner/internal/catalog"
)

const spec = `{
  "openapi":"3.0.3","info":{"title":"test","version":"1"},
  "paths":{
    "/runs/{run}":{"get":{
      "operationId":"getRun","summary":"Find a run",
      "parameters":[
        {"in":"path","name":"run","required":true,"schema":{"type":"string"}},
        {"in":"query","name":"page","schema":{"type":"integer"}},
        {"in":"query","name":"filter","schema":{"type":"string"}}
      ],
      "responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object"}}}}}
    }},
    "/runs/private":{"get":{
      "operationId":"getPrivate","summary":"Private metadata",
      "responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object"}}}}}
    }}
  }
}`

func client(t *testing.T, base string, limit int64) *Client {
	t.Helper()
	cat, err := catalog.Parse([]byte(spec), []string{"getRun"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(cat, Config{BaseURL: base, BearerToken: "super.private.token", MaxResponseBytes: limit})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func errorCode(t *testing.T, err error, want string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != want {
		t.Fatalf("wanted %s, got %v", want, err)
	}
}

const validArgs = `{"path":{"run":"r814"}}`

func TestDispatch(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ci/runs/a b" || r.URL.Query().Get("filter") != "a&b?c" || r.URL.Query().Get("page") != "2" {
			t.Errorf("wrong request target %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer super.private.token" {
			t.Error("missing host credential")
		}
		io.WriteString(w, `{"headers":{"Authorization":"Bearer other-secret","Set-Cookie":"secret=value"},"log":"note\nX-API-Key: do-not-leak","echo":"super.private.token","n":9007199254740993}`)
	}))
	defer s.Close()
	c := client(t, s.URL+"/ci/", 0)
	out, err := c.Call(context.Background(), "getRun", json.RawMessage(`{"path":{"run":"a b"},"query":{"filter":"a&b?c","page":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"other-secret", "secret=value", "do-not-leak", "super.private.token"} {
		if strings.Contains(string(out), secret) {
			t.Errorf("leaked %s", secret)
		}
	}
	if !json.Valid(out) || !strings.Contains(string(out), "9007199254740993") {
		t.Errorf("invalid or lossy result %s", out)
	}
}

func TestRejectedBeforeDispatch(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected request") }))
	defer s.Close()
	c := client(t, s.URL, 100)
	for _, tt := range []struct{ id, args, code string }{
		{"getPrivate", "{}", "denied"},
		{"unpublished", "{}", "denied"},
		{"getRun", `{"path":{"run":"private"}}`, "denied"},
		{"getRun", "{}", "invalid_arguments"},
		{"getRun", `{"path":{"run":"../private"}}`, "invalid_arguments"},
		{"getRun", `{"path":{"run":"..%252fprivate"}}`, "invalid_arguments"},
		{"getRun", `{"path":{"run":"."}}`, "invalid_arguments"},
		{"getRun", `{"path":{"run":"x\\private"}}`, "invalid_arguments"},
		{"getRun", `{"path":{"run":"//example.invalid"}}`, "invalid_arguments"},
		{"getRun", `{"path":{"run":"x"},"query":{"page":"2"}}`, "invalid_arguments"},
		{"getRun", `{"path":{"run":"x"},"query":{"unexpected":"y"}}`, "invalid_arguments"},
		{"getRun", `{"path":{"run":"x"},"headers":{"Host":"example.invalid"}}`, "invalid_arguments"},
		{"getRun", `{"path":null}`, "invalid_arguments"},
		{"getRun", `{"path":{"run":null}}`, "invalid_arguments"},
		{"getRun", strings.Repeat(" ", MaxArgumentBytes+1), "invalid_arguments"},
	} {
		_, err := c.Call(context.Background(), tt.id, json.RawMessage(tt.args))
		errorCode(t, err, tt.code)
	}
}

func TestRedirectsNeverForwardCredentials(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	for _, loc := range []string{target.URL, "/private"} {
		t.Run(loc, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/private" {
					hits.Add(1)
					return
				}
				http.Redirect(w, r, loc, http.StatusTemporaryRedirect)
			}))
			defer s.Close()
			_, err := client(t, s.URL, 100).Call(context.Background(), "getRun", json.RawMessage(validArgs))
			errorCode(t, err, "redirect_denied")
			if Status(err) != 307 {
				t.Error("missing redirect status")
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatal("followed redirect")
	}
}

func TestResponseErrors(t *testing.T) {
	for _, tt := range []struct {
		status int
		body   string
		code   string
	}{
		{200, strings.Repeat("x", 33), "response_truncated"},
		{200, "super.private.token", "upstream_error"},
		{403, "Authorization: Bearer super.private.token", "upstream_error"},
	} {
		t.Run(fmt.Sprintf("%d-%s", tt.status, tt.code), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			}))
			defer s.Close()
			_, err := client(t, s.URL, 32).Call(context.Background(), "getRun", json.RawMessage(validArgs))
			errorCode(t, err, tt.code)
			if strings.Contains(err.Error(), "super.private.token") {
				t.Fatal("error leaked credential")
			}
		})
	}
}

func TestCancelUpstream(t *testing.T) {
	started := make(chan struct{})
	aborted := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(aborted)
	}))
	defer s.Close()
	c := client(t, s.URL, 128)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.Call(ctx, "getRun", json.RawMessage(validArgs))
		done <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("request never started")
	}
	cancel()
	errorCode(t, <-done, "canceled")
	select {
	case <-aborted:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not observe cancellation")
	}
}

func TestRejectsUnpinnedAndAmbiguousBaseURLs(t *testing.T) {
	cat, err := catalog.Parse([]byte(spec), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{
		"//localhost", "ftp://localhost/a", "https://token@localhost", "http://localhost/a?tenant=1",
		"http://localhost/x#anchor", "http://localhost//", "http://localhost/safe/../admin",
		"http://localhost/api%2fadmin", "http://localhost/prefix%25",
	} {
		if _, err := New(cat, Config{BaseURL: u}); err == nil {
			t.Errorf("accepted ambiguous base %q", u)
		}
	}
}

func TestBearerRequirementCheckedWithoutDiscovery(t *testing.T) {
	withAuth := strings.Replace(spec, `"paths":`, `"security":[{"bearer":[]}],
		"components":{"securitySchemes":{"bearer":{"type":"http","scheme":"bearer"}}},"paths":`, 1)
	cat, err := catalog.Parse([]byte(withAuth), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("missing credential reached upstream")
	}))
	defer s.Close()
	c, err := New(cat, Config{BaseURL: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Call(context.Background(), "getRun", json.RawMessage(validArgs))
	errorCode(t, err, "denied")
}

func TestDeclaredCredentialHeadersAndRedaction(t *testing.T) {
	const source = `openapi: 3.1.0
paths:
  /feed:
    parameters: [{$ref: '#/components/parameters/key'}]
    get:
      operationId: feed
      parameters:
        - {in: header, name: API-Username, required: true, schema: {type: string}}
        - {in: query, name: q42, schema: {type: integer}}
      responses: &responses {'200': {content: {application/json: {schema: {type: object}}}}}
  /key:
    get: {operationId: key, parameters: [{$ref: '#/components/parameters/key'}], responses: *responses}
  /public:
    get: {operationId: public, responses: *responses}
    post: {operationId: write, responses: *responses}
components:
  parameters:
    key: {in: header, name: api-key, required: true, schema: {type: string}}
`
	cat, err := catalog.Parse([]byte(source), []string{"feed", "key", "public", "write"})
	if err != nil {
		t.Fatal(err)
	}
	const key, owner = "42-private-key", "42"
	headers := map[string]string{"api-KEY": key, "Api-Username": owner}
	cat = cat.WithCredentials(true, headers)
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		wantKey, wantOwner := "", ""
		switch r.URL.Path {
		case "/feed":
			wantKey, wantOwner = key, owner
		case "/key":
			wantKey = key
		case "/public":
		default:
			t.Errorf("unexpected target %s", r.URL.Path)
		}
		if r.Header.Get("Api-Key") != wantKey || r.Header.Get("Api-Username") != wantOwner || r.Header.Get("Authorization") != "Bearer host-bearer" {
			t.Errorf("incorrect credentials for %s %s", r.Method, r.URL.Path)
		}
		io.WriteString(w, `{"key":"42-private-key","actor":"42","echoNumber":42,"id":1420,"api_username":"other-user","log":"Api-Username: other-owner","42":true}`)
	}))
	defer upstream.Close()
	ctx := context.Background()
	c, err := New(cat, Config{BaseURL: upstream.URL, CredentialHeaders: headers, BearerToken: "host-bearer"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	delete(headers, "api-KEY") // the validated client owns its bindings
	for _, id := range []string{"feed", "key", "public", "write"} {
		out, err := c.Call(ctx, id, json.RawMessage("{}"))
		if err != nil {
			t.Fatal(err)
		}
		want := `{"42":true,"actor":"[REDACTED]","api_username":"[REDACTED]","echoNumber":"[REDACTED]","id":1420,"key":"[REDACTED]","log":"Api-Username: [REDACTED]"}`
		if string(out) != want {
			t.Fatalf("unexpected scrubbed response: %s", out)
		}
	}
	missing, err := New(cat, Config{BaseURL: upstream.URL, CredentialHeaders: headers})
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Close()
	_, err = missing.Call(ctx, "feed", json.RawMessage("{}"))
	errorCode(t, err, "denied")
	_, err = c.Call(ctx, "feed", json.RawMessage(`{"headers":{"Api-Username":"new-owner"}}`))
	errorCode(t, err, "invalid_arguments")
	_, err = c.Call(ctx, "feed", json.RawMessage(`{"query":{"q42":"bad"}}`))
	errorCode(t, err, "invalid_arguments")
	if strings.Contains(err.Error(), owner) || hits.Load() != 4 {
		t.Fatalf("unsafe error or unexpected upstream call: %v (calls %d)", err, hits.Load())
	}
}

func TestResponseFieldNamesAndCredentials(t *testing.T) {
	cat, err := catalog.Parse([]byte(spec), []string{"getRun"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, key, body string
		reject          bool
	}{
		{"Discourse admin field", "key-secret", `{"admin":true,"admin_status":"admin"}`, false},
		{"API key inside a nested field name", "key-secret", `{"rows":[{"first-key-secret":1,"first-[REDACTED]":2}]}`, true},
		{"bearer inside a field name", "key-secret", `{"by-bearer-secret":1}`, true},
		{"key equal to username", "admin", `{"admin":true}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, tc.body)
			}))
			defer s.Close()
			c, err := New(cat, Config{BaseURL: s.URL, BearerToken: "bearer-secret", CredentialHeaders: map[string]string{"Api-Key": tc.key, "Api-Username": "admin"}})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			out, status, err := c.CallWithStatus(context.Background(), "getRun", json.RawMessage(validArgs))
			if tc.reject {
				errorCode(t, err, "sensitive_response")
				if out != nil || status != http.StatusOK || strings.Contains(err.Error(), tc.key) || strings.Contains(err.Error(), "bearer-secret") {
					t.Fatalf("unsafe failure: body %s, status %d, error %v", out, status, err)
				}
				return
			}
			if err != nil || string(out) != `{"admin":true,"admin_status":"[REDACTED]"}` {
				t.Fatalf("incorrect structural fields or echoed value: %s, %v", out, err)
			}
		})
	}
}

func TestCredentialHeaderConfiguration(t *testing.T) {
	cat, err := catalog.Parse([]byte(spec), nil)
	if err != nil {
		t.Fatal(err)
	}
	cat.BaseURL = "https://spec.example.test"
	for _, tc := range []struct {
		name    string
		base    string
		headers map[string]string
	}{
		{"spec supplied origin", "", map[string]string{"Api-Key": "private-key"}},
		{"cleartext remote origin", "http://example.test", map[string]string{"Api-Key": "private-key"}},
		{"duplicate binding", "https://example.test", map[string]string{"Api-Key": "private-key", "API-KEY": "private-key"}},
		{"forbidden header", "https://example.test", map[string]string{"Host": "private-key"}},
		{"invalid value", "https://example.test", map[string]string{"Api-Key": "private-key\r\nX-Tenant: one"}},
		{"empty binding", "https://example.test", map[string]string{"Api-Key": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if c, err := New(cat, Config{BaseURL: tc.base, CredentialHeaders: tc.headers}); err == nil {
				c.Close()
				t.Fatal("accepted credential configuration")
			} else if strings.Contains(err.Error(), "private-key") {
				t.Fatal("credential in configuration error")
			}
		})
	}
}
