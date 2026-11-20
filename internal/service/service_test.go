package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hasit/skeiner/internal/catalog"
	"github.com/hasit/skeiner/internal/dispatch"
	"github.com/hasit/skeiner/internal/sandbox"
)

func TestPaginatedReadsRequireApprovedGET(t *testing.T) {
	const spec = `openapi: 3.0.3
paths:
  /items:
    parameters: [{in: query, name: cursor, schema: {type: string}}]
    get:
      operationId: listItems
      responses: {'200': {content: {application/json: {schema: {type: object}}}}}
    post:
      operationId: createItem
      responses: {'200': {content: {application/json: {schema: {type: object}}}}}`
	cat, err := catalog.Parse([]byte(spec), []string{"listItems", "createItem"})
	if err != nil {
		t.Fatal(err)
	}
	var reads, writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			reads.Add(1)
		case http.MethodPost:
			writes.Add(1)
		default:
			t.Errorf("unexpected upstream method %s", r.Method)
		}
		io.WriteString(w, `{"items":[1,2],"next":null}`)
	}))
	defer server.Close()
	client, err := dispatch.New(cat, dispatch.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	engine, err := sandbox.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(ctx)
	var trace []Exchange
	s := New(cat, engine, client, func(x Exchange) { trace = append(trace, x) })
	const options = `{cursorParam:'cursor', initial:0, next:p=>p.next, reduce:(n,p)=>n+p.items.length}`
	_, err = s.Execute(ctx, `return api.reducePages('createItem',`+options+`)`)
	if err == nil || PublicError(err).Code != "denied" || writes.Load() != 0 {
		t.Fatalf("refused page read produced %d writes, error %v", writes.Load(), err)
	}
	raw, err := s.Execute(ctx, `await api.call('createItem'); return (await api.reducePages('listItems',`+options+`)).state`)
	if err != nil || string(raw) != "2" || reads.Load() != 1 || writes.Load() != 1 {
		t.Fatalf("got result %s, %d reads and %d writes, error %v", raw, reads.Load(), writes.Load(), err)
	}
	if len(trace) != 3 || trace[0].Operation != "createItem" || trace[0].Code != "denied" || trace[0].Status != 0 ||
		trace[1].Operation != "createItem" || trace[1].Status != http.StatusOK ||
		trace[2].Operation != "listItems" || trace[2].Status != http.StatusOK {
		t.Fatalf("unexpected dispatch observations %+v", trace)
	}
}
