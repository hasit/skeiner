// skeiner-bench is a development harness. Build and point it at the production
// binary when measuring stdio startup and peak server RSS.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hasit/skeiner/internal/catalog"
	"github.com/hasit/skeiner/internal/dispatch"
	"github.com/hasit/skeiner/internal/fixture"
	"github.com/hasit/skeiner/internal/sandbox"
	"github.com/hasit/skeiner/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const program = `const run = api.call("getRun", {path:{runId:"814"}});
const attempts = api.call("listJobAttempts", {path:{jobId:run.jobs[0].id}});
const page = api.call("getAttemptLogs", {path:{attemptId:attempts.attempts[0].id}});
return page.lines.map(line => line.id);`

var expected = []string{"814-U1-L001", "814-U1-L002"}

func verify(raw json.RawMessage) error {
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return err
	}
	if !slices.Equal(ids, expected) {
		return fmt.Errorf("unexpected log IDs %q", ids)
	}
	return nil
}

// direct uses the same dispatcher, transport settings, decoding/redaction and
// dependent IDs as the guest path. Each whole reply is parsed into objects
// before selecting IDs, just as it is by JSON.parse in the guest.
func direct(ctx context.Context, client *dispatch.Client) (json.RawMessage, error) {
	ids := func(op, param, id, field string) ([]string, error) {
		args, err := json.Marshal(map[string]any{"path": map[string]string{param: id}})
		if err != nil {
			return nil, err
		}
		raw, err := client.Call(ctx, op, args)
		if err != nil {
			return nil, err
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, err
		}
		list, ok := decoded[field].([]any)
		if !ok || len(list) == 0 {
			return nil, fmt.Errorf("missing %s in %s response", field, op)
		}
		values := make([]string, len(list))
		for i, item := range list {
			object, _ := item.(map[string]any)
			value, ok := object["id"].(string)
			if !ok {
				return nil, fmt.Errorf("invalid ID in %s response", op)
			}
			values[i] = value
		}
		return values, nil
	}
	jobs, err := ids("getRun", "runId", "814", "jobs")
	if err != nil {
		return nil, err
	}
	attempts, err := ids("listJobAttempts", "jobId", jobs[0], "attempts")
	if err != nil {
		return nil, err
	}
	lines, err := ids("getAttemptLogs", "attemptId", attempts[0], "lines")
	if err != nil {
		return nil, err
	}
	return json.Marshal(lines)
}

func summarize(label string, values []time.Duration) {
	slices.Sort(values)
	n := len(values)
	ms := func(q float64) float64 {
		rank := int(math.Ceil(float64(n)*q)) - 1
		return float64(values[rank]) / float64(time.Millisecond)
	}
	fmt.Printf("%-29s n=%-4d p50=%9.3f ms  p95=%9.3f ms\n", label, n, ms(.50), ms(.95))
}

func measure(label string, n int, step func() error) error {
	values := make([]time.Duration, n)
	for i := range values {
		start := time.Now()
		if err := step(); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		values[i] = time.Since(start)
	}
	summarize(label, values)
	return nil
}

func generatedSpec(n int) []byte {
	paths := make(map[string]any, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("listBuildEvents%d", i)
		paths[fmt.Sprintf("/builds/%d/events", i)] = map[string]any{"get": map[string]any{
			"operationId": id, "tags": []string{"builds"}, "summary": "Read build events and job logs",
			"responses": map[string]any{"200": map[string]any{"description": "events", "content": map[string]any{
				"application/json": map[string]any{"schema": map[string]any{"type": "array", "items": map[string]string{"type": "string"}}},
			}}},
		}}
	}
	b, err := json.Marshal(map[string]any{
		"openapi": "3.0.3", "info": map[string]string{"title": "Generated CI", "version": "1"}, "paths": paths,
	})
	if err != nil {
		panic(err)
	}
	return b
}

func openSession(ctx context.Context, binary, spec, base string) (*mcp.ClientSession, *exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, binary, "serve", "--spec", spec, "--base-url", base)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "SKEINER_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Stderr = os.Stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "skeiner-bench", Version: "dev"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	return session, cmd, err
}

func cold(ctx context.Context, binary, spec, base string) (time.Duration, error) {
	start := time.Now()
	session, _, err := openSession(ctx, binary, spec, base)
	if err != nil {
		return 0, err
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		return 0, err
	}
	if len(list.Tools) != 2 {
		return 0, errors.New("expected exactly two tools")
	}
	return time.Since(start), nil // teardown is excluded.
}

// Hold the first request of every program until all guests are active. This
// proves the documented concurrency and avoids a serialized measurement.
func peak(ctx context.Context, binary, spec string, handler http.Handler, parallel int) error {
	var entered atomic.Int32
	ready := make(chan struct{})
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/runs/814" {
			if int(entered.Add(1)) == parallel {
				close(ready)
			}
			select {
			case <-ready:
			case <-r.Context().Done():
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer host.Close()
	session, cmd, err := openSession(ctx, binary, spec, host.URL)
	if err != nil {
		return err
	}
	defer session.Close()
	var work sync.WaitGroup
	errs := make(chan error, parallel)
	for i := 0; i < parallel; i++ {
		work.Add(1)
		go func() {
			defer work.Done()
			res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "execute", Arguments: map[string]string{"code": program}})
			if err == nil {
				var body json.RawMessage
				body, err = resultBody(res)
				if err == nil {
					err = verify(body)
				}
			}
			errs <- err
		}()
	}
	work.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			return e
		}
	}
	if int(entered.Load()) != parallel {
		return errors.New("concurrent execution did not reach the fixture")
	}
	if runtime.GOOS != "linux" {
		fmt.Printf("peak RSS: Linux /proc required (%d simultaneous executions verified)\n", parallel)
		return nil
	}
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("read server peak RSS: %w", err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "VmHWM:") {
			fmt.Printf("server peak RSS with %d simultaneous executions: %s\n", parallel, strings.TrimSpace(strings.TrimPrefix(line, "VmHWM:")))
			return nil
		}
	}
	return errors.New("kernel did not report VmHWM")
}

func run() error {
	binaryFlag := flag.String("binary", "./skeiner", "built production CLI")
	inventory := flag.Bool("inventory", false, "run the paginated inventory comparison instead of runtime timings")
	reruns := flag.Bool("reruns", false, "measure fresh saved-program processes with and without a runtime cache")
	spec := flag.String("spec", "examples/ci/openapi.yaml", "CI fixture specification")
	n := flag.Int("n", 100, "warm samples per measurement")
	processes := flag.Int("cold", 5, "fresh process samples")
	parallel := flag.Int("parallel", 4, "simultaneous executions for RSS measurement (1-4)")
	size := flag.Int("operations", 1000, "operations in generated search workload")
	flag.Parse()
	if *n < 1 || *processes < 1 || *parallel < 1 || *parallel > 4 || *size < 1 {
		return errors.New("positive sample/operation counts and concurrency 1-4 required")
	}
	binary, err := filepath.Abs(*binaryFlag)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if *reruns {
		if *inventory {
			return errors.New("select one workload: -reruns or -inventory")
		}
		return measureReruns(ctx, binary, *spec, *processes)
	}
	if *inventory {
		return compareInventory(ctx, binary)
	}
	specBytes, err := os.ReadFile(*spec)
	if err != nil {
		return err
	}
	h, err := fixture.New("assertion")
	if err != nil {
		return err
	}
	host := httptest.NewServer(h)
	defer host.Close()

	cat, err := catalog.Parse(specBytes, nil)
	if err != nil {
		return err
	}
	candidate := generatedSpec(*size)
	large, err := catalog.Parse(candidate, nil)
	if err != nil {
		return err
	}
	if large.Len() != *size || cat.Len() != 5 {
		return errors.New("unexpected number of published operations")
	}
	fmt.Printf("%s %s/%s, %d logical CPUs, GOMAXPROCS=%d\n", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.GOMAXPROCS(0))
	fmt.Printf("fixture %d operations / %d bytes; generated %d operations / %d bytes\n", cat.Len(), len(specBytes), large.Len(), len(candidate))

	// Cold process samples stop when tools/list completes, before Close.
	coldTimes := make([]time.Duration, *processes)
	for i := range coldTimes {
		elapsed, err := cold(ctx, binary, *spec, host.URL)
		if err != nil {
			return err
		}
		coldTimes[i] = elapsed
	}
	summarize("cold process to tools/list", coldTimes)

	if err := measure("index fixture (incl. read)", *n, func() error {
		_, err := catalog.Load(*spec, nil)
		return err
	}); err != nil {
		return err
	}
	engine, err := sandbox.New(ctx)
	if err != nil {
		return err
	}
	defer engine.Close(ctx)
	client, err := dispatch.New(cat, dispatch.Config{BaseURL: host.URL})
	if err != nil {
		return err
	}
	defer client.Close()
	svc := service.New(cat, engine, client, nil)
	largeSvc := service.New(large, engine, client, nil) // only search is invoked.
	for _, entry := range []struct {
		label string
		svc   *service.Service
	}{{"warm search (fixture)", svc}, {"warm search (generated)", largeSvc}} {
		search := func() error {
			_, err := entry.svc.Search("failed job logs")
			return err
		}
		if err := search(); err != nil {
			return err
		}
		if err := measure(entry.label, *n, search); err != nil {
			return err
		}
	}
	for _, entry := range []struct {
		label string
		run   func() (json.RawMessage, error)
	}{
		{"warm execute / three calls", func() (json.RawMessage, error) { return svc.Execute(ctx, program) }},
		{"direct Go / three calls", func() (json.RawMessage, error) { return direct(ctx, client) }},
	} {
		step := func() error {
			raw, err := entry.run()
			if err != nil {
				return err
			}
			return verify(raw)
		}
		if err := step(); err != nil {
			return err
		}
		if err := measure(entry.label, *n, step); err != nil {
			return err
		}
	}
	return peak(ctx, binary, *spec, h, *parallel)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
