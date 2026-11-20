package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestCandidate(t *testing.T) {
	start := time.Now()
	e, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("interpreter compile and WASI initialization: %s", time.Since(start))
	t.Cleanup(func() { e.Close(context.Background()) })

	t.Run("dependent calls and fresh globals", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/run", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"job":"build"}`) })
		mux.HandleFunc("/build", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"attempt":"a1"}`) })
		mux.HandleFunc("/a1", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"lines":["ok","error"]}`) })
		server := httptest.NewServer(mux)
		defer server.Close()
		calls := 0
		fetch := func(ctx context.Context, op string, args json.RawMessage) (json.RawMessage, error) {
			if op != "get" {
				return nil, errors.New("unexpected operation")
			}
			var arg struct{ ID string }
			if err := json.Unmarshal(args, &arg); err != nil {
				return nil, err
			}
			if arg.ID != "run" && arg.ID != "build" && arg.ID != "a1" {
				return nil, errors.New("unexpected identifier")
			}
			calls++
			req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/"+arg.ID, nil)
			if err != nil {
				return nil, err
			}
			resp, err := server.Client().Do(req)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			return io.ReadAll(resp.Body)
		}
		result, err := e.Run(context.Background(), `
			globalThis.secret = "first execution";
			const job = api.call("get", {id:"run"}).job;
			const attempt = api.call("get", {id:job}).attempt;
			return api.call("get", {id:attempt}).lines.filter(line => line === "error");
		`, fetch)
		if err != nil || string(result) != `["error"]` || calls != 3 {
			t.Fatalf("calls=%d result=%s error=%v", calls, result, err)
		}
		result, err = e.Run(context.Background(), `return typeof globalThis.secret;`, nil)
		if err != nil || string(result) != `"undefined"` {
			t.Fatalf("fresh state: %s %v", result, err)
		}
	})

	t.Run("record identity across dependent calls", func(t *testing.T) {
		const join = `const record = api.call("reference").items[0]; return api.call("lookup", {path:{id:record.id}});`
		for _, tc := range []struct {
			name, id string
			safe     bool
		}{
			{"safe integer limit", "9007199254740991", true},
			{"exact string identifier", `"9007199254740993"`, true},
			{"rounded integer identifier", "9007199254740993", false},
			{"negative scientific integer", "-9.007199254740993e15", false},
			{"numeric overflow", "1e400", false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var lookups int
				result, err := e.Run(context.Background(), join, func(_ context.Context, op string, args json.RawMessage) (json.RawMessage, error) {
					switch op {
					case "reference":
						return json.RawMessage(`{"items":[{"id":` + tc.id + `,"rate":1.0824}]}`), nil
					case "lookup":
						lookups++
						return args, nil
					default:
						return nil, errors.New("unexpected operation")
					}
				})
				if tc.safe {
					if err != nil || lookups != 1 || string(result) != `{"path":{"id":`+tc.id+`}}` {
						t.Fatalf("expected original ID in one lookup, got %s (%d lookups, %v)", result, lookups, err)
					}
					return
				}
				var failure *Error
				if !errors.As(err, &failure) || failure.Code != "unsafe_integer" || lookups != 0 {
					t.Fatalf("expected unsafe_integer before lookup, got %s (%d lookups, %v)", result, lookups, err)
				}
			})
		}
	})

	t.Run("awaited reads and rejected host calls", func(t *testing.T) {
		denied := errors.New("denied")
		var operations []string
		dispatch := func(_ context.Context, op string, args json.RawMessage) (json.RawMessage, error) {
			operations = append(operations, op)
			switch op {
			case "read":
				return json.RawMessage(`{"id":"verified"}`), nil
			case "denied":
				return nil, denied
			default:
				return args, nil
			}
		}
		result, err := e.Run(context.Background(), `
			const record = await(api.call("read"));
			const fallback = await api.call("denied").catch(e => e.message);
			return await api.call("lookup", {id:record.id, fallback});
		`, dispatch)
		if err != nil || string(result) != `{"id":"verified","fallback":"denied"}` || strings.Join(operations, ",") != "read,denied,lookup" {
			t.Fatalf("got %s, calls %v, error %v", result, operations, err)
		}
		operations = nil
		_, err = e.Run(context.Background(), `
			const rejected = api.call("denied");
			await api.call("read");
			return rejected;
		`, dispatch)
		if !errors.Is(err, denied) || strings.Join(operations, ",") != "denied,read" {
			t.Fatalf("lost original host error: calls %v, error %v", operations, err)
		}
	})

	t.Run("ordinary results finish before detached jobs", func(t *testing.T) {
		result, err := e.Run(context.Background(), `
			const row = {stage:"await"};
			Promise.resolve().then(() => { row.stage = "late"; });
			return row;
		`, nil)
		if err != nil || string(result) != `{"stage":"await"}` {
			t.Fatalf("synchronous return changed: %s %v", result, err)
		}
	})

	t.Run("choose the JavaScript grammar before dispatch", func(t *testing.T) {
		for _, tc := range []struct {
			mode       Mode
			code, want string
		}{
			{ModeAsync, `return await(Promise.resolve(7)) + 1;`, "8"},
			{ModeAuto, `function format(await) { return await.toUpperCase(); } return format(api.call("read").id);`, `"VERIFIED"`},
			{ModeAuto, `return (async () => (await api.call("read")).id)();`, `"verified"`},
		} {
			result, err := e.RunMode(context.Background(), tc.code, tc.mode, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"id":"verified"}`), nil
			})
			if err != nil || string(result) != tc.want {
				t.Fatalf("%s: got %s, error %v", tc.code, result, err)
			}
		}
		calls := 0
		_, err := e.Run(context.Background(), `api.call("write"); throw new SyntaxError("await");`, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
			calls++
			return json.RawMessage("null"), nil
		})
		var failure *Error
		if !errors.As(err, &failure) || failure.Code != "syntax_error" || calls != 1 {
			t.Fatalf("body must run exactly once: calls %d, error %v", calls, err)
		}
	})

	t.Run("unsafe integers authored by the guest", func(t *testing.T) {
		for _, code := range []string{
			`return api.call("lookup", {path:{id:9007199254740993}});`,
			`return {id:9007199254740993};`,
		} {
			_, err := e.Run(context.Background(), code, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
				t.Error("unsafe argument reached the dispatcher")
				return json.RawMessage("null"), nil
			})
			var failure *Error
			if !errors.As(err, &failure) || failure.Code != "unsafe_integer" {
				t.Fatalf("expected unsafe_integer, got %v", err)
			}
		}
	})

	t.Run("guest capabilities and late callbacks", func(t *testing.T) {
		result, err := e.Run(context.Background(), `return [typeof std, typeof os, typeof fetch, typeof process, typeof require];`, nil)
		if err != nil || string(result) != `["undefined","undefined","undefined","undefined","undefined"]` {
			t.Fatalf("unexpected globals: %s %v", result, err)
		}
		// A dynamic import can recover the QuickJS standard library inside WASI.
		// Even its raw fd_write must obey the same Go policy and terminal state.
		calls := 0
		_, err = e.Run(context.Background(), `
			import("qjs:std").then(s => {
				s.out.puts(JSON.stringify({kind:"call",operation:"late",args:{}}) + "\n");
				s.out.flush();
			});
			return true;
		`, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
			calls++
			return json.RawMessage("null"), nil
		})
		if calls != 0 {
			t.Fatalf("callback ran after return: %v", err)
		}
		// A failed execution must not affect the next request's bridge.
		result, err = e.Run(context.Background(), `return true;`, nil)
		if err != nil || string(result) != "true" {
			t.Fatalf("next execution: %s %v", result, err)
		}
	})

	t.Run("runaway", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := e.Run(ctx, `while (true) {}`, nil)
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
			t.Fatalf("deadline: %v (%s)", err, time.Since(start))
		}
		t.Logf("200 ms deadline interrupted guest after %s", time.Since(start))

		// A completed read must re-arm the guest watchdog even with a later caller deadline.
		outer, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		read := false
		_, err = e.Run(outer, `await api.call("read"); while (true) {}`, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
			read = true
			return json.RawMessage("null"), nil
		})
		if !read || !errors.Is(err, context.DeadlineExceeded) || outer.Err() != nil {
			t.Fatalf("guest watchdog: read %t, error %v, outer %v", read, err, outer.Err())
		}
	})
	t.Run("host wait and overall deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			// This review succeeded upstream after the old five-second guest deadline.
			const observedLatency = 5774 * time.Millisecond
			var stalled bool
			fetch := func(ctx context.Context, op string, _ json.RawMessage) (json.RawMessage, error) {
				switch op {
				case "review":
					select {
					case <-time.After(observedLatency):
						return json.RawMessage(`{"status":"completed"}`), nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				case "stall":
					stalled = true
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return nil, errors.New("unexpected operation")
			}
			result, err := e.Run(context.Background(), `return (await api.call("review")).status;`, fetch)
			if err != nil || string(result) != `"completed"` {
				t.Fatalf("slow review: result %s, error %v", result, err)
			}
			// Use an isolated admission queue on the shared, compiled interpreter.
			busy := &Engine{runtime: e.runtime, compiled: e.compiled, slots: make(chan struct{}, MaxConcurrent)}
			for range MaxConcurrent {
				busy.slots <- struct{}{}
			}
			start := time.Now()
			_, err = busy.Run(context.Background(), `return await api.call("stall");`, fetch)
			if !errors.Is(err, context.DeadlineExceeded) || stalled || time.Since(start) != 5*time.Second {
				t.Fatalf("admission deadline: stalled %t, elapsed %s, error %v", stalled, time.Since(start), err)
			}
			go func() {
				time.Sleep(4 * time.Second)
				<-busy.slots
			}()
			start = time.Now()
			_, err = busy.Run(context.Background(), `await api.call("review"); return await api.call("stall");`, fetch)
			if !errors.Is(err, context.DeadlineExceeded) || !stalled || time.Since(start) != 30*time.Second {
				t.Fatalf("overall deadline: stalled %t, elapsed %s, error %v", stalled, time.Since(start), err)
			}
		})
	})
	t.Run("guest memory", func(t *testing.T) {
		_, err := e.Run(context.Background(), `return new Uint8Array(64 * 1024 * 1024).length;`, nil)
		var failure *Error
		if !errors.As(err, &failure) {
			t.Fatalf("expected allocation failure under %d bytes: %v", MemoryBytes, err)
		}
		t.Logf("64 MiB allocation under %d MiB cap: %s", MemoryBytes>>20, failure.Code)
		result, err := e.Run(context.Background(), `return 42;`, nil)
		if err != nil || string(result) != "42" {
			t.Fatalf("next execution after allocation failure: %s %v", result, err)
		}
	})
	t.Run("cancel in-flight HTTP", func(t *testing.T) {
		started, canceled := make(chan struct{}), make(chan struct{})
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			close(started)
			select {
			case <-req.Context().Done():
				close(canceled)
			case <-time.After(4 * time.Second):
			}
		}))
		defer s.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
		defer cancel()
		_, err := e.Run(ctx, `return await api.call("wait", {});`, func(ctx context.Context, op string, args json.RawMessage) (json.RawMessage, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
			if err != nil {
				return nil, err
			}
			resp, err := s.Client().Do(req)
			if err == nil {
				resp.Body.Close()
			}
			return nil, err
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("guest error: %v", err)
		}
		select {
		case <-started:
		default:
			t.Fatal("upstream was never reached")
		}
		select {
		case <-canceled:
		case <-time.After(time.Second):
			t.Fatal("upstream request not cancelled")
		}
	})
	t.Run("host errors and limits", func(t *testing.T) {
		denied := errors.New("operation denied")
		dispatch := func(context.Context, string, json.RawMessage) (json.RawMessage, error) { return nil, denied }
		_, err := e.Run(context.Background(), `return api.call("denied", {});`, dispatch)
		if !errors.Is(err, denied) {
			t.Fatalf("error identity lost: %v", err)
		}
		result, err := e.Run(context.Background(), `try { api.call("denied", {}); } catch (e) { return [e.message, api.remainingCalls]; }`, dispatch)
		if err != nil || string(result) != fmt.Sprintf(`["operation denied",%d]`, MaxCalls-1) {
			t.Fatalf("caught error: %s %v", result, err)
		}
		for _, tc := range []struct{ code, want string }{
			{`return "x".repeat(20000)`, "output_limit"},
			{`return api.call(1, [])`, "invalid_arguments"},
			{`throw new Error("Authorization: Bearer example")`, "javascript_error"},
			{`return {nested: Promise.resolve(1)}`, "invalid_result"},
			{`return new Promise(() => {})`, "invalid_result"},
			{`return ;)`, "syntax_error"},
			{`for(let i=0;i<17;i++){try {api.call("denied",{})} catch (_) {}} return true`, "call_limit"},
		} {
			_, err := e.Run(context.Background(), tc.code, dispatch)
			var f *Error
			if !errors.As(err, &f) || f.Code != tc.want || strings.Contains(err.Error(), "Bearer") {
				t.Errorf("expected %s; got %v", tc.want, err)
			}
		}
	})

	t.Run("resume at the next unread page", func(t *testing.T) {
		type batch struct {
			Complete   bool     `json:"complete"`
			Evidence   []string `json:"evidence"`
			NextCursor *string  `json:"nextCursor"`
		}
		var reads int
		fetch := func(_ context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
			var args struct{ Cursor *string }
			if op != "page" || json.Unmarshal(raw, &args) != nil {
				return nil, errors.New("invalid request")
			}
			if reads == 0 && args.Cursor != nil || reads != 0 && (args.Cursor == nil || *args.Cursor != fmt.Sprintf("page:%d", reads+1)) {
				return nil, errors.New("skipped or repeated a page")
			}
			reads++
			var next *string
			if reads < MaxCalls+1 {
				cursor := fmt.Sprintf("page:%d", reads+1)
				next = &cursor
			}
			return json.Marshal(map[string]any{"id": fmt.Sprintf("record:%d", reads), "nextCursor": next})
		}
		runBatch := func(cursor *string) batch {
			t.Helper()
			initial, err := json.Marshal(cursor)
			if err != nil {
				t.Fatal(err)
			}
			body := `let cursor = ` + string(initial) + `;
			const evidence = [];
			while (api.remainingCalls > 0) {
				const page = await api.call("page", {cursor});
				evidence.push(page.id);
				cursor = page.nextCursor;
				if (cursor === null) break;
			}
			return {complete: cursor === null, evidence, nextCursor: cursor};`
			raw, err := e.RunMode(context.Background(), body, ModeAsync, fetch)
			var result batch
			if err != nil || json.Unmarshal(raw, &result) != nil {
				t.Fatalf("batch: %s, %v", raw, err)
			}
			return result
		}
		first := runBatch(nil)
		if first.Complete || first.NextCursor == nil || len(first.Evidence) != MaxCalls || reads != MaxCalls {
			t.Fatalf("first batch: %+v (%d reads)", first, reads)
		}
		second := runBatch(first.NextCursor)
		if !second.Complete || second.NextCursor != nil || len(second.Evidence) != 1 || reads != MaxCalls+1 {
			t.Fatalf("second batch: %+v (%d reads)", second, reads)
		}
		for i, id := range append(first.Evidence, second.Evidence...) {
			if want := fmt.Sprintf("record:%d", i+1); id != want {
				t.Fatalf("got %q, want %q", id, want)
			}
		}
	})

	t.Run("reduce pages with fixed filters and resume the accumulator", func(t *testing.T) {
		var reads int
		page := func(_ context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
			var args struct {
				Path  struct{ Project string }
				Query struct {
					Scope string
					Page  *int
				}
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if op != "list" || args.Path.Project != "p1" || args.Query.Scope != "open" || args.Query.Page == nil || *args.Query.Page != reads {
				return nil, fmt.Errorf("unexpected page arguments: %s", raw)
			}
			reads++
			if reads == MaxCalls+1 {
				return json.RawMessage(`{"next":null,"count":2}`), nil
			}
			return json.Marshal(map[string]int{"next": reads, "count": 2})
		}
		metadata := func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage("true"), nil
		}
		program := `const args = {path:{project:"p1"},query:{scope:"open"}};
			return api.reducePages("list", {args, cursorParam:"page", cursor, initial,
				next: page => page.next, reduce: (state, page) => {
					args.query.scope = "changed";
					return state + page.count;
				}
			});`
		raw, err := e.RunModeWithPages(context.Background(), `api.call("metadata"); const initial=0, cursor=0; `+program, ModeSync, metadata, page)
		var first struct {
			State      int
			Complete   bool
			NextCursor int
		}
		if err != nil || json.Unmarshal(raw, &first) != nil || reads != MaxCalls-1 ||
			first.Complete || first.NextCursor != reads || first.State != 2*reads {
			t.Fatalf("first execution: %s, %d pages: %v", raw, reads, err)
		}
		resume := fmt.Sprintf("const {state:initial,nextCursor:cursor} = %s; ", raw)
		got, err := e.RunModeWithPages(context.Background(), resume+program, ModeAsync, metadata, page)
		if err != nil || reads != MaxCalls+1 || string(got) != fmt.Sprintf(`{"state":%d,"complete":true,"nextCursor":null}`, 2*reads) {
			t.Fatalf("resumed execution: %s, %d pages: %v", got, reads, err)
		}
	})

	t.Run("invalid continuation and oversized state stop reads", func(t *testing.T) {
		for _, tc := range []struct {
			name, next, reduce, want string
			reads                    int
		}{
			{"missing cursor field", "p=>p.missing", "n=>n+1", "invalid_pagination", 1},
			{"cycle", "p=>p.n%2", "n=>n+1", "invalid_pagination", 3},
			{"UTF-8 state exceeds byte limit", "p=>p.n", "()=>'界'.repeat(6000)", "output_limit", 1},
			{"unsafe integer in intermediate state", "p=>p.n", "(n,p)=>p.n===1?9007199254740992:0", "unsafe_integer", 1},
			{"reducer omitted its return", "p=>p.n", "()=>{}", "invalid_result", 1},
		} {
			t.Run(tc.name, func(t *testing.T) {
				reads := 0
				page := func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
					reads++
					return json.Marshal(map[string]int{"n": reads})
				}
				body := fmt.Sprintf(`return api.reducePages("list", {cursorParam:"page", initial:0, next:%s, reduce:%s});`, tc.next, tc.reduce)
				_, err := e.RunModeWithPages(context.Background(), body, ModeAuto, nil, page)
				var guestErr *Error
				if !errors.As(err, &guestErr) || guestErr.Code != tc.want || reads != tc.reads {
					t.Fatalf("got %v after %d reads; want %s after %d", err, reads, tc.want, tc.reads)
				}
			})
		}
	})
}

func TestCumulativeGuestTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		b := &bridge{ctx: ctx, budget: startGuestBudget(cancel), call: func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
			time.Sleep(7 * time.Second)
			return json.RawMessage("true"), nil
		}}
		defer b.budget.timer.Stop()
		// Advance the guest-side clock between real bridge frames and the
		// dispatcher-side clock inside the callback, without burning CPU.
		for range 2 {
			time.Sleep(2 * time.Second)
			_, err := io.WriteString(b, "{\"kind\":\"call\",\"operation\":\"read\",\"args\":{}}\n")
			if err != nil || b.err != nil || b.reply.String() != "{\"value\":true}\n" {
				t.Fatalf("call failed: %v, %v, %s", err, b.err, &b.reply)
			}
			b.reply.Reset()
		}
		if ctx.Err() != nil {
			t.Fatal("the two host waits consumed the guest budget")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if ctx.Err() == nil {
			t.Fatal("the guest received a fresh five-second budget after a host call")
		}
	})
}
