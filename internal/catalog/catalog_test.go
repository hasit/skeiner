package catalog

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestFixtureSearchAndReferences(t *testing.T) {
	c, err := Load("../../examples/ci/openapi.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Len() != 5 || len(c.Warnings) != 0 {
		t.Fatalf("loaded %d operations, warnings %v", c.Len(), c.Warnings)
	}
	for _, query := range []string{"failed job logs", "cursor", "getAttemptLogs"} {
		results := c.Search(query)
		if len(results) == 0 || results[0].ID != "getAttemptLogs" {
			t.Fatalf("%q: %v", query, results)
		}
	}
	op, _ := c.Lookup("getAttemptLogs")
	if len(op.Parameters) != 2 || op.Parameters[0].Name != "attemptId" || !op.Parameters[0].Required {
		t.Fatalf("parameter resolution: %v", op.Parameters)
	}
	b, _ := json.Marshal(op.Response)
	for _, s := range []string{`"nextCursor"`, `"nullable":true`, `"number":{"type":"integer"}`, `"text":{"type":"string"}`} {
		if !strings.Contains(string(b), s) {
			t.Fatalf("missing %s in %s", s, b)
		}
	}
	restricted, err := Load("../../examples/ci/openapi.yaml", []string{"listRuns"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := restricted.Lookup("getAttemptLogs"); ok {
		t.Fatal("denied ID available to dispatch")
	}
	for _, match := range restricted.Search("getAttemptLogs") {
		if match.ID != "listRuns" {
			t.Fatalf("denied operation in search: %q", match.ID)
		}
	}
	empty, err := Load("../../examples/ci/openapi.yaml", []string{})
	if err != nil || empty.Len() != 0 {
		t.Fatalf("empty allowlist must expose zero operations")
	}
}

func simpleOp(id string) obj {
	return obj{"operationId": id, "responses": obj{"200": obj{"content": obj{"application/json": obj{"schema": obj{"type": "string"}}}}}}
}
func unnamedOp() obj {
	op := simpleOp("")
	delete(op, "operationId")
	return op
}
func rootDoc(paths obj) obj {
	return obj{"openapi": "3.1.0", "info": obj{"title": "Unit", "version": "1"}, "paths": paths}
}
func fromDoc(t *testing.T, doc obj, allow []string, getPaths ...string) *Catalog {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseSelected(data, allow, getPaths)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSelectExactGET(t *testing.T) {
	doc := rootDoc(obj{
		"/holidays/{country}": obj{"get": unnamedOp(), "post": simpleOp("publish"), "parameters": []any{
			obj{"name": "country", "in": "path", "required": true, "schema": obj{"type": "string"}},
		}},
		"/holidays/today": obj{"get": simpleOp("today")},
		"/countries":      obj{"get": simpleOp("listCountries")},
		"/writes":         obj{"put": unnamedOp()},
		"/malformed":      obj{"get": simpleOp("")},
	})
	const key = "GET /holidays/{country}"
	byID := fromDoc(t, doc, []string{key})
	if byID.Len() != 0 {
		t.Fatalf("ID allowlist enabled an unnamed GET: %v", byID.Inventory())
	}
	for _, op := range byID.Inventory() {
		if strings.Contains(op.Reason, "--allow-get") != (op.Method == "GET" && op.Path == "/holidays/{country}") {
			t.Fatalf("incorrect selector guidance for %+v", op)
		}
	}
	for _, allow := range [][]string{nil, {}, {"today"}} {
		c := fromDoc(t, doc, allow, "/holidays/{country}", "/countries")
		if c.Len() != 2+len(allow) {
			t.Fatalf("allow %v: unexpected selection: %v", allow, c.Inventory())
		}
		if _, ok := c.Lookup("listCountries"); !ok {
			t.Fatal("path-selected named GET lost its declared operationId")
		}
		op, ok := c.Lookup(key)
		if !ok || op.ID != key || op.Method != "GET" || !c.MatchesPath(op, "/holidays/DE") || c.MatchesPath(op, "/holidays/today") {
			t.Fatalf("generated ID or route scope incorrect: %v", op)
		}
		if hits := c.Search(key); len(hits) == 0 || hits[0].ID != key {
			t.Fatalf("search did not expose execution key: %v", hits)
		}
	}
	missing := []string{"/holidays/DE", "/holidays/*", " /countries", "", "/writes"}
	c := fromDoc(t, doc, nil, missing...)
	if c.Len() != 0 {
		t.Fatalf("unmatched templates admitted operations: %v", c.Inventory())
	}
	warnings := strings.Join(c.Warnings, "\n")
	for _, path := range missing {
		if !strings.Contains(warnings, strconv.Quote(path)) {
			t.Fatalf("missing warning for selector %q in %s", path, warnings)
		}
	}
}

func TestSelectedGETCollisionWithOmittedOperations(t *testing.T) {
	const key = "GET /countries"
	for name, write := range map[string]obj{
		"declared write":   {"post": simpleOp(key)},
		"custom operation": {"additionalOperations": obj{"PURGE": simpleOp(key)}},
	} {
		t.Run(name, func(t *testing.T) {
			doc := rootDoc(obj{"/countries": obj{"get": unnamedOp()}, "/other": write})
			doc["openapi"] = "3.2.0"
			c := fromDoc(t, doc, nil, "/countries")
			if c.Len() != 0 {
				t.Fatal("selected an ambiguous execution key")
			}
			for _, op := range c.Inventory() {
				if op.ID != key || op.Reason != "ambiguous operationId" {
					t.Fatalf("ambiguous declaration incorrectly reported: %+v", op)
				}
			}
		})
	}
}

func TestSelectedGETRequiresValidDeclarationAndSecurity(t *testing.T) {
	secured := unnamedOp()
	secured["security"] = []any{obj{"bearer": []any{}}}
	keyOnly := unnamedOp()
	keyOnly["security"] = []any{obj{"key": []any{}}}
	malformed := simpleOp("")
	malformed["operationId"] = nil
	long := "/" + strings.Repeat("a", 252)
	doc := rootDoc(obj{
		"/private": obj{"get": secured},
		"/api-key": obj{"get": keyOnly},
		"/bad-id":  obj{"get": malformed},
		long:       obj{"get": unnamedOp()},
	})
	doc["components"] = obj{"securitySchemes": obj{"bearer": obj{"type": "http", "scheme": "bearer"}}}
	c := fromDoc(t, doc, nil, "/private", "/api-key", "/bad-id", long)
	if c.Len() != 1 || c.WithoutBearer().Len() != 0 {
		t.Fatalf("incorrect authentication or ID admission: %v", c.Inventory())
	}
	want := map[string]string{"/private": "bearer token", "/api-key": "unsupported scheme", "/bad-id": "operationId", long: "oversized operationId"}
	for _, op := range c.WithoutBearer().Inventory() {
		if !strings.Contains(op.Reason, want[op.Path]) {
			t.Fatalf("missing exclusion reason for %s: %s", op.Path, op.Reason)
		}
	}
}

func TestRejectUnsupportedAndAmbiguousOperations(t *testing.T) {
	body := simpleOp("postItem")
	body["requestBody"] = obj{}
	header := simpleOp("headerOp")
	header["parameters"] = []any{obj{"in": "header", "name": "tenant", "schema": obj{"type": "string"}}}
	foreign := simpleOp("remote")
	foreign["responses"] = obj{"200": obj{"$ref": "file:///etc/passwd"}}
	paths := obj{
		"/a": obj{"get": simpleOp("copy")},
		"/b": obj{"get": simpleOp("copy"), "post": body},
		"/c": obj{"get": simpleOp(""), "put": header},
		"/d": obj{"get": foreign},
		"/v": obj{"get": simpleOp("visible")},
	}
	c := fromDoc(t, rootDoc(paths), nil)
	if c.Len() != 1 {
		t.Fatalf("expected one safe operation: %v", c.ops)
	}
	log := strings.Join(c.Warnings, "\n")
	for _, s := range []string{"ambiguous operationId", "missing operationId", "GET /d (remote)", "requestBody", "header"} {
		if !strings.Contains(log, s) {
			t.Fatalf("%q missing from warnings: %s", s, log)
		}
	}
}

func TestPathScopeAndOverride(t *testing.T) {
	get := simpleOp("getUser")
	p := obj{"name": "id", "in": "path", "required": true, "schema": obj{"type": "string"}}
	get["parameters"] = []any{obj{"$ref": "#/components/parameters/ID"}}
	doc := rootDoc(obj{
		"/users/{id}": obj{"parameters": []any{p}, "get": get},
		"/users/me":   obj{"get": simpleOp("me")},
	})
	doc["components"] = obj{"parameters": obj{"ID": p}}
	c := fromDoc(t, doc, []string{"getUser"})
	op, ok := c.Lookup("getUser")
	if !ok || len(op.Parameters) != 1 {
		t.Fatalf("operation parameter should override path item: %v", c.Warnings)
	}
	if !c.MatchesPath(op, "/users/123") || c.MatchesPath(op, "/users/me") {
		t.Fatal("operation allowlist bypass through a concrete route")
	}
}

func TestAnonymousAndBearerAlternatives(t *testing.T) {
	secure := simpleOp("secure")
	mixed := simpleOp("unsupported")
	mixed["security"] = []any{obj{"key": []any{}}}
	anon := simpleOp("anonymous")
	anon["security"] = []any{obj{"key": []any{}}, obj{}}
	doc := rootDoc(obj{"/a": obj{"get": secure}, "/b": obj{"get": mixed}, "/c": obj{"get": anon}})
	doc["security"] = []any{obj{"bearer": []any{}}}
	doc["components"] = obj{"securitySchemes": obj{
		"bearer": obj{"type": "http", "scheme": "Bearer"},
		"key":    obj{"type": "apiKey", "in": "query", "name": "api_key"},
	}}
	c := fromDoc(t, doc, nil)
	if c.Len() != 2 || c.WithoutBearer().Len() != 1 {
		t.Fatalf("security requirements: %d %v", c.Len(), c.Warnings)
	}
	if _, ok := c.WithoutBearer().Lookup("anonymous"); !ok {
		t.Fatal("anonymous alternative missing")
	}
}

func TestSearchDescribesScalarParameterEnums(t *testing.T) {
	read := simpleOp("read")
	read["parameters"] = []any{
		obj{"in": "path", "name": "revision", "required": true, "schema": obj{"$ref": "#/components/schemas/Revision"}},
		obj{"in": "query", "name": "limit", "schema": obj{"type": "integer", "enum": []any{1, 10}}},
		obj{"in": "query", "name": "ratio", "schema": obj{"type": "number", "enum": []any{0, 0.25}}},
		obj{"in": "query", "name": "verified", "schema": obj{"type": "boolean", "enum": []any{false}}},
		obj{"in": "query", "name": "cursor", "schema": obj{"type": "string"}},
		obj{"in": "query", "name": "label", "schema": obj{"type": "string", "enum": []any{"accepted", strings.Repeat("long label", 200)}}},
		obj{"in": "query", "name": "item", "schema": obj{"type": "integer", "enum": []any{uint64(9007199254740993), 5}}},
	}
	doc := rootDoc(obj{"/artifacts/{revision}": obj{"get": read}})
	doc["components"] = obj{"schemas": obj{"Revision": obj{"type": "string", "enum": []any{"before", "after"}}}}
	hits := fromDoc(t, doc, nil).Search("read")
	if len(hits) != 1 {
		t.Fatalf("discovery returned %d operations", len(hits))
	}
	// Decode the values the client sees to check presence, order and JSON types.
	data, err := json.Marshal(hits[0].Parameters)
	if err != nil {
		t.Fatal(err)
	}
	var parameters []struct {
		Name          string
		Enum          json.RawMessage
		EnumTruncated bool
	}
	if err := json.Unmarshal(data, &parameters); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range parameters {
		got[p.Name] = string(p.Enum)
		if p.EnumTruncated != (p.Name == "label" || p.Name == "item") {
			t.Errorf("incorrect completeness for %s", p.Name)
		}
	}
	want := map[string]string{
		"revision": `["before","after"]`, "limit": "[1,10]", "ratio": "[0,0.25]", "verified": "[false]",
		"cursor": "", "label": `["accepted"]`, "item": "[5]",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("model-visible choices %v, want %v", got, want)
	}
}

func TestSearchDescribesResponseEnumValues(t *testing.T) {
	read := simpleOp("listReviews")
	properties := obj{
		"state":    obj{"$ref": "#/components/schemas/State"},
		"decision": obj{"type": []any{"string", "null"}, "enum": []any{"approved", "declined", nil, false}},
		"level":    obj{"type": "integer", "enum": []any{1, 1.5, 2}},
		"fraction": obj{"type": "number", "enum": []any{0.5, -0.25}},
		"enabled":  obj{"type": "boolean", "enum": []any{false, true}},
		"missing":  obj{"type": "null", "enum": []any{nil}},
		"plain":    obj{"type": "string"},
		"id":       obj{"type": "integer", "enum": []any{uint64(9007199254740993), 7}},
	}
	read["responses"] = obj{"200": obj{"content": obj{"application/json": obj{"schema": obj{
		"type": "array", "items": obj{"type": "object", "properties": properties},
	}}}}}
	doc := rootDoc(obj{"/reviews": obj{"get": read}})
	doc["openapi"] = "3.1.0"
	doc["components"] = obj{"schemas": obj{"State": obj{"type": "string", "enum": []any{"open", "closed", false, obj{"nested": true}}}}}
	hits := fromDoc(t, doc, nil).Search("listReviews")
	if len(hits) != 1 {
		t.Fatalf("expected usable operation, got %d", len(hits))
	}
	data, err := json.Marshal(hits[0].Response["200"])
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Items struct {
			Properties map[string]struct {
				Enum          json.RawMessage
				EnumTruncated bool
				Nullable      bool
			}
		}
	}
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"state": `["open","closed"]`, "decision": `["approved","declined",null]`,
		"level": `[1,2]`, "fraction": `[0.5,-0.25]`, "enabled": `[false,true]`,
		"missing": `[null]`, "id": `[7]`, "plain": "",
	}
	for key, choices := range want {
		field, ok := response.Items.Properties[key]
		if !ok || string(field.Enum) != choices || field.EnumTruncated != (key == "id") || field.Nullable != (key == "decision") {
			t.Errorf("field %q: %+v, want choices %s", key, field, choices)
		}
	}
}

func TestResponseEnumDiscoveryBudgetAcrossStatuses(t *testing.T) {
	read := simpleOp("read")
	responses := obj{}
	for i := 0; i < 8; i++ {
		// Escaping quotes makes the encoded strings larger than their UTF-8 length.
		schema := obj{"type": "string", "enum": []any{"first", strings.Repeat(`"`, 495), strings.Repeat("x", 1500)}}
		responses[strconv.Itoa(200+i)] = obj{"content": obj{"application/json": obj{"schema": schema}}}
	}
	read["responses"] = responses
	c := fromDoc(t, rootDoc(obj{"/read": obj{"get": read}}), nil)
	hits := c.Search("read")
	if len(hits) != 1 || len(hits[0].Response) != len(responses) {
		t.Fatalf("expected every success status to remain discoverable")
	}
	total := 0
	for status, raw := range hits[0].Response {
		field := asObj(raw)
		values, _ := field["enum"].(json.RawMessage)
		if field["type"] != "string" || field["enumTruncated"] != true || len(values) > maxEnumBytes {
			t.Fatalf("status %s lost its shape or exceeded its budget: %s", status, values)
		}
		if status == "200" {
			var choices []string
			if json.Unmarshal(values, &choices) != nil || !reflect.DeepEqual(choices, []string{"first", strings.Repeat(`"`, 495)}) {
				t.Fatalf("expected two whole literals for the first status, got %s", values)
			}
		}
		total += len(values)
	}
	if total > maxResponseEnumBytes {
		t.Errorf("response enum arrays use %d bytes, limit is %d", total, maxResponseEnumBytes)
	}
}

func TestCredentialHeadersRequireExplicitSelectionAndBindings(t *testing.T) {
	key := obj{"in": "header", "name": "API-KEY", "required": true, "schema": obj{"type": "string"}}
	username := obj{"in": "header", "name": "api-username", "style": "simple", "required": true, "schema": obj{"type": "string"}}
	get := simpleOp("latest")
	get["parameters"] = []any{username}
	doc := rootDoc(obj{"/latest.json": obj{"parameters": []any{key}, "get": get, "post": simpleOp("publish")}})
	values := map[string]string{"api-key": "secret-key-value", "API-USERNAME": "secret-owner"}
	if c := fromDoc(t, doc, nil).WithCredentials(true, values); c.Len() != 0 {
		t.Fatalf("default selection exposed header operations: %v", c.Inventory())
	}
	c := fromDoc(t, doc, []string{"latest", "publish"})
	for _, tc := range []struct {
		headers map[string]string
		reason  string
	}{
		{nil, "Api-Key, Api-Username"},
		{map[string]string{"api-key": "bound"}, "Api-Username"},
		{map[string]string{"api-key": "one", "Api-Key": "two", "Api-Username": "owner"}, "Api-Key"},
	} {
		view := c.WithCredentials(false, tc.headers)
		if view.Len() != 0 || len(view.Search("latest")) != 0 {
			t.Fatalf("missing credentials exposed operation: %v", view.Inventory())
		}
		if _, ok := view.Lookup("latest"); ok || !strings.HasSuffix(view.Inventory()[0].Reason, tc.reason) {
			t.Fatalf("incorrect exclusion: %v", view.Inventory())
		}
	}
	bound := c.WithCredentials(true, values).WithoutBearer()
	op, ok := bound.Lookup("latest")
	if !ok || len(op.Parameters) != 0 || bound.Len() != 1 || c.Len() != 0 {
		t.Fatalf("incorrect credentials or mutable policy: %v", bound.Inventory())
	}
	hits := bound.Search("latest")
	b, err := json.Marshal(hits)
	if err != nil || len(hits) != 1 || strings.Contains(strings.ToLower(string(b)), "api-key") || strings.Contains(string(b), "secret-") {
		t.Fatalf("credential data in discovery or missing operation: %s, %v", b, err)
	}
}

func TestRejectUnsupportedCredentialHeaderDeclarations(t *testing.T) {
	header := func(name string) obj {
		return obj{"name": name, "in": "header", "required": true, "schema": obj{"type": "string"}}
	}
	for _, tc := range []struct {
		name   string
		field  string
		value  any
		reason string
	}{
		{"unknown header", "name", "X-Tenant", "unsupported"},
		{"optional header", "required", false, "optional"},
		{"non-string header", "schema", obj{"type": "integer"}, "string schema"},
		{"complex serialization", "style", "matrix", "style"},
		{"malformed serialization", "explode", "false", "explode"},
		{"case-insensitive duplicate", "", nil, "duplicate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := header("Api-Key")
			params := []any{p}
			if tc.field == "" {
				params = append(params, header("api-key"))
			} else {
				p[tc.field] = tc.value
			}
			get := simpleOp("read")
			get["parameters"] = params
			c := fromDoc(t, rootDoc(obj{"/read": obj{"get": get}}), []string{"read"}).WithCredentials(false, map[string]string{"Api-Key": "bound"})
			if c.Len() != 0 || !strings.Contains(c.Inventory()[0].Reason, tc.reason) {
				t.Fatalf("incorrect decision: %v", c.Inventory())
			}
		})
	}
}

func TestSchemaLimitAndDeterministicRanking(t *testing.T) {
	paths := obj{}
	for _, id := range []string{"z", "j", "f", "i", "g", "c", "a", "d", "b", "e", "h"} {
		op := simpleOp(id)
		op["tags"] = []any{"shared"}
		paths["/"+id] = obj{"get": op}
	}
	c := fromDoc(t, rootDoc(paths), nil)
	result := c.Search("shared")
	if len(result) != 8 || result[0].ID != "a" || result[7].ID != "h" {
		t.Fatalf("unstable ranking: %v", result)
	}
	cycle := simpleOp("cycle")
	cycle["responses"] = obj{"200": obj{"content": obj{"application/json": obj{"schema": obj{"$ref": "#/components/schemas/Tree"}}}}}
	doc := rootDoc(obj{"/tree": obj{"get": cycle}})
	doc["components"] = obj{"schemas": obj{"Tree": obj{"type": "object", "properties": obj{"children": obj{"type": "array", "items": obj{"$ref": "#/components/schemas/Tree"}}}}}}
	bounded := fromDoc(t, doc, nil)
	op, ok := bounded.Lookup("cycle")
	if !ok {
		t.Fatal(bounded.Warnings)
	}
	b, _ := json.Marshal(op.Response)
	if !strings.Contains(string(b), `"truncated":true`) {
		t.Fatal(string(b))
	}
}

func TestReadWithNullableAndUnspecifiedResponseFields(t *testing.T) {
	// Currency and forum APIs use nullable fields and arrays with unspecified items.
	response := obj{"200": obj{"content": obj{"application/json": obj{"schema": obj{
		"type": "object", "properties": obj{
			"start_date": obj{"type": []any{"string", "null"}},
			"aliases":    obj{"type": []any{"null", "array"}, "items": obj{}},
			"history":    obj{"type": "array"},
		},
	}}}}}
	op := simpleOp("readCurrency")
	op["responses"] = response
	write := simpleOp("updateCurrency")
	write["responses"] = response
	c := fromDoc(t, rootDoc(obj{"/currency": obj{"get": op, "put": write}}), nil)
	read, ok := c.Lookup("readCurrency")
	if !ok {
		t.Fatalf("currency read excluded: %v", c.Warnings)
	}
	if _, ok := c.Lookup("updateCurrency"); ok {
		t.Fatal("supporting more read descriptions silently enabled a write")
	}
	want := obj{"200": obj{"type": "object", "properties": obj{
		"start_date": obj{"type": "string", "nullable": true},
		"aliases":    obj{"type": "array", "nullable": true, "items": obj{"incomplete": true}},
		"history":    obj{"type": "array", "items": obj{"incomplete": true}},
	}}}
	if !reflect.DeepEqual(read.Response, want) {
		t.Fatalf("unexpected currency response description: %#v", read.Response)
	}
}

func TestMalformedYAML(t *testing.T) {
	for _, doc := range []string{"openapi: 3.0.3\npaths: {}\npaths: {}", "openapi: 3.0.3\npaths: {}\n---\npaths: {}", "openapi: 2.0\npaths: {}"} {
		if _, err := Parse([]byte(doc), nil); err == nil {
			t.Fatalf("accepted invalid doc %q", doc)
		}
	}
}
