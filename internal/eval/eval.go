// Package eval runs owner-defined tasks through an MCP client. Drivers see the
// public task and MCP tools. The grader alone receives the owner’s rubric.
package eval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hasit/skeiner/internal/yamljson"
)

type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// Client is the entire interface available to an agent.
type Client interface {
	Tools(context.Context) ([]Tool, error)
	Call(context.Context, string, map[string]any) (json.RawMessage, error)
}

type Session interface {
	Client
	Close()
}

type Driver interface {
	Name() string
	Answer(context.Context, TaskInput, Client) (Answer, error)
}

// TaskInput is the public contract. It never contains fixture state, expected
// answers, required evidence, or operation hints from the owner-side rubric.
type TaskInput struct {
	Prompt         string         `json:"prompt"`
	Version        int            `json:"version"`
	AnswerFields   []AnswerField  `json:"answer_fields,omitempty"`
	CitationFields CitationFields `json:"citation_fields"`
}

type AnswerField struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

type Answer struct {
	Summary   string                     `json:"summary,omitempty"`
	Citations []Citation                 `json:"citations"`
	Facts     map[string]json.RawMessage `json:"facts,omitempty"`
}

type Citation struct {
	ID     string  `json:"id"`
	Text   string  `json:"text"`
	Source *Source `json:"source,omitempty"`
}

type Source struct {
	Operation string          `json:"operation"`
	Args      json.RawMessage `json:"args"`
}

// Exchange is an owner-side observation. Body is examined locally and is never
// copied into a report or provided to a driver by this observer.
type Exchange struct {
	Operation string          `json:"operation"`
	Args      json.RawMessage `json:"args"`
	Status    int             `json:"status"`
	Duration  time.Duration   `json:"duration,omitempty"`
	Code      string          `json:"code,omitempty"`
	Body      json.RawMessage `json:"body"`
}

type Options struct {
	Connect       func(context.Context, string, func(Exchange)) (Session, error)
	Driver        Driver
	Case          string
	StateOverride string
	Timeout       time.Duration
	Redact        func(string) string
}

type suite struct {
	Version        int            `json:"version"`
	Driver         string         `json:"driver"`
	CitationFields CitationFields `json:"citation_fields"`
	Cases          []task         `json:"cases"`
}

type CitationFields struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type task struct {
	ID             string          `json:"id"`
	Prompt         string          `json:"prompt"`
	State          string          `json:"state"`
	Facts          []fact          `json:"facts"`
	Evidence       []Citation      `json:"evidence"`
	AnswerFields   []AnswerField   `json:"answer_fields,omitempty"`
	CitationFields *CitationFields `json:"citation_fields,omitempty"`
}
type fact struct {
	Label   string          `json:"label"`
	Pattern string          `json:"pattern"`
	Reject  string          `json:"reject,omitempty"`
	Field   string          `json:"field,omitempty"`
	Value   json.RawMessage `json:"value,omitempty"`
	re      *regexp.Regexp
	reject  *regexp.Regexp
}

type Report struct {
	Driver  string   `json:"driver"`
	Results []Result `json:"results"`
}

func (r Report) Passed() bool {
	if len(r.Results) == 0 {
		return false
	}
	for _, c := range r.Results {
		if !c.Passed {
			return false
		}
	}
	return true
}

type Result struct {
	Case        string  `json:"case"`
	State       string  `json:"state,omitempty"`
	Prompt      string  `json:"prompt"`
	Passed      bool    `json:"passed"`
	Answer      Answer  `json:"answer"`
	Checks      []Check `json:"checks,omitempty"`
	Diagnosis   string  `json:"diagnosis,omitempty"`
	Trace       []Event `json:"trace"`
	ElapsedMS   int64   `json:"elapsed_ms"`
	GradingMode string  `json:"grading_mode"`
	ReceiptMode string  `json:"receipt_mode"`
}

type Check struct {
	Label  string `json:"label"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

type Event struct {
	Type       string     `json:"type"`
	Name       string     `json:"name"`
	Query      string     `json:"query,omitempty"`
	Operations []string   `json:"operations,omitempty"`
	Status     int        `json:"status,omitempty"`
	Error      string     `json:"error,omitempty"`
	Evidence   []Citation `json:"evidence,omitempty"`
	ElapsedUS  int64      `json:"elapsed_us"`
}

func load(path string) (suite, error) {
	var s suite
	raw, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	if err = yamljson.Decode(raw, &s); err != nil {
		return s, fmt.Errorf("read suite: %w", err)
	}
	if (s.Version != 1 && s.Version != 2) || len(s.Cases) == 0 {
		return s, errors.New("suite requires version 1 or 2 and at least one case")
	}
	if s.CitationFields.ID == "" {
		s.CitationFields.ID = "id"
	}
	if s.CitationFields.Text == "" {
		s.CitationFields.Text = "text"
	}
	ids := map[string]bool{}
	for i := range s.Cases {
		t := &s.Cases[i]
		if t.ID == "" || ids[t.ID] || strings.TrimSpace(t.Prompt) == "" || (len(t.Facts)+len(t.Evidence) == 0) {
			return s, fmt.Errorf("case %d needs a unique id, prompt and rubric", i+1)
		}
		ids[t.ID] = true
		if t.CitationFields != nil && (t.CitationFields.ID == "" || t.CitationFields.Text == "") {
			return s, fmt.Errorf("case %s requires both citation field names", t.ID)
		}
		if s.Version == 2 {
			if err := validateStructuredTask(*t); err != nil {
				return s, fmt.Errorf("case %s: %w", t.ID, err)
			}
		}
		labels := map[string]bool{}
		for j := range t.Facts {
			f := &t.Facts[j]
			if s.Version == 2 {
				continue
			}
			if strings.TrimSpace(f.Label) == "" || f.Pattern == "" || labels[f.Label] {
				return s, fmt.Errorf("case %s needs a unique label and nonempty pattern for each fact", t.ID)
			}
			labels[f.Label] = true
			f.re, err = regexp.Compile(f.Pattern)
			if err != nil {
				return s, fmt.Errorf("%s / %s: %w", t.ID, f.Label, err)
			}
			if f.reject, err = optionalRegexp(f.Reject); err != nil {
				return s, fmt.Errorf("%s / %s rejection: %w", t.ID, f.Label, err)
			}
		}
		seen := map[[32]byte]bool{}
		for _, e := range t.Evidence {
			if e.ID == "" || e.Text == "" || seen[fingerprint(e)] {
				return s, fmt.Errorf("case %s requires unique evidence records and nonempty quotes", t.ID)
			}
			if s.Version == 2 && !validSource(e.Source) {
				return s, fmt.Errorf("case %s requires an operation and argument object for every evidence source", t.ID)
			}
			seen[fingerprint(e)] = true
		}
	}
	return s, nil
}

func optionalRegexp(s string) (*regexp.Regexp, error) {
	if s == "" {
		return nil, nil
	}
	return regexp.Compile(s)
}
func DefaultDriver(path string) (string, error) {
	s, err := load(path)
	if err != nil {
		return "", err
	}
	switch s.Driver {
	case "":
		return "openai", nil
	case "openai", "replay":
		return s.Driver, nil
	default:
		return "", fmt.Errorf("unknown suite driver %q", s.Driver)
	}
}
func Run(ctx context.Context, path string, opts Options) (Report, error) {
	var r Report
	if opts.Driver == nil || opts.Connect == nil {
		return r, errors.New("evaluation requires a driver and an MCP connector")
	}
	s, err := load(path)
	if err != nil {
		return r, err
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 2 * time.Minute
	}
	if opts.Redact == nil {
		opts.Redact = func(s string) string { return s }
	}
	r.Driver = opts.Driver.Name()
	for _, t := range s.Cases {
		if opts.Case != "" && opts.Case != t.ID {
			continue
		}
		if ctx.Err() != nil {
			return r, ctx.Err()
		}
		if opts.StateOverride != "" {
			t.State = opts.StateOverride
		}
		r.Results = append(r.Results, runCase(ctx, s, t, opts))
	}
	if len(r.Results) == 0 {
		return r, fmt.Errorf("unknown case %q", opts.Case)
	}
	return r, nil
}

type recorder struct {
	mu             sync.Mutex
	events         []Event
	wanted         []Citation
	requiredSeen   map[[32]byte]bool
	seen           map[[32]byte]bool
	idKey, textKey string
	redact         func(string) string
	executed       bool
	retrieved      bool
	version        int
}

func (r *recorder) clean(s string) string {
	s = r.redact(scrub(s))
	if len(s) > 2048 {
		return s[:2048] + "…"
	}
	return s
}

var sensitiveHeader = regexp.MustCompile(`(?i)\b(authorization|proxy-authorization|cookie|set-cookie|x-api-key|api-key)\s*[:=]\s*[^\r\n,}]+`)

func scrub(s string) string { return sensitiveHeader.ReplaceAllString(s, "$1=[REDACTED]") }

func (r *recorder) add(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

// Fingerprint records so extra citations can be checked without retaining
// unrelated payloads in owner-side memory or writing them to the trace.
func fingerprint(c Citation) [32]byte {
	if c.Source != nil {
		copy := *c.Source
		if v, err := sourceArgs(copy.Args); err == nil {
			copy.Args, _ = json.Marshal(v)
		}
		c.Source = &copy
	}
	data, _ := json.Marshal(c)
	return sha256.Sum256(data)
}
func (r *recorder) observe(x Exchange) {
	e := Event{Type: "upstream", Name: r.clean(x.Operation), Status: x.Status, Error: r.clean(x.Code), ElapsedUS: x.Duration.Microseconds()}
	var val any
	if len(x.Body) > 0 && x.Status >= 200 && x.Status < 300 && x.Code == "" {
		d := json.NewDecoder(bytes.NewReader(x.Body))
		d.UseNumber()
		if d.Decode(&val) != nil {
			r.add(e)
			return
		}
		r.mu.Lock()
		r.retrieved = true
		walkRecords(val, func(m map[string]any) {
			var id string
			switch x := m[r.idKey].(type) {
			case string:
				id = x
			case json.Number:
				id = x.String()
			}
			quote, ok := m[r.textKey].(string)
			if id == "" || !ok {
				return
			}
			c := Citation{ID: id, Text: quote}
			if r.version == 2 {
				c.Source = &Source{Operation: x.Operation, Args: x.Args}
				if !validSource(c.Source) {
					return
				}
			}
			r.seen[fingerprint(c)] = true
			for _, wanted := range r.wanted {
				if matchesEvidence(wanted, c) {
					r.requiredSeen[fingerprint(wanted)] = true
					e.Evidence = append(e.Evidence, r.cleanCitation(c))
					break
				}
			}
		})
		r.mu.Unlock()
	}
	r.add(e)
}
func walkRecords(v any, f func(map[string]any)) {
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			walkRecords(item, f)
		}
	case map[string]any:
		f(x)
		for _, item := range x {
			walkRecords(item, f)
		}
	}
}

type tracedClient struct {
	Client
	r *recorder
}

func (c tracedClient) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	t0 := time.Now()
	v, err := c.Client.Call(ctx, name, args)
	e := Event{Type: "tool", Name: c.r.clean(name), ElapsedUS: time.Since(t0).Microseconds()}
	if name == "search" {
		q, _ := args["query"].(string)
		e.Query = c.r.clean(q)
		var ops []struct {
			ID string `json:"operationId"`
		}
		if json.Unmarshal(v, &ops) == nil {
			for _, o := range ops {
				e.Operations = append(e.Operations, c.r.clean(o.ID))
			}
		}
	}
	if err != nil {
		e.Error = c.r.clean(err.Error())
	}
	if name == "execute" && err == nil {
		c.r.mu.Lock()
		c.r.executed = true
		c.r.mu.Unlock()
	}
	c.r.add(e)
	return v, err
}

func runCase(parent context.Context, s suite, t task, o Options) (out Result) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(parent, o.Timeout)
	defer cancel()
	r := newRecorder(s, t, o.Redact)
	out = baseResult(s, t, r, "observed-upstream")
	defer func() {
		r.mu.Lock()
		out.Trace = append([]Event{}, r.events...)
		r.mu.Unlock()
		out.ElapsedMS = time.Since(start).Milliseconds()
	}()
	session, err := o.Connect(ctx, t.State, r.observe)
	if err != nil {
		out.Diagnosis = "MCP setup: " + r.clean(err.Error())
		return
	}
	defer session.Close()
	answer, err := o.Driver.Answer(ctx, publicTask(s, t), tracedClient{session, r})
	if err != nil {
		out.Diagnosis = "agent or tool execution: " + r.clean(err.Error())
		return
	}
	gradeAnswer(&out, s, t, answer, r)
	return
}

func gradeAnswer(out *Result, s suite, t task, answer Answer, r *recorder) {
	out.Answer = Answer{Summary: r.clean(answer.Summary)}
	if answer.Facts != nil {
		out.Answer.Facts = map[string]json.RawMessage{}
		for k, v := range answer.Facts {
			out.Answer.Facts[r.clean(k)] = r.cleanJSON(v)
		}
	}
	for _, e := range answer.Citations {
		out.Answer.Citations = append(out.Answer.Citations, r.cleanCitation(e))
	}
	out.Passed = true
	add := func(label string, passed bool, detail string) {
		if passed {
			detail = ""
		} else {
			out.Passed = false
			if out.Diagnosis == "" {
				out.Diagnosis = detail
			}
		}
		out.Checks = append(out.Checks, Check{label, passed, detail})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	add("retrieval through execution", r.executed && r.retrieved, "no successful upstream retrieval through execution; inspect discovery and tool errors")
	// Evidence checks come first so missing upstream records are distinguishable
	// from model interpretation of evidence that it actually fetched.
	for _, e := range t.Evidence {
		detail := "expected record was not retrieved; inspect chosen operations, pagination and fixture data"
		if r.requiredSeen[fingerprint(e)] {
			detail = "upstream returned this record; the final answer omitted or misquoted it"
		}
		cited := false
		for _, c := range answer.Citations {
			if matchesEvidence(e, c) && r.seen[fingerprint(c)] {
				cited = true
				break
			}
		}
		add("citation "+e.ID, r.requiredSeen[fingerprint(e)] && cited, detail)
	}
	for _, c := range answer.Citations {
		if !r.seen[fingerprint(c)] {
			add("source for citation "+r.clean(c.ID), false, "the final answer cites a record ID and text pair that no successful upstream call returned")
		}
	}
	for _, f := range t.Facts {
		if s.Version == 2 {
			value, exists := answer.Facts[f.Field]
			add("fact "+f.Field, exists && scalarEqual(f.Value, value), "structured answer did not establish "+f.Field)
		} else {
			add(f.Label, f.re.MatchString(answer.Summary) && (f.reject == nil || !f.reject.MatchString(answer.Summary)), "final answer did not establish "+f.Label+"; inspect the returned evidence and agent interpretation")
		}
	}
	if s.Version == 2 {
		add("structured answer only", strings.TrimSpace(answer.Summary) == "", "version 2 accepts facts and citations only; free-form summaries are not graded")
		add("declared fact fields only", len(answer.Facts) == len(t.AnswerFields), "answer must contain exactly the declared fact fields")
	}
}
