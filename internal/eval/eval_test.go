package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testSession struct {
	observe  func(Exchange)
	body     string
	returned json.RawMessage
}

func (s *testSession) Close() {}
func (s *testSession) Tools(context.Context) ([]Tool, error) {
	return []Tool{{"search", "search catalog", json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)}, {"execute", "run code", json.RawMessage(`{"type":"object","properties":{"code":{"type":"string"}}}`)}}, nil
}
func (s *testSession) Call(_ context.Context, name string, _ map[string]any) (json.RawMessage, error) {
	if name == "search" {
		return json.RawMessage(`[{"operationId":"fetch"}]`), nil
	}
	s.observe(Exchange{Operation: "fetch", Status: 200, Body: json.RawMessage(s.body)})
	return s.returned, nil
}

type testDriver struct{ answer Answer }

func (testDriver) Name() string { return "test" }
func (d testDriver) Answer(ctx context.Context, p TaskInput, c Client) (Answer, error) {
	if p.Prompt != "What caused this?" {
		return Answer{}, fmt.Errorf("grader data leaked into prompt: %s", p.Prompt)
	}
	if _, err := c.Call(ctx, "execute", map[string]any{"code": "return api.call('fetch',{})"}); err != nil {
		return Answer{}, err
	}
	return d.answer, nil
}
func TestRubricRequiresCorroboratedCitations(t *testing.T) {
	suitePath := filepath.Join(t.TempDir(), "suite.yaml")
	s := `version: 1
cases:
  - id: evidence
    prompt: "What caused this?"
    facts:
      - label: cause
        pattern: "timed out"
    evidence:
      - id: line-2
        text: timed out
`
	if err := os.WriteFile(suitePath, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	a := Answer{Summary: "timed out", Citations: []Citation{{ID: "line-2", Text: "timed out"}}}
	for _, tt := range []struct {
		name, body string
		answer     Answer
		pass       bool
		diagnostic string
	}{
		{"pass", `{"records":[{"id":"line-2","text":"timed out"}]}`, a, true, ""},
		{"wrong association", `[{"id":"line-1","text":"timed out"},{"id":"line-2","text":"other"}]`, a, false, "not retrieved"},
		{"uncited", `{"id":"line-2","text":"timed out"}`, Answer{Summary: "timed out"}, false, "omitted or misquoted"},
		{"fabrication", `{"id":"line-2","text":"timed out"}`, Answer{Summary: "timed out", Citations: []Citation{{ID: "line-2", Text: "timed out"}, {ID: "line-9", Text: "Authorization: bearer SECRET"}}}, false, "no successful upstream"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Run(context.Background(), suitePath, Options{Driver: testDriver{tt.answer}, Connect: func(_ context.Context, _ string, observe func(Exchange)) (Session, error) {
				return &testSession{observe, tt.body, json.RawMessage("null")}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if r.Passed() != tt.pass || !strings.Contains(r.Results[0].Diagnosis, tt.diagnostic) {
				t.Fatalf("unexpected report: %+v", r)
			}
			raw, _ := json.Marshal(r)
			if strings.Contains(string(raw), "SECRET") {
				t.Fatal("trace exposed header")
			}
		})
	}
}
func TestStructuredAnswerRequiresCitationsArray(t *testing.T) {
	task := TaskInput{Version: 2}
	for _, raw := range []string{`{"facts":{"matches":0}}`, `{"facts":{"matches":0},"citations":null}`} {
		if _, err := ParseAnswer([]byte(raw), task); err == nil {
			t.Fatalf("accepted missing citations array: %s", raw)
		}
	}
	answer, err := ParseAnswer([]byte(`{"facts":{"matches":0},"citations":[]}`), task)
	if err != nil || answer.Citations == nil {
		t.Fatalf("expected an explicit empty array: %+v, %v", answer, err)
	}
}

func TestOpenAIConversation(t *testing.T) {
	stage := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []message `json:"messages"`
			Tools    []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if r.Header.Get("Authorization") != "Bearer mock" || json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("bad provider request")
			http.Error(w, "bad", 400)
			return
		}
		if len(req.Tools) != 2 || req.Messages[1].Content != "Why did this fail?" {
			t.Error("unexpected tools or user prompt")
		}
		stage++
		var msg message
		switch stage {
		case 1:
			msg = message{Role: "assistant", ToolCalls: []toolCall{{"one", "function", function{"search", `{"query":"logs"}`}}}}
		case 2:
			last := req.Messages[len(req.Messages)-1]
			if last.ToolCallID != "one" || !strings.Contains(last.Content, "fetch") {
				t.Error("missing search result")
			}
			msg = message{Role: "assistant", ToolCalls: []toolCall{{"two", "function", function{"execute", `{"code":"return api.call('fetch',{})"}`}}}}
		case 3:
			last := req.Messages[len(req.Messages)-1]
			if last.ToolCallID != "two" || !strings.Contains(last.Content, "timed out") {
				t.Error("missing execute result")
			}
			msg = message{Role: "assistant", Content: `{"summary":"timed out","citations":[{"id":"L2","text":"timed out"}]}`}
		default:
			t.Error("unexpected provider round trip")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg}}})
	}))
	defer api.Close()
	d := modelDriver{model: "test", key: "mock", url: api.URL, http: api.Client()}
	a, err := d.Answer(context.Background(), TaskInput{Prompt: "Why did this fail?", Version: 1}, &testSession{func(Exchange) {}, `[]`, json.RawMessage(`[{"id":"L2","text":"timed out"}]`)})
	if err != nil || a.Summary != "timed out" || stage != 3 {
		t.Fatalf("answer=%+v, turns=%d, err=%v", a, stage, err)
	}
}

// The original regex rubric accepted a wrong OOM diagnosis that merely named
// the real test. These controls exercise the authoritative structured answer
// and prevent a quote from another job from corroborating this job's cause.
func TestStructuredFactsAndScopedEvidence(t *testing.T) {
	suitePath := filepath.Join(t.TempDir(), "suite.yaml")
	const spec = `version: 2
cases:
  - id: failure
    prompt: "Why did run 814 fail?"
    citation_fields: {id: sequence, text: text}
    answer_fields:
      - {name: run_id, type: string, description: "Requested run identifier"}
      - {name: failure_category, type: string, description: "test_assertion, infrastructure, or unknown"}
    facts:
      - {field: run_id, value: "814"}
      - {field: failure_category, value: test_assertion}
    evidence:
      - id: "000000000002"
        text: "FAIL TestInvoiceRounding: expected 125 cents, got 124 cents"
        source:
          operation: getJobLogs
          args: {path: {jobId: job-814-unit}}
`
	if err := os.WriteFile(suitePath, []byte(spec), 0600); err != nil {
		t.Fatal(err)
	}
	tasks, err := LoadTaskInputs(suitePath, "failure")
	if err != nil {
		t.Fatal(err)
	}
	input := tasks["failure"]
	public, _ := json.Marshal(input)
	if strings.Contains(string(public), "expected 125") || strings.Contains(string(public), "getJobLogs") || strings.Contains(string(public), "job-814-unit") || input.CitationFields.ID != "sequence" {
		t.Fatalf("private rubric or missing public citation mapping: %s", public)
	}
	const answerJSON = `{"facts":{"run_id":"814","failure_category":"test_assertion"},"citations":[{"id":"000000000002","text":"FAIL TestInvoiceRounding: expected 125 cents, got 124 cents","source":{"operation":"getJobLogs","args":{"path":{"jobId":"job-814-unit"},"query":{"after":"000000000001"}}}}]}`
	for _, tc := range []struct {
		name, cause, summary, job          string
		missing, forged, emptyGroups, pass bool
	}{
		{name: "correct facts and source, arbitrary pagination", pass: true},
		{name: "HTTP receipt omits empty query group", emptyGroups: true, pass: true},
		{name: "wrong cause with correctly fetched quote", cause: "out_of_memory"},
		{name: "missing structured facts", missing: true},
		{name: "contradictory prose cannot ride beside correct facts", summary: "The failure was not TestInvoiceRounding; it was an out-of-memory kill."},
		{name: "same sequence and text from another job", job: "job-other"},
		{name: "forged source for another job's quote", job: "job-other", forged: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer, err := ParseAnswer([]byte(answerJSON), input)
			if err != nil {
				t.Fatal(err)
			}
			if tc.cause != "" {
				answer.Facts["failure_category"], _ = json.Marshal(tc.cause)
			}
			if tc.missing {
				answer.Facts = nil
			}
			answer.Summary = tc.summary
			job := "job-814-unit"
			if tc.job != "" {
				job = tc.job
			}
			// Property order intentionally differs from the agent's citation.
			args := json.RawMessage(fmt.Sprintf(`{"query":{"after":"000000000001"},"path":{"jobId":%q}}`, job))
			if tc.emptyGroups {
				args = json.RawMessage(`{"path":{"jobId":"job-814-unit"}}`)
				answer.Citations[0].Source.Args = json.RawMessage(`{"path":{"jobId":"job-814-unit"},"query":{}}`)
			}
			if tc.job != "" && !tc.forged {
				answer.Citations[0].Source.Args = args
			}
			receipt := Exchange{Operation: "getJobLogs", Args: args, Status: 200, Body: json.RawMessage(`[{"sequence":"000000000002","text":"FAIL TestInvoiceRounding: expected 125 cents, got 124 cents"}]`)}
			result, err := GradeAnswer(suitePath, "failure", answer, []Exchange{receipt})
			if err != nil || result.Passed != tc.pass {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
	// Unknown prose fields must not be silently discarded during JSON decoding.
	unknown := strings.TrimSuffix(answerJSON, "}") + `,"diagnosis":"out of memory"}`
	if _, err := ParseAnswer([]byte(unknown), input); err == nil {
		t.Fatal("accepted ungraded diagnosis field")
	}
}
