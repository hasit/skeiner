// Package sandbox executes a JavaScript function body in a fresh QuickJS WebAssembly instance.
package sandbox

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	quickjs "github.com/paralin/go-quickjs-wasi"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

const (
	MemoryBytes    = 32 << 20
	MaxCodeBytes   = 32 << 10
	MaxOutputBytes = 16 << 10
	MaxCalls       = 16
	MaxConcurrent  = 4
	MaxDuration    = 30 * time.Second
	maxSlotWait    = 5 * time.Second
	maxGuestTime   = 5 * time.Second
	maxFrameBytes  = 64 << 10
	maxReplyBytes  = 1 << 20
)

//go:embed guest.js
var bootstrap string

// Error describes a safe, actionable failure. Guest strings and Wasm backtraces never leave the VM.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

type Engine struct {
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	slots    chan struct{}
}

func New(ctx context.Context) (*Engine, error) {
	config := wazero.NewRuntimeConfig().WithMemoryLimitPages(MemoryBytes / 65536).WithCloseOnContextDone(true)
	r := wazero.NewRuntimeWithConfig(ctx, config)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		r.Close(ctx)
		return nil, fmt.Errorf("initialize WASI: %w", err)
	}
	compiled, err := r.CompileModule(ctx, quickjs.QuickJSWASM)
	if err != nil {
		r.Close(ctx)
		return nil, fmt.Errorf("compile embedded QuickJS: %w", err)
	}
	return &Engine{runtime: r, compiled: compiled, slots: make(chan struct{}, MaxConcurrent)}, nil
}

func (e *Engine) Close(ctx context.Context) error { return e.runtime.Close(ctx) }

// Callback authorizes an operation, dispatches it, and returns client-safe errors.
type Callback func(context.Context, string, json.RawMessage) (json.RawMessage, error)

type Mode string

const (
	ModeAuto  Mode = ""
	ModeSync  Mode = "sync"
	ModeAsync Mode = "async"
)

func (e *Engine) Run(ctx context.Context, code string, call Callback) (json.RawMessage, error) {
	return e.RunMode(ctx, code, ModeAuto, call)
}

// RunMode invokes host calls serially under one overall deadline in every JavaScript mode.
// The callback must authorize each operation and return client-safe errors.
func (e *Engine) RunMode(ctx context.Context, code string, mode Mode, call Callback) (json.RawMessage, error) {
	return e.RunModeWithPages(ctx, code, mode, call, nil)
}

// RunModeWithPages enables paginated reads when readPage is supplied. That
// callback must require an authorized GET. Both callbacks share one call budget.
func (e *Engine) RunModeWithPages(ctx context.Context, code string, mode Mode, call, readPage Callback) (json.RawMessage, error) {
	if mode != ModeAuto && mode != ModeSync && mode != ModeAsync {
		return nil, &Error{"invalid_arguments", "Choose sync or async execution mode."}
	}
	if len(code) > MaxCodeBytes {
		return nil, &Error{"code_limit", "Shorten the JavaScript program (32 KiB maximum)."}
	}
	ctx, cancel := context.WithTimeout(ctx, MaxDuration)
	defer cancel()
	admission, endAdmission := context.WithTimeout(ctx, maxSlotWait)
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	case <-admission.Done():
	}
	err := admission.Err()
	endAdmission()
	if err != nil {
		return nil, err
	}

	// The WASI command sees this execution's source and in-memory frames only.
	// No filesystem, environment, process stdio, sockets or system clocks are inherited.
	guest, interrupt := context.WithCancel(ctx)
	defer interrupt()
	budget := startGuestBudget(interrupt)
	defer budget.timer.Stop()
	b := &bridge{ctx: guest, budget: budget, call: call, readPage: readPage}
	body, err := json.Marshal(code)
	if err != nil {
		return nil, err
	}
	config := wazero.NewModuleConfig().WithName("").WithStdin(&b.reply).WithStdout(b).
		WithStderr(io.Discard).WithArgs("qjs", "--std", "--memory-limit", "16384", "-e", fmt.Sprintf("%s(%s,%q,%d)", bootstrap, body, mode, MaxOutputBytes))
	instance, err := e.runtime.InstantiateModule(guest, e.compiled, config)
	if instance != nil {
		defer instance.Close(context.Background())
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if _, ok := budget.pause(); !ok {
		return nil, context.DeadlineExceeded
	}
	if b.err != nil {
		return nil, b.err
	}
	var exit *sys.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 0) {
		return nil, &Error{"guest_failure", "QuickJS stopped before completion. Reduce allocations and check the program."}
	}
	if len(b.final) == 0 {
		return nil, &Error{"invalid_result", "Execution ended without a result. Return JSON or a promise that settles to JSON."}
	}
	return b.final, nil
}

type bridge struct {
	ctx        context.Context
	budget     *guestBudget
	call       Callback
	readPage   Callback
	line       bytes.Buffer
	reply      bytes.Buffer
	final      json.RawMessage
	callErrors [MaxCalls]error
	calls      int
	err        error
}

// Only the execution goroutine pauses and resumes the clock. The timer callback
// only cancels the guest context, so it can interrupt WASM even in a tight loop.
type guestBudget struct {
	deadline  time.Time
	timer     *time.Timer
	interrupt context.CancelFunc
}

func startGuestBudget(interrupt context.CancelFunc) *guestBudget {
	deadline := time.Now().Add(maxGuestTime)
	return &guestBudget{deadline: deadline, timer: time.AfterFunc(time.Until(deadline), interrupt), interrupt: interrupt}
}

func (g *guestBudget) pause() (time.Duration, bool) {
	stopped := g.timer.Stop()
	remaining := time.Until(g.deadline)
	if !stopped || remaining <= 0 {
		// The deadline may have passed before the timer goroutine was scheduled.
		g.interrupt()
		return 0, false
	}
	return remaining, true
}

func (g *guestBudget) resume(remaining time.Duration) {
	g.deadline = time.Now().Add(remaining)
	g.timer.Reset(remaining)
}

// Write receives guest stdout (never MCP stdout). Frames can span any number of fd_write calls.
// An untrusted guest can forge frames. Every call goes through the budget, validator and dispatcher.
func (b *bridge) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 && b.err == nil {
		end := bytes.IndexByte(p, '\n')
		fragment := p
		if end >= 0 {
			fragment = p[:end]
		}
		if len(fragment) > maxFrameBytes-b.line.Len() {
			b.err = &Error{"frame_limit", "Reduce the arguments or return fewer fields."}
			break
		}
		b.line.Write(fragment)
		if end < 0 {
			break
		}
		b.accept(b.line.Bytes())
		b.line.Reset()
		p = p[end+1:]
	}
	return n, nil
}

type frame struct {
	Kind      string          `json:"kind"`
	Operation string          `json:"operation"`
	Args      json.RawMessage `json:"args"`
	Value     json.RawMessage `json:"value"`
	Call      int             `json:"call"`
}

func (b *bridge) accept(line []byte) {
	var f frame
	if json.Unmarshal(line, &f) != nil || b.final != nil || b.reply.Len() != 0 {
		b.err = &Error{"guest_protocol", "Use api.call and return the final value."}
		return
	}
	switch f.Kind {
	case "remainingCalls":
		fmt.Fprintln(&b.reply, MaxCalls-b.calls)
	case "call", "readPage":
		b.invoke(f)
	case "return":
		if len(f.Value) > MaxOutputBytes {
			b.err = &Error{"output_limit", "Return fewer results or select smaller fields (16 KiB maximum)."}
		} else if len(f.Value) == 0 {
			b.err = &Error{"invalid_result", "Return a JSON-serializable value."}
		} else if !safeJSNumbers(f.Value) {
			b.err = unsafeIntegerError()
		} else {
			b.final = f.Value
		}
	case "hostError":
		if f.Call < 1 || f.Call > MaxCalls || b.callErrors[f.Call-1] == nil {
			b.err = &Error{"guest_protocol", "No failed API call matches this error."}
		} else {
			b.err = b.callErrors[f.Call-1]
		}
	case "arguments":
		b.err = &Error{"invalid_arguments", "Pass a string operation identifier and an argument object."}
	case "pagination":
		b.err = &Error{"invalid_pagination", "Check the cursor query parameter and next cursor field. Each page must return a new cursor or null at the end."}
	case "unsafe_integer":
		b.err = unsafeIntegerError()
	case "syntax":
		b.err = &Error{"syntax_error", "Invalid JavaScript function body. Use an explicit return and place await inside an async body."}
	case "memory":
		b.err = &Error{"memory_limit", "Reduce guest allocations (16 MiB JavaScript heap, 32 MiB WebAssembly memory maximum)."}
	case "output":
		b.err = &Error{"output_limit", "Return fewer results or select smaller fields (16 KiB maximum)."}
	case "result":
		b.err = &Error{"invalid_result", "Add an explicit return for the value you want to inspect (for example, return {ok: true};). Resolve nested promises before returning finite, acyclic JSON."}
	case "type", "reference", "range", "exception":
		b.err = &Error{"javascript_error", "JavaScript " + f.Kind + " error. Check variable names, response fields, and array bounds."}
	default:
		b.err = &Error{"guest_protocol", "Use api.call and return the final value."}
	}
}

func (b *bridge) invoke(f frame) {
	b.calls++
	call := b.call
	if f.Kind == "readPage" {
		call = b.readPage
	}
	var result json.RawMessage
	var err error
	switch {
	case b.calls > MaxCalls:
		b.err = &Error{"call_limit", fmt.Sprintf("Use at most %d calls per execution. Check api.remainingCalls before the next read and return evidence with the next unread cursor.", MaxCalls)}
		return
	case b.ctx.Err() != nil:
		err = b.ctx.Err()
	case f.Operation == "" || len(f.Operation) > 512 || len(f.Args) == 0 || f.Args[0] != '{':
		err = &Error{"invalid_arguments", "api.call requires an operation identifier and an object of arguments."}
	case call == nil:
		err = &Error{"denied", "No upstream operations are available."}
	case !safeJSNumbers(f.Args):
		err = unsafeIntegerError()
	default:
		remaining, ok := b.budget.pause()
		if !ok {
			b.err = context.DeadlineExceeded
			return
		}
		result, err = call(b.ctx, f.Operation, f.Args)
		b.budget.resume(remaining)
		if err == nil {
			switch {
			case len(result) > maxReplyBytes || !json.Valid(result):
				err = &Error{"invalid_upstream_result", "Expected a bounded JSON response. Request a smaller page."}
			case !safeJSNumbers(result):
				err = unsafeIntegerError()
			}
		}
	}
	if err != nil {
		// Dispatcher errors are already sanitized by the host. Keep their original Go type
		// for an uncaught failure, and make the reason available to JS for recovery.
		b.callErrors[b.calls-1] = err
		_ = json.NewEncoder(&b.reply).Encode(struct {
			Error string `json:"error"`
			Call  int    `json:"call"`
		}{err.Error(), b.calls})
		return
	}
	// Compact to a single line even if an upstream returned pretty-printed JSON.
	b.reply.WriteString(`{"value":`)
	_ = json.Compact(&b.reply, result)
	b.reply.WriteString("}\n")
}

func unsafeIntegerError() *Error {
	return &Error{"unsafe_integer", "JavaScript numbers support integers only within ±9007199254740991. Use an API that represents larger identifiers as strings."}
}

// All callers supply bounded, valid JSON. Decode numbers as binary64, as the
// guest does. At magnitude 2^53 and above, neighboring integers can collide.
// Decimal and exponent spellings follow the same rule; string IDs stay exact.
func safeJSNumbers(raw json.RawMessage) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for {
		token, err := decoder.Token()
		if err != nil {
			// For valid JSON a conversion error means the number overflowed.
			return err == io.EOF
		}
		if n, ok := token.(float64); ok && math.Abs(n) >= 1<<53 {
			return false
		}
	}
}
