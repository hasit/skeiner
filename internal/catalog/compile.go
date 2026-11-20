package catalog

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const identifierPattern = `[A-Za-z0-9_.-]+`

var literal = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
var identifier = regexp.MustCompile(`^` + identifierPattern + `$`)
var pathParameterSegment = regexp.MustCompile(`^\{(` + identifierPattern + `)\}(\.[A-Za-z0-9]+)?$`)

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max] + "…"
}

func (d document) compile(path, method, id string, item, raw obj, explicitlySelected bool) (*Operation, error) {
	if raw == nil {
		return nil, fmt.Errorf("expected operation object")
	}
	for _, field := range []string{"servers", "requestBody", "callbacks", "$ref"} {
		if _, ok := raw[field]; ok {
			return nil, fmt.Errorf("%s on operation unsupported", field)
		}
	}
	if _, ok := item["servers"]; ok {
		return nil, fmt.Errorf("path-level servers unsupported")
	}
	if len(path) > 1024 || !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("invalid or oversized path")
	}
	placeholders := map[string]bool{}
	if path != "/" {
		for _, seg := range strings.Split(path[1:], "/") {
			if parameter := pathParameterSegment.FindStringSubmatch(seg); parameter != nil {
				if parameter[2] != "" {
					if method != "get" {
						return nil, fmt.Errorf("path parameter with a file extension only supported for GET")
					}
					if !explicitlySelected {
						return nil, fmt.Errorf("select this GET with --allow or --allow-get to use its file extension")
					}
				}
				placeholders[parameter[1]] = true
			} else if !literal.MatchString(seg) || seg == "." || seg == ".." {
				return nil, fmt.Errorf("unsupported path segment %q", seg)
			}
		}
	}
	if method == "head" || method == "trace" || method == "query" {
		return nil, fmt.Errorf("method %s unsupported", method)
	}
	if id == "" || len(id) > 256 || strings.TrimSpace(id) != id {
		return nil, fmt.Errorf("invalid or oversized operationId")
	}
	op := &Operation{
		ID: id, Method: strings.ToUpper(method), Path: path,
		Summary: str(raw["summary"]), Description: str(raw["description"]),
		Parameters: []Parameter{},
	}
	needsBearer, err := d.security(raw)
	if err != nil {
		return nil, err
	}
	op.NeedsBearer = needsBearer
	if tags, ok := raw["tags"].([]any); ok {
		for _, t := range tags {
			op.Tags = append(op.Tags, str(t))
		}
	}
	params := map[string]Parameter{}
	for _, src := range []obj{item, raw} {
		value, has := src["parameters"]
		if !has {
			continue
		}
		arr, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("expected parameter array")
		}
		seen := map[string]bool{}
		for _, e := range arr {
			p, err := d.parameter(e, method, explicitlySelected)
			if err != nil {
				return nil, err
			}
			key := p.In + "\x00" + p.Name
			if seen[key] {
				return nil, fmt.Errorf("duplicate %s parameter %q", p.In, p.Name)
			}
			seen[key] = true
			params[key] = p
		}
	}
	if len(params) > 32 {
		return nil, fmt.Errorf("more than 32 parameters unsupported")
	}
	for _, p := range params {
		if p.In == "header" {
			op.CredentialHeaders = append(op.CredentialHeaders, p.Name)
			continue
		}
		if p.In == "path" {
			if !placeholders[p.Name] {
				return nil, fmt.Errorf("path parameter %q absent from template", p.Name)
			}
			delete(placeholders, p.Name)
		}
		op.Parameters = append(op.Parameters, p)
	}
	if len(placeholders) > 0 {
		return nil, fmt.Errorf("missing required path parameter declaration")
	}
	sort.Strings(op.CredentialHeaders)
	sort.Slice(op.Parameters, func(i, j int) bool {
		a, b := op.Parameters[i], op.Parameters[j]
		if a.In != b.In {
			return a.In < b.In
		}
		return a.Name < b.Name
	})
	// Scaling read coverage must not silently admit previously excluded writes.
	readSchema31 := method == "get" && !strings.HasPrefix(str(d.root["openapi"]), "3.0.")
	response, err := d.response(raw["responses"], readSchema31)
	if err != nil {
		return nil, err
	}
	op.Response = response
	return op, nil
}

func (d document) security(operation obj) (bool, error) {
	value, defined := operation["security"]
	if !defined {
		value, defined = d.root["security"]
	}
	if !defined {
		return false, nil
	}
	rules, ok := value.([]any)
	if !ok {
		return false, fmt.Errorf("security must be a list")
	}
	if len(rules) == 0 {
		return false, nil
	}
	schemes := asObj(asObj(d.root["components"])["securitySchemes"])
	bearer := false
	for _, rule := range rules {
		set := asObj(rule)
		if set == nil {
			return false, fmt.Errorf("invalid security requirement")
		}
		if len(set) == 0 {
			return false, nil
		}
		if len(set) != 1 {
			continue
		}
		for name, requirement := range set {
			roles, ok := requirement.([]any)
			if !ok || len(roles) != 0 {
				continue
			}
			scheme, err := d.resolve(schemes[name])
			if err != nil {
				continue
			}
			if str(scheme["type"]) == "http" && strings.EqualFold(str(scheme["scheme"]), "bearer") {
				bearer = true
			}
		}
	}
	if !bearer {
		return false, fmt.Errorf("security requirement needs an unsupported scheme (configure anonymous or HTTP bearer access)")
	}
	return true, nil
}

func (d document) parameter(raw any, method string, explicitlySelected bool) (Parameter, error) {
	o, err := d.resolve(raw)
	if err != nil {
		return Parameter{}, err
	}
	p := Parameter{Name: str(o["name"]), In: str(o["in"]), Description: clip(str(o["description"]), 256)}
	if len(p.Name) > 128 || !identifier.MatchString(p.Name) {
		return p, fmt.Errorf("invalid or unsupported parameter name %q", p.Name)
	}
	expectedStyle := "form"
	switch p.In {
	case "path":
		expectedStyle = "simple"
	case "header":
		name, ok := CredentialHeaderName(p.Name)
		if !ok || method != "get" {
			return p, fmt.Errorf("%q parameter %q unsupported", p.In, p.Name)
		}
		if !explicitlySelected {
			return p, fmt.Errorf("credential header %q requires explicit GET selection with --allow or --allow-get", name)
		}
		p.Name = name
		expectedStyle = "simple"
	case "query":
	default:
		return p, fmt.Errorf("%q parameter %q unsupported", p.In, p.Name)
	}
	if r, ok := o["required"]; ok {
		if _, valid := r.(bool); !valid {
			return p, fmt.Errorf("invalid required on parameter %q", p.Name)
		}
		p.Required = r == true
	}
	if p.In == "path" && !p.Required {
		return p, fmt.Errorf("path parameter %q must be required", p.Name)
	}
	if p.In == "header" && !p.Required {
		return p, fmt.Errorf("optional credential header %q unsupported", p.Name)
	}
	if s, ok := o["style"]; ok && s != expectedStyle {
		return p, fmt.Errorf("unsupported parameter style for %q", p.Name)
	}
	if explode, ok := o["explode"]; p.In == "header" && ok {
		if _, valid := explode.(bool); !valid {
			return p, fmt.Errorf("invalid explode on credential header %q", p.Name)
		}
	}
	for _, key := range []string{"allowReserved", "allowEmptyValue"} {
		if v, ok := o[key]; ok && v != false {
			return p, fmt.Errorf("%s unsupported for %q", key, p.Name)
		}
	}
	if _, ok := o["content"]; ok {
		return p, fmt.Errorf("content-based parameter %q unsupported", p.Name)
	}
	schema, err := d.resolve(o["schema"])
	if err != nil {
		return p, fmt.Errorf("parameter %q: %w", p.Name, err)
	}
	p.Type = str(schema["type"])
	if p.In == "header" && p.Type != "string" {
		return p, fmt.Errorf("credential header %q requires a string schema", p.Name)
	}
	switch p.Type {
	case "string", "number", "integer", "boolean":
	default:
		return p, fmt.Errorf("scalar string, number, integer or boolean schema required for %q", p.Name)
	}
	for _, k := range []string{"oneOf", "anyOf", "allOf", "not", "if", "then", "else", "contentMediaType", "contentEncoding"} {
		if _, ok := schema[k]; ok {
			return p, fmt.Errorf("schema keyword %s unsupported for parameter %q", k, p.Name)
		}
	}
	if choices, ok := schema["enum"].([]any); ok && p.In != "header" {
		p.Enum, p.EnumTruncated = describeEnum(p.Type, false, choices, maxEnumBytes)
	}
	return p, nil
}

const maxEnumBytes = 1024
const maxResponseEnumBytes = 4096 // Shared by all success response shapes of one operation.

// Expose whole, usable JSON literals in declaration order within the discovery
// budget. Mark omissions so callers can distinguish a partial list.
func describeEnum(typ string, nullable bool, choices []any, maxBytes int) (json.RawMessage, bool) {
	if maxBytes < 2 {
		return nil, true
	}
	values := []byte{'['}
	truncated := false
	for _, choice := range choices {
		switch v := choice.(type) {
		case nil:
			if !nullable && typ != "null" {
				continue
			}
		case string:
			if typ != "string" {
				continue
			}
			if len(v) > maxBytes {
				return append(values, ']'), true
			}
		case bool:
			if typ != "boolean" {
				continue
			}
		case float64:
			if typ != "number" && typ != "integer" || typ == "integer" && math.Trunc(v) != v {
				continue
			}
			// The decoder and the JavaScript guest both use binary64 numbers.
			// Never present a potentially rounded integer as an exact choice.
			if math.Abs(v) >= 1<<53 {
				truncated = true
				continue
			}
		default:
			continue
		}
		literal, _ := json.Marshal(choice)     // The decoder only produces finite JSON scalars.
		size := len(values) + len(literal) + 1 // Include the closing bracket.
		if len(values) > 1 {
			size++ // Include the separator after the previous choice.
		}
		if size > maxBytes {
			truncated = true
			break
		}
		if len(values) > 1 {
			values = append(values, ',')
		}
		values = append(values, literal...)
	}
	return append(values, ']'), truncated
}

type shapeBudget struct {
	nodes     int
	enumBytes int
}

var successCode = regexp.MustCompile(`^2([0-9][0-9]|XX)$`)

func (d document) response(raw any, readSchema31 bool) (obj, error) {
	responses := asObj(raw)
	shapes := obj{}
	budget := shapeBudget{enumBytes: maxResponseEnumBytes}
	for _, code := range keys(responses) {
		if !successCode.MatchString(code) {
			continue
		}
		if len(shapes) >= 8 {
			return nil, fmt.Errorf("more than eight success responses unsupported")
		}
		response, err := d.resolve(responses[code])
		if err != nil {
			return nil, fmt.Errorf("response %s: %w", code, err)
		}
		content := asObj(response["content"])
		media := asObj(content["application/json"])
		if media == nil || len(content) != 1 {
			return nil, fmt.Errorf("response %s needs application/json as its single content type", code)
		}
		budget.nodes = 64
		shape, err := d.shape(media["schema"], 0, &budget, readSchema31)
		if err != nil {
			return nil, fmt.Errorf("response %s: %w", code, err)
		}
		shapes[code] = shape
	}
	if len(shapes) == 0 {
		return nil, fmt.Errorf("missing documented JSON success response")
	}
	return shapes, nil
}

// shape returns a compact schema, with truncation visible to the client.
func (d document) shape(raw any, depth int, budget *shapeBudget, readSchema31 bool) (obj, error) {
	if depth >= 6 || budget.nodes <= 0 {
		return obj{"truncated": true}, nil
	}
	budget.nodes--
	s, err := d.resolve(raw)
	if err != nil {
		return nil, err
	}
	if readSchema31 && len(s) == 0 {
		// The empty JSON Schema accepts any value. There are no fields to describe.
		return obj{"incomplete": true}, nil
	}
	for _, k := range []string{"oneOf", "allOf", "anyOf", "$dynamicRef", "not", "prefixItems", "if", "patternProperties", "propertyNames", "unevaluatedProperties"} {
		if _, ok := s[k]; ok {
			return nil, fmt.Errorf("response schema keyword %s unsupported", k)
		}
	}
	t := str(s["type"])
	nullable := s["nullable"] == true
	if types, ok := s["type"].([]any); ok && readSchema31 {
		switch {
		case len(types) == 1:
			t = str(types[0])
		case len(types) == 2 && types[0] == "null":
			t, nullable = str(types[1]), true
		case len(types) == 2 && types[1] == "null":
			t, nullable = str(types[0]), true
		default:
			return nil, fmt.Errorf("response schema needs one type with optional null")
		}
		if t == "" || (len(types) == 2 && t == "null") {
			return nil, fmt.Errorf("invalid response schema type list")
		}
	}
	if t == "" {
		if s["properties"] != nil {
			t = "object"
		} else {
			return nil, fmt.Errorf("response schema needs a single type")
		}
	}
	out := obj{"type": t}
	if nullable {
		out["nullable"] = true
	}
	switch t {
	case "object":
		props := asObj(s["properties"])
		fields := obj{}
		for i, k := range keys(props) {
			if i >= 16 || budget.nodes <= 0 || len(k) > 128 {
				out["truncated"] = true
				break
			}
			fields[k], err = d.shape(props[k], depth+1, budget, readSchema31)
			if err != nil {
				return nil, fmt.Errorf("property %q: %w", k, err)
			}
		}
		out["properties"] = fields
		if extra := asObj(s["additionalProperties"]); extra != nil {
			out["additionalProperties"], err = d.shape(extra, depth+1, budget, readSchema31)
		}
	case "array":
		items, declared := s["items"]
		if !declared && readSchema31 {
			items = obj{} // In JSON Schema, omitted items leaves the elements unconstrained.
		}
		out["items"], err = d.shape(items, depth+1, budget, readSchema31)
	case "string", "number", "integer", "boolean", "null":
		if choices, ok := s["enum"].([]any); ok {
			values, truncated := describeEnum(t, nullable, choices, min(maxEnumBytes, budget.enumBytes))
			if values != nil {
				out["enum"] = values
				budget.enumBytes -= len(values)
			}
			if truncated {
				out["enumTruncated"] = true
			}
		}
	default:
		return nil, fmt.Errorf("unsupported response schema type %q", t)
	}
	return out, err
}
