package fixture

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func request(t *testing.T, h http.Handler, path string, dest any) int {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if dest != nil && w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), dest); err != nil {
			t.Fatal(err)
		}
	}
	return w.Code
}

func TestPagesAndReversibleMissingChunk(t *testing.T) {
	for _, state := range []string{"assertion", "missing-assertion", "assertion"} {
		h, err := New(state)
		if err != nil {
			t.Fatal(err)
		}
		var lines []line
		var ids []string
		for cursor := ""; ; {
			path := "/attempts/att-814-unit-1/logs"
			if cursor != "" {
				path += "?cursor=" + url.QueryEscape(cursor)
			}
			var page struct {
				Lines      []line
				NextCursor *string
			}
			if got := request(t, h, path, &page); got != http.StatusOK {
				t.Fatalf("GET %s returned %d", path, got)
			}
			lines = append(lines, page.Lines...)
			for _, l := range page.Lines {
				ids = append(ids, l.ID)
			}
			if page.NextCursor == nil {
				break
			}
			if len(lines) > 6 {
				t.Fatal("pagination did not terminate")
			}
			cursor = *page.NextCursor
		}
		missing := state == "missing-assertion"
		count := 6
		if missing {
			count = 4
		}
		if len(lines) != count || lines[count-1].ID != "814-U1-L006" || lines[count-1].Text != "exit code 1" {
			t.Fatalf("%s: got log IDs %v", state, ids)
		}
		if missing && lines[2].Number != 5 {
			t.Errorf("regressed fixture renumbered logs: %v", ids)
		}
		if !missing && (lines[2].ID != "814-U1-L003" || lines[3].ID != "814-U1-L004") {
			t.Errorf("missing primary evidence: %v", ids)
		}
	}
}

func TestRetryAndStateIsolation(t *testing.T) {
	h, err := New("retry")
	if err != nil {
		t.Fatal(err)
	}
	var r run
	if request(t, h, "/runs/815", &r) != 200 || r.SHA != "def456" || r.Status != "passed" {
		t.Fatalf("retry run: %+v", r)
	}
	var list struct{ Attempts []attempt }
	if request(t, h, "/jobs/"+r.Jobs[0].ID+"/attempts", &list) != 200 || len(list.Attempts) != 2 ||
		list.Attempts[0].Status != "failed" || list.Attempts[1].Status != "passed" {
		t.Fatalf("unexpected attempts: %+v", list)
	}
	if got := request(t, h, "/runs/814", nil); got != 404 {
		t.Errorf("other fixture state exposed: status %d", got)
	}
	if got := request(t, h, "/attempts/att-815-integration-2/logs?cursor=att-815-integration-1@2", nil); got != 400 {
		t.Errorf("cross-attempt cursor accepted: status %d", got)
	}
}

func TestFollowupSHAAndReadOnly(t *testing.T) {
	h, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	var result struct{ Runs []run }
	if request(t, h, "/runs?sha=abc124", &result) != 200 || len(result.Runs) != 1 || result.Runs[0].Status != "passed" {
		t.Fatalf("updated commit never verified: %+v", result)
	}
	var tests struct{ Results []history }
	if request(t, h, "/test-history?test=TestInvoiceRounding", &tests) != 200 || len(tests.Results) != 2 {
		t.Fatalf("test history: %+v", tests)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/runs/814", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /runs/814 returned %d", w.Code)
	}
}
