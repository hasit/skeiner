package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hasit/skeiner/internal/eval"
	"github.com/hasit/skeiner/internal/fixture"
	"github.com/hasit/skeiner/internal/sandbox"
	"github.com/hasit/skeiner/internal/transport"
	"github.com/hasit/skeiner/internal/yamljson"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func examplePaths() (string, string) {
	return filepath.Join("..", "..", "examples", "ci", "openapi.yaml"), filepath.Join("..", "..", "evals", "ci.yaml")
}

func TestRunSavedProgram(t *testing.T) {
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.Method != "GET" || r.URL.Path != "/runs/814" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		io.WriteString(w, `{"id":"814","status":"failed"}`)
	}))
	defer server.Close()
	spec, _ := examplePaths()
	path := filepath.Join(t.TempDir(), "investigate.js")
	args := []string{"run", "--spec", spec, "--base-url", server.URL, "--allow", "getRun", "--program", path}
	for _, test := range []struct {
		name, code, mode, want, errCode string
	}{
		{"auto await", `return (await api.call("getRun",{path:{runId:"814"}})).status;`, "", `"failed"`, ""},
		{"explicit async", `return api.call("getRun",{path:{runId:"814"}}).then(r=>r.id);`, "async", `"814"`, ""},
		{"denied", `return api.call("listRuns",{query:{sha:"abc123"}});`, "", "", "denied"},
		{"size limit", strings.Repeat("x", sandbox.MaxCodeBytes+1), "", "", "code_limit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(test.code), 0600); err != nil {
				t.Fatal(err)
			}
			options := args
			if test.mode != "" {
				options = append(options, "--mode", test.mode)
			}
			var out bytes.Buffer
			err := run(context.Background(), options, &out, io.Discard)
			if test.errCode == "" {
				if err != nil || strings.TrimSpace(out.String()) != test.want {
					t.Fatalf("run returned %q, %v", out.String(), err)
				}
			} else if err == nil || !strings.HasPrefix(err.Error(), test.errCode+":") || out.Len() != 0 {
				t.Fatalf("want %s and empty stdout, got %v and %q", test.errCode, err, out.String())
			}
		})
	}
	if reads.Load() != 2 {
		t.Fatalf("want two authorized reads, got %d", reads.Load())
	}
}

func TestRunUnchangedProgramWithJSONInputs(t *testing.T) {
	handler, err := fixture.New("all")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	dir := t.TempDir()
	program := filepath.Join(dir, "status.js")
	input := filepath.Join(dir, "request.json")
	if err := os.WriteFile(program, []byte(`return (await api.call("getRun", {path:{runId:api.input.id}})).status;`), 0600); err != nil {
		t.Fatal(err)
	}
	spec, _ := examplePaths()
	args := []string{"run", "--spec", spec, "--base-url", server.URL, "--allow", "getRun", "--program", program, "--input-file", input}
	for _, tc := range []struct{ input, status string }{
		{`{"id":"814"}`, `"failed"`},
		{`{"id":"815"}`, `"passed"`},
	} {
		if err := os.WriteFile(input, []byte(tc.input), 0600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := run(context.Background(), args, &out, io.Discard); err != nil || strings.TrimSpace(out.String()) != tc.status {
			t.Fatalf("input %s returned %s, %v", tc.input, out.String(), err)
		}
	}
}

func TestAcceptanceAndRegression(t *testing.T) {
	spec, suite := examplePaths()
	for _, test := range []struct {
		name    string
		options []string
		passed  bool
		count   int
	}{
		{"golden", nil, true, 3},
		{"remove earlier assertion page", []string{"--case", "assertion", "--state", "missing-assertion"}, false, 1},
		{"restore snapshot", []string{"--case", "assertion"}, true, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var b bytes.Buffer
			err := run(context.Background(), append([]string{"eval", "--spec", spec, "--suite", suite}, test.options...), &b, io.Discard)
			if (err == nil) != test.passed {
				t.Fatalf("exit: %v\n%s", err, b.String())
			}
			var report eval.Report
			if err := json.Unmarshal(b.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if report.Passed() != test.passed || len(report.Results) != test.count {
				t.Fatalf("wrong evaluation outcome: %+v", report)
			}
			for _, r := range report.Results {
				if len(r.Trace) == 0 || (!r.Passed && r.Diagnosis == "") {
					t.Fatalf("missing diagnostics: %+v", r)
				}
			}
		})
	}
}

func TestSavedProgramMissingEvidence(t *testing.T) {
	spec, suite := examplePaths()
	suite = filepath.Join(filepath.Dir(suite), "log-scan.yaml")
	program := filepath.Join(filepath.Dir(spec), "find-lines.js")
	var out bytes.Buffer
	err := run(context.Background(), []string{"eval", "--spec", spec, "--suite", suite,
		"--program", program, "--case", "early-failure", "--state", "missing-assertion"}, &out, io.Discard)
	if err == nil {
		t.Fatal("expected a failed evaluation when the required log record is missing")
	}
	var report eval.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Driver != "saved-program" || len(report.Results) != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	result := report.Results[0]
	if result.Passed || string(result.Answer.Facts["matches"]) != "0" || !strings.Contains(result.Diagnosis, "not retrieved") {
		t.Fatalf("expected a valid zero-match answer and a failed evidence check: %+v", result)
	}
}

func TestEvalKeepsCredentialsAtConfiguredBase(t *testing.T) {
	spec, suite := examplePaths()
	raw, err := os.ReadFile(spec)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yamljson.Decode(raw, &doc); err != nil {
		t.Fatal(err)
	}
	getRun := doc["paths"].(map[string]any)["/runs/{runId}"].(map[string]any)
	getRun["parameters"] = []any{map[string]any{
		"in": "header", "name": "Api-Key", "required": true, "schema": map[string]any{"type": "string"},
	}}
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	spec = filepath.Join(t.TempDir(), "api.json")
	if err := os.WriteFile(spec, raw, 0600); err != nil {
		t.Fatal(err)
	}
	data, err := fixture.New("assertion")
	if err != nil {
		t.Fatal(err)
	}
	const key = "example-state-credential"
	t.Setenv("TEST_EVAL_API_KEY", key)
	const token = "example-eval-bearer"
	t.Setenv("TEST_EVAL_TOKEN", token)
	var authenticatedReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := strings.Split(r.URL.Path, "/")[1]
		wantBearer := ""
		if base == "primary" {
			wantBearer = "Bearer " + token
		}
		if r.Header.Get("Authorization") != wantBearer {
			t.Error("bearer was not scoped to the configured API")
		}
		if value := r.Header.Get("Api-Key"); value != "" {
			if value != key || r.URL.Path != "/primary/runs/814" {
				t.Error("credential sent outside the configured operation")
			}
			authenticatedReads.Add(1)
		}
		http.StripPrefix("/"+base, data).ServeHTTP(w, r)
	}))
	defer server.Close()

	args := []string{"eval", "--spec", spec, "--suite", suite, "--case", "assertion",
		"--allow", "getRun,listJobAttempts,getAttemptLogs", "--token-env", "TEST_EVAL_TOKEN", "--header-env", "Api-Key=TEST_EVAL_API_KEY",
		"--base-url", server.URL + "/primary", "--state-url"}
	for _, stateBase := range []string{"/primary", "/other"} {
		var out bytes.Buffer
		err := run(context.Background(), append(args, "assertion="+server.URL+stateBase), &out, io.Discard)
		if (err == nil) != (stateBase == "/primary") || authenticatedReads.Load() != 1 || strings.Contains(out.String(), key) {
			t.Fatalf("unexpected evaluation for %s: %v, credentialed reads %d", stateBase, err, authenticatedReads.Load())
		}
	}
	// A public operation still executes at an alternate base without sending
	// the token there. The header-required getRun above was excluded entirely.
	publicSpec, _ := examplePaths()
	args = []string{"eval", "--spec", publicSpec, "--suite", suite, "--case", "assertion", "--token-env", "TEST_EVAL_TOKEN",
		"--base-url", server.URL + "/primary", "--state-url", "assertion=" + server.URL + "/other"}
	if err := run(context.Background(), args, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
}

// External runners must receive only public tasks, and a failing grade must
// produce a machine-readable result as well as a failing process outcome.
func TestOwnerEvaluationBoundary(t *testing.T) {
	dir := t.TempDir()
	suite := filepath.Join(dir, "suite.yaml")
	input := filepath.Join(dir, "receipt.json")
	if err := os.WriteFile(suite, []byte(`version: 2
cases:
  - id: task
    prompt: Determine the category.
    state: owner-only-fixture
    answer_fields:
      - {name: category, type: string, description: The observed category.}
    facts:
      - {field: category, value: owner-only-expected}
    evidence:
      - source: {operation: getRecord, args: {path: {id: record-1}}}
        id: line-1
        text: owner-only-quote
`), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"tasks", "--suite", suite}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "owner-only-") || !strings.Contains(out.String(), "Determine the category.") {
		t.Fatalf("task export leaked private grading data or lost the public task: %s", out.String())
	}
	for _, category := range []string{"owner-only-expected", "wrong"} {
		receipt := map[string]any{
			"answer": map[string]any{
				"facts": map[string]any{"category": category},
				"citations": []any{map[string]any{
					"source": map[string]any{"operation": "getRecord", "args": map[string]any{"path": map[string]any{"id": "record-1"}}},
					"id":     "line-1", "text": "owner-only-quote",
				}},
			},
			"exchanges": []any{map[string]any{
				"operation": "getRecord", "args": map[string]any{"path": map[string]any{"id": "record-1"}},
				"status": 200, "body": map[string]any{"id": "line-1", "text": "owner-only-quote"},
			}},
		}
		raw, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(input, raw, 0600); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		err = run(context.Background(), []string{"grade", "--suite", suite, "--case", "task", "--input", input}, &out, io.Discard)
		want := category == "owner-only-expected"
		var result eval.Result
		if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil {
			t.Fatalf("grade returned no report: %v; %s; run error: %v", decodeErr, out.String(), err)
		}
		if result.Passed != want || (err == nil) != want {
			t.Fatalf("grade result and exit disagree: passed=%t error=%v want=%t", result.Passed, err, want)
		}
	}
}

// The test binary runs the actual entrypoint on stdin/stdout in a subprocess.
// This catches startup messages accidentally written to the MCP wire.
func TestMCPSubprocess(t *testing.T) {
	if os.Getenv("SKEINER_STDIO_TEST") == "1" {
		spec, _ := examplePaths()
		if err := run(context.Background(), []string{"serve", "--spec", spec}, os.Stdout, os.Stderr); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPSubprocess$")
	cmd.Env = append(os.Environ(), "SKEINER_STDIO_TEST=1")
	cs, err := mcp.NewClient(transport.Implementation, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	tools, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil || len(tools.Tools) != 2 {
		t.Fatalf("list tools: %v %v", tools, err)
	}
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "search", Arguments: map[string]string{"query": "failed job logs"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) == 0 || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "getAttemptLogs") {
		t.Fatalf("discovery: %+v", result)
	}
	result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "execute", Arguments: map[string]string{"code": "return 2 + 3;"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.Content[0].(*mcp.TextContent).Text != "5" {
		t.Fatalf("execute: %+v", result)
	}
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"code": "return typeof api.input;"}, `"undefined"`},
		{map[string]any{"code": "return api.input;", "input": nil}, "null"},
		{map[string]any{"code": "return api.input[1];", "mode": "async", "input": []any{false, "saved"}}, `"saved"`},
	} {
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "execute", Arguments: tc.args})
		if err != nil || result.IsError || result.Content[0].(*mcp.TextContent).Text != tc.want {
			t.Fatalf("input via MCP: %+v, error %v; want %s", result, err, tc.want)
		}
	}
	result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "execute", Arguments: map[string]string{
		"mode": "async",
		"code": `return api.call("unavailableOperation").catch(() => "recovered");`,
	}})
	if err != nil || result.IsError || result.Content[0].(*mcp.TextContent).Text != `"recovered"` {
		t.Fatalf("promise rejection via MCP: %+v, %v", result, err)
	}
	for _, args := range []map[string]any{
		{"mode": "async"},
		{"code": "return 1", "mode": "private-value"},
		{"code": "return 1", "mode": nil},
		{"code": "return 1", "private-value": "unexpected"},
	} {
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "execute", Arguments: args})
		if err != nil || !result.IsError {
			t.Fatalf("expected a tool argument error, got %+v, %v", result, err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		var problem struct{ Code string }
		if json.Unmarshal([]byte(text), &problem) != nil || problem.Code != "invalid_arguments" || strings.Contains(text, "private-value") {
			t.Fatalf("expected a sanitized invalid_arguments error, got %s", text)
		}
	}
}
