package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"strings"
)

func decodeJSON(raw json.RawMessage) (any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, errors.New("expected one JSON value")
	}
	return value, nil
}

func scalarType(raw json.RawMessage, kind string) bool {
	v, err := decodeJSON(raw)
	if err != nil {
		return false
	}
	switch kind {
	case "null":
		return v == nil
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "number", "integer":
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		r, ok := new(big.Rat).SetString(n.String())
		return ok && (kind == "number" || r.IsInt())
	default:
		return false
	}
}

func scalarEqual(a, b json.RawMessage) bool {
	x, err := decodeJSON(a)
	if err != nil {
		return false
	}
	y, err := decodeJSON(b)
	if err != nil {
		return false
	}
	if n, ok := x.(json.Number); ok {
		m, ok := y.(json.Number)
		if !ok {
			return false
		}
		r, ok := new(big.Rat).SetString(n.String())
		if !ok {
			return false
		}
		s, ok := new(big.Rat).SetString(m.String())
		return ok && r.Cmp(s) == 0
	}
	return reflect.DeepEqual(x, y)
}

func validateStructuredTask(t task) error {
	if len(t.AnswerFields) == 0 || len(t.AnswerFields) != len(t.Facts) {
		return errors.New("version 2 requires public answer_fields and exactly one expected fact per field")
	}
	fields := map[string]string{}
	for _, f := range t.AnswerFields {
		if strings.TrimSpace(f.Name) == "" || fields[f.Name] != "" {
			return errors.New("answer fields need unique nonempty names")
		}
		switch f.Type {
		case "string", "number", "integer", "boolean", "null":
		default:
			return fmt.Errorf("unsupported answer type for %s", f.Name)
		}
		fields[f.Name] = f.Type
	}
	seen := map[string]bool{}
	for _, f := range t.Facts {
		if seen[f.Field] || fields[f.Field] == "" || !scalarType(f.Value, fields[f.Field]) {
			return errors.New("every expected fact must match one public field and its scalar type")
		}
		if f.Pattern != "" || f.Reject != "" {
			return errors.New("version 2 facts use field/value, not summary patterns")
		}
		seen[f.Field] = true
	}
	return nil
}

func validSource(s *Source) bool {
	if s == nil || strings.TrimSpace(s.Operation) == "" {
		return false
	}
	v, err := decodeJSON(s.Args)
	_, ok := v.(map[string]any)
	return err == nil && ok
}

// The dispatcher treats an omitted path/query group and an empty object as the
// same request. HTTP-side receipts cannot recover that JavaScript spelling.
// Preserve empty parameter values and every nonempty group.
func sourceArgs(raw json.RawMessage) (any, error) {
	v, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}
	if object, ok := v.(map[string]any); ok {
		for _, name := range []string{"path", "query"} {
			if group, ok := object[name].(map[string]any); ok && len(group) == 0 {
				delete(object, name)
			}
		}
	}
	return v, nil
}

// An owner rubric names resource-defining arguments and can omit pagination
// arguments. Actual citations still bind to one complete observed call.
func subset(expected, actual any) bool {
	if object, ok := expected.(map[string]any); ok {
		other, ok := actual.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range object {
			v, present := other[key]
			if !present || !subset(value, v) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(expected, actual)
}

func matchesEvidence(expected, actual Citation) bool {
	if expected.ID != actual.ID || expected.Text != actual.Text {
		return false
	}
	if expected.Source == nil {
		return actual.Source == nil
	}
	if !validSource(expected.Source) || !validSource(actual.Source) || expected.Source.Operation != actual.Source.Operation {
		return false
	}
	x, _ := sourceArgs(expected.Source.Args)
	y, _ := sourceArgs(actual.Source.Args)
	return subset(x, y)
}

func (r *recorder) cleanJSON(raw json.RawMessage) json.RawMessage {
	v, err := decodeJSON(raw)
	if err != nil {
		return json.RawMessage("null")
	}
	var clean func(any) any
	clean = func(v any) any {
		switch x := v.(type) {
		case string:
			return r.clean(x)
		case []any:
			for i := range x {
				x[i] = clean(x[i])
			}
			return x
		case map[string]any:
			out := map[string]any{}
			for k, v := range x {
				out[r.clean(k)] = clean(v)
			}
			return out
		default:
			return v
		}
	}
	b, _ := json.Marshal(clean(v))
	return b
}

func (r *recorder) cleanCitation(c Citation) Citation {
	out := Citation{ID: r.clean(c.ID), Text: r.clean(c.Text)}
	if c.Source != nil {
		out.Source = &Source{Operation: r.clean(c.Source.Operation), Args: r.cleanJSON(c.Source.Args)}
	}
	return out
}

func publicTask(s suite, t task) TaskInput {
	fields := s.CitationFields
	if t.CitationFields != nil {
		fields = *t.CitationFields
	}
	return TaskInput{Prompt: t.Prompt, Input: t.Input, Version: s.Version, AnswerFields: append([]AnswerField(nil), t.AnswerFields...), CitationFields: fields}
}

func newRecorder(s suite, t task, redact func(string) string) *recorder {
	if redact == nil {
		redact = func(v string) string { return v }
	}
	fields := s.CitationFields
	if t.CitationFields != nil {
		fields = *t.CitationFields
	}
	return &recorder{wanted: t.Evidence, requiredSeen: map[[32]byte]bool{}, seen: map[[32]byte]bool{}, idKey: fields.ID, textKey: fields.Text, redact: redact, version: s.Version}
}

func baseResult(s suite, t task, r *recorder, receipts string) Result {
	mode := "legacy-summary-regex"
	if s.Version == 2 {
		mode = "structured-facts-and-citations"
	}
	return Result{Case: t.ID, State: t.State, Prompt: r.clean(t.Prompt), GradingMode: mode, ReceiptMode: receipts}
}

// LoadTaskInputs returns only model-safe prompts and answer shapes. Case keys
// are for the owner to select tasks; drivers receive TaskInput values only.
func LoadTaskInputs(path, caseID string) (map[string]TaskInput, error) {
	s, err := load(path)
	if err != nil {
		return nil, err
	}
	out := map[string]TaskInput{}
	for _, t := range s.Cases {
		if caseID == "" || caseID == t.ID {
			out[t.ID] = publicTask(s, t)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("unknown case %q", caseID)
	}
	return out, nil
}

// GradeAnswer is for trusted owner-side comparison harnesses. Receipts are
// supplied by that harness, not authenticated by this function or by an agent.
func GradeAnswer(path, caseID string, answer Answer, exchanges []Exchange) (Result, error) {
	s, err := load(path)
	if err != nil {
		return Result{}, err
	}
	for _, t := range s.Cases {
		if t.ID != caseID {
			continue
		}
		r := newRecorder(s, t, nil)
		for _, x := range exchanges {
			r.observe(x)
		}
		r.executed = len(exchanges) > 0
		out := baseResult(s, t, r, "owner-supplied-offline")
		gradeAnswer(&out, s, t, answer, r)
		out.Trace = append([]Event(nil), r.events...)
		return out, nil
	}
	return Result{}, fmt.Errorf("unknown case %q", caseID)
}
