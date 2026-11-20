// Package fixture serves a synthetic read-only CI API for the skeiner example.
package fixture

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

type job struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // Latest attempt.
}

type run struct {
	ID     string `json:"id"`
	SHA    string `json:"sha"`
	Status string `json:"status"`
	Jobs   []job  `json:"jobs"`
}

type attempt struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	Status string `json:"status"`
}

type line struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	Text   string `json:"text"`
}

type history struct {
	RunID     string `json:"runId"`
	SHA       string `json:"sha"`
	AttemptID string `json:"attemptId"`
	Status    string `json:"status"`
}

type snapshot struct {
	runs     []run
	attempts map[string][]attempt
	logs     map[string][]line
	tests    map[string][]history
}

// New returns an independent fixture snapshot. The selected state controls which
// runs are visible. missing-assertion reproduces a log retention regression.
func New(state string) (http.Handler, error) {
	s := snapshot{attempts: map[string][]attempt{}, logs: map[string][]line{}, tests: map[string][]history{}}
	switch state {
	case "", "all":
		s.addAssertion(false)
		s.addRetry()
		s.addInfrastructure()
	case "assertion", "missing-assertion":
		s.addAssertion(state == "missing-assertion")
	case "retry":
		s.addRetry()
	case "infrastructure":
		s.addInfrastructure()
	default:
		return nil, fmt.Errorf("unknown CI fixture state %q", state)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /runs", func(w http.ResponseWriter, r *http.Request) {
		sha := r.URL.Query().Get("sha")
		if sha == "" {
			problem(w, http.StatusBadRequest, "sha is required")
			return
		}
		matches := []run{}
		for _, run := range s.runs {
			if run.SHA == sha {
				matches = append(matches, run)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"runs": matches})
	})
	mux.HandleFunc("GET /runs/{runId}", func(w http.ResponseWriter, r *http.Request) {
		for _, run := range s.runs {
			if run.ID == r.PathValue("runId") {
				writeJSON(w, http.StatusOK, run)
				return
			}
		}
		problem(w, http.StatusNotFound, "run not found")
	})
	mux.HandleFunc("GET /jobs/{jobId}/attempts", func(w http.ResponseWriter, r *http.Request) {
		attempts, ok := s.attempts[r.PathValue("jobId")]
		if !ok {
			problem(w, http.StatusNotFound, "job not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"attempts": attempts})
	})
	mux.HandleFunc("GET /attempts/{attemptId}/logs", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("attemptId")
		logs, ok := s.logs[id]
		if !ok {
			problem(w, http.StatusNotFound, "attempt not found")
			return
		}
		start := 0
		if cursor, present := r.URL.Query()["cursor"]; present {
			prefix := id + "@"
			if len(cursor) != 1 || !strings.HasPrefix(cursor[0], prefix) {
				problem(w, http.StatusBadRequest, "invalid cursor for this attempt")
				return
			}
			value, err := strconv.Atoi(strings.TrimPrefix(cursor[0], prefix))
			if err != nil || value <= 0 || value >= len(logs) || value%2 != 0 {
				problem(w, http.StatusBadRequest, "invalid log cursor")
				return
			}
			start = value
		}
		end := min(start+2, len(logs))
		var cursor *string
		if end < len(logs) {
			next := id + "@" + strconv.Itoa(end)
			cursor = &next
		}
		writeJSON(w, http.StatusOK, map[string]any{"attemptId": id, "lines": logs[start:end], "nextCursor": cursor})
	})
	mux.HandleFunc("GET /test-history", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("test")
		if name == "" {
			problem(w, http.StatusBadRequest, "test is required")
			return
		}
		results := s.tests[name]
		if results == nil {
			results = []history{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"test": name, "results": results})
	})
	return mux, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func problem(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func numbered(prefix string, messages ...string) []line {
	lines := make([]line, len(messages))
	for i, text := range messages {
		lines[i] = line{fmt.Sprintf("%s-L%03d", prefix, i+1), i + 1, text}
	}
	return lines
}

func (s *snapshot) addAssertion(missing bool) {
	s.runs = append(s.runs,
		run{"814", "abc123", "failed", []job{
			{"job-814-unit", "unit-linux", "failed"},
			{"job-814-lint", "lint", "passed"},
		}},
		run{"817", "abc124", "passed", []job{{"job-817-unit", "unit-linux", "passed"}}},
	)
	s.attempts["job-814-unit"] = []attempt{{"att-814-unit-1", 1, "failed"}}
	s.attempts["job-814-lint"] = []attempt{{"att-814-lint-1", 1, "passed"}}
	s.attempts["job-817-unit"] = []attempt{{"att-817-unit-1", 1, "passed"}}
	unit := numbered("814-U1",
		"checkout abc123",
		"running package billing",
		"FAIL TestInvoiceRounding",
		"invoice_test.go:71: expected 125 cents, got 124 cents",
		"uploading test report",
		"exit code 1",
	)
	if missing {
		// Simulate a collector dropping a chunk. Preserve the remaining IDs
		// so the trace shows precisely which evidence disappeared.
		unit = append(unit[:2], unit[4:]...)
	}
	s.logs["att-814-unit-1"] = unit
	s.logs["att-814-lint-1"] = numbered("814-L1", "starting lint", "no findings")
	s.logs["att-817-unit-1"] = numbered("817-U1", "checkout abc124", "PASS TestInvoiceRounding", "exit code 0")
	s.tests["TestInvoiceRounding"] = []history{
		{"814", "abc123", "att-814-unit-1", "failed"},
		{"817", "abc124", "att-817-unit-1", "passed"},
	}
}

func (s *snapshot) addRetry() {
	s.runs = append(s.runs, run{"815", "def456", "passed", []job{{"job-815-integration", "integration-linux", "passed"}}})
	s.attempts["job-815-integration"] = []attempt{{"att-815-integration-1", 1, "failed"}, {"att-815-integration-2", 2, "passed"}}
	s.logs["att-815-integration-1"] = numbered("815-I1", "checkout def456", "registry pull failed: 503 Service Unavailable", "exit code 1")
	s.logs["att-815-integration-2"] = numbered("815-I2", "checkout def456", "PASS integration suite", "exit code 0")
}

func (s *snapshot) addInfrastructure() {
	s.runs = append(s.runs, run{"816", "fed987", "failed", []job{{"job-816-unit", "unit-linux", "failed"}}})
	s.attempts["job-816-unit"] = []attempt{{"att-816-unit-1", 1, "failed"}}
	s.logs["att-816-unit-1"] = numbered("816-U1",
		"requesting linux runner",
		"runner provisioning failed: no compatible executor available",
		"test process never started",
		"exit code 1",
	)
}
