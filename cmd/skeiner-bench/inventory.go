package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	pageSize  = 9
	pageCount = 6
)

type review struct {
	ID   string `json:"id"`
	Head string `json:"head"`
}

type rule struct {
	Name   string `json:"name"`
	Issuer string `json:"issuer"`
}

type checkRun struct {
	rule
	ID     string `json:"id"`
	Head   string `json:"head"`
	Status string `json:"status"`
}

type pullRequest struct {
	Number         int        `json:"number"`
	Head           string     `json:"head"`
	Review         review     `json:"review"`
	RequiredChecks []rule     `json:"requiredChecks"`
	Checks         []checkRun `json:"checks"`
}

type inventoryPage struct {
	Items      []pullRequest `json:"items"`
	NextCursor *string       `json:"nextCursor"`
}

type evidence struct {
	PR     int    `json:"pr"`
	Review string `json:"review"`
	Run    string `json:"run"`
}

// Include plausible false positives for each part of the selection rule.
func makePullRequest(n int, updated bool) pullRequest {
	head := fmt.Sprintf("%040x", n)
	formal := rule{Name: "test", Issuer: "build-service"}
	check := checkRun{rule: formal, ID: fmt.Sprintf("run-%d", n), Head: head, Status: "failed"}
	review := review{ID: fmt.Sprintf("review-%d", n), Head: head}
	oldHead := fmt.Sprintf("%040x", n+1000)
	switch n % 6 {
	case 0:
		check.Status = "passed"
	case 2:
		review.Head = oldHead
	case 3:
		check.Head = oldHead
	case 4:
		check.Issuer = "lint-service"
	case 5:
		check.Name = "lint"
	}
	if n == 1 {
		review.Head = oldHead
	}
	if updated {
		switch n {
		case 2:
			review.Head = head
		case 7:
			check.Status = "passed"
		case 37:
			head = oldHead
		case 54:
			check.Status = "failed"
			check.ID = "rerun-54"
		}
	}
	return pullRequest{Number: n, Head: head, Review: review, RequiredChecks: []rule{formal}, Checks: []checkRun{check}}
}

// Keep the expected answer independent of the filtering code in both clients.
func expectedEvidence(updated bool) []evidence {
	numbers := []int{7, 13, 19, 25, 31, 37, 43, 49}
	if updated {
		numbers = []int{2, 13, 19, 25, 31, 43, 49, 54}
	}
	var result []evidence
	for _, n := range numbers {
		id := fmt.Sprintf("run-%d", n)
		if n == 54 {
			id = "rerun-54"
		}
		result = append(result, evidence{n, fmt.Sprintf("review-%d", n), id})
	}
	return result
}

// Record cursors at the HTTP server, so a repeated read cannot hide behind a correct answer.
type inventoryFixture struct {
	pages [][]byte
	mu    sync.Mutex
	reads []int
}

func newInventoryFixture(updated bool) (*inventoryFixture, error) {
	fixture := &inventoryFixture{}
	for i := range pageCount {
		page := inventoryPage{}
		for n := i*pageSize + 1; n <= (i+1)*pageSize; n++ {
			page.Items = append(page.Items, makePullRequest(n, updated))
		}
		if i+1 < pageCount {
			cursor := strconv.Itoa(i + 1)
			page.NextCursor = &cursor
		}
		body, err := json.Marshal(page)
		if err != nil {
			return nil, err
		}
		fixture.pages = append(fixture.pages, body)
	}
	return fixture, nil
}

func (f *inventoryFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cursor := 0
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var err error
		cursor, err = strconv.Atoi(raw)
		if err != nil || cursor < 1 || cursor >= len(f.pages) {
			http.Error(w, "invalid cursor", http.StatusBadRequest)
			return
		}
	}
	if r.Method != http.MethodGet || r.URL.Path != "/pulls" {
		http.NotFound(w, r)
		return
	}
	f.mu.Lock()
	f.reads = append(f.reads, cursor)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Write(f.pages[cursor])
}

func (f *inventoryFixture) takeReads() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	visited := f.reads
	f.reads = nil
	return visited
}

func resultBody(res *mcp.CallToolResult) (json.RawMessage, error) {
	if res.IsError || len(res.Content) != 1 {
		return nil, fmt.Errorf("unexpected tool result: %v", res.Content)
	}
	content, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		return nil, fmt.Errorf("expected MCP text, got %T", res.Content[0])
	}
	return json.RawMessage(content.Text), nil
}

type measurement struct {
	calls int
	bytes int
}

func (m *measurement) execute(ctx context.Context, session *mcp.ClientSession, code string, output any) error {
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "execute", Arguments: map[string]string{"mode": "async", "code": code},
	})
	if err != nil {
		return err
	}
	raw, err := resultBody(res)
	if err != nil {
		return err
	}
	m.calls++
	m.bytes += len(raw)
	return json.Unmarshal(raw, output)
}

func readPages(ctx context.Context, s *mcp.ClientSession, m *measurement) ([]evidence, error) {
	var result []evidence
	code := `return await api.call("listPullRequests");`
	for range pageCount {
		var page inventoryPage
		if err := m.execute(ctx, s, code, &page); err != nil {
			return nil, err
		}
		for _, row := range page.Items {
			if row.Review.Head != row.Head {
				continue
			}
			for _, check := range row.Checks {
				if check.Head == row.Head && check.Status == "failed" && slices.Contains(row.RequiredChecks, check.rule) {
					result = append(result, evidence{row.Number, row.Review.ID, check.ID})
				}
			}
		}
		if page.NextCursor == nil {
			return result, nil
		}
		cursor, err := json.Marshal(*page.NextCursor)
		if err != nil {
			return nil, err
		}
		code = fmt.Sprintf(`return await api.call("listPullRequests", {query: {cursor: %s}});`, cursor)
	}
	return nil, fmt.Errorf("expected the inventory to end after %d pages", pageCount)
}

func compareSnapshot(ctx context.Context, binary, spec, program, label string, updated bool) error {
	fixture, err := newInventoryFixture(updated)
	if err != nil {
		return err
	}
	host := httptest.NewServer(fixture)
	defer host.Close()
	session, _, err := openSession(ctx, binary, spec, host.URL)
	if err != nil {
		return err
	}
	defer session.Close()
	for _, approach := range []string{"page at a time", "saved program"} {
		var metrics measurement
		var matches []evidence
		if approach == "page at a time" {
			matches, err = readPages(ctx, session, &metrics)
		} else {
			err = metrics.execute(ctx, session, program, &matches)
		}
		if err != nil {
			return fmt.Errorf("%s / %s: %w", label, approach, err)
		}
		if !slices.Equal(matches, expectedEvidence(updated)) {
			return fmt.Errorf("%s / %s selected unexpected evidence: %+v", label, approach, matches)
		}
		reads := fixture.takeReads()
		if !slices.Equal(reads, []int{0, 1, 2, 3, 4, 5}) {
			return fmt.Errorf("%s / %s visited unexpected pages: %v", label, approach, reads)
		}
		fmt.Printf("%-8s  %-15s  %7d  %7d  %10d  %12d\n", label, approach, len(matches), len(reads), metrics.calls, metrics.bytes)
	}
	return nil
}

func compareInventory(ctx context.Context, binary string) error {
	dir := filepath.Join("benchmarks", "inventory")
	spec := filepath.Join(dir, "openapi.yaml")
	program, err := os.ReadFile(filepath.Join(dir, "blocked.js"))
	if err != nil {
		return err
	}
	fmt.Printf("%-8s  %-15s  %7s  %7s  %10s  %12s\n", "snapshot", "approach", "matches", "HTTP", "execute", "reply bytes")
	if err := compareSnapshot(ctx, binary, spec, string(program), "original", false); err != nil {
		return err
	}
	return compareSnapshot(ctx, binary, spec, string(program), "updated", true)
}
