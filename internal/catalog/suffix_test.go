package catalog

import (
	"strings"
	"testing"
)

func TestSelectedFileExtensionGET(t *testing.T) {
	const path = "/topics/{id}.json"
	id := obj{"name": "id", "in": "path", "required": true, "schema": obj{"type": "integer"}}
	doc := rootDoc(obj{
		path:                      obj{"parameters": []any{id}, "get": simpleOp("topic"), "put": simpleOp("updateTopic")},
		"/topics/entry-{id}.json": obj{"parameters": []any{id}, "get": simpleOp("prefixed")},
	})
	defaults := fromDoc(t, doc, nil)
	if defaults.Len() != 0 {
		t.Fatalf("default selection admitted a new operation: %v", defaults.Inventory())
	}
	for _, op := range defaults.Inventory() {
		if op.Path == path && op.Method == "GET" && !strings.Contains(op.Reason, "--allow-get") {
			t.Errorf("missing selection guidance: %+v", op)
		}
	}
	for _, c := range []*Catalog{
		fromDoc(t, doc, []string{"topic", "updateTopic", "prefixed"}),
		fromDoc(t, doc, nil, path),
	} {
		op, ok := c.Lookup("topic")
		if !ok || c.Len() != 1 || len(op.Parameters) != 1 || op.Parameters[0].Name != "id" {
			t.Fatalf("expected the explicitly selected GET and its input: %v", c.Inventory())
		}
	}
}

func TestFileExtensionRouteScope(t *testing.T) {
	id := obj{"name": "id", "in": "path", "required": true, "schema": obj{"type": "string"}}
	doc := rootDoc(obj{
		"/topics/{id}.json":   obj{"parameters": []any{id}, "get": unnamedOp()},
		"/topics/{id}.xml":    obj{"$ref": "#/components/pathItems/unknown"},
		"/topics/latest.json": obj{"get": simpleOp("latest")},
		"/groups/{id}":        obj{"parameters": []any{id}, "get": simpleOp("group")},
		"/groups/{id}.json":   obj{"parameters": []any{id}, "get": simpleOp("groupJSON")},
		"/groups/{id}.txt":    obj{"$ref": "#/components/pathItems/unresolved"},
	})
	c := fromDoc(t, doc, []string{"group"}, "/topics/{id}.json", "/groups/{id}.json")
	for _, tc := range []struct {
		id   string
		path string
		want bool
	}{
		{"GET /topics/{id}.json", "/topics/42.json", true},
		{"GET /topics/{id}.json", "/topics/latest.json", false},
		{"GET /topics/{id}.json", "/topics/42.xml", false},
		{"group", "/groups/team", true},
		{"group", "/groups/team.json", false},
		{"group", "/groups/team.txt", false},
		{"groupJSON", "/groups/team.json", false},
	} {
		op, ok := c.Lookup(tc.id)
		if !ok {
			t.Fatalf("missing %s: %v", tc.id, c.Inventory())
		}
		if got := c.MatchesPath(op, tc.path); got != tc.want {
			t.Errorf("%s matching %s: got %v, want %v", tc.id, tc.path, got, tc.want)
		}
	}
}
