// Package service binds catalog discovery and JavaScript execution to one authorized API.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/hasit/skeiner/internal/catalog"
	"github.com/hasit/skeiner/internal/dispatch"
	"github.com/hasit/skeiner/internal/sandbox"
)

// Exchange is an owner-side observation. Body has already passed the dispatcher’s
// credential scrubber. The normal MCP server never retains it.
type Exchange struct {
	Operation string
	Args      json.RawMessage
	Status    int
	Duration  time.Duration
	Code      string
	Body      json.RawMessage
}

type Service struct {
	catalog *catalog.Catalog
	engine  *sandbox.Engine
	client  *dispatch.Client
	observe func(Exchange)
}

func New(c *catalog.Catalog, e *sandbox.Engine, d *dispatch.Client, observe func(Exchange)) *Service {
	return &Service{catalog: c, engine: e, client: d, observe: observe}
}

func (s *Service) Search(query string) (json.RawMessage, error) {
	if len(query) > 1024 || strings.TrimSpace(query) == "" {
		return nil, &dispatch.Error{Code: "invalid_arguments", Message: "query must be 1 to 1024 bytes of search terms"}
	}
	return json.Marshal(s.catalog.Search(query))
}

func (s *Service) Execute(ctx context.Context, code string) (json.RawMessage, error) {
	return s.ExecuteMode(ctx, code, sandbox.ModeAuto, nil)
}

func (s *Service) ExecuteMode(ctx context.Context, code string, mode sandbox.Mode, input json.RawMessage) (json.RawMessage, error) {
	if strings.TrimSpace(code) == "" {
		return nil, &dispatch.Error{Code: "invalid_arguments", Message: "provide a JavaScript function body with a return statement"}
	}
	return s.engine.RunModeWithPages(ctx, code, mode, input, s.observed(s.client.CallWithStatus), s.observed(s.client.ReadPageWithStatus))
}

func (s *Service) observed(call func(context.Context, string, json.RawMessage) (json.RawMessage, int, error)) sandbox.Callback {
	return func(ctx context.Context, id string, args json.RawMessage) (json.RawMessage, error) {
		start := time.Now()
		body, status, err := call(ctx, id, args)
		if s.observe != nil {
			// Unknown names are guest input, never put arbitrary strings in the trace.
			name := "unavailable"
			if _, ok := s.catalog.Lookup(id); ok {
				name = id
			}
			var code string
			if err != nil {
				code = PublicError(err).Code
			}
			s.observe(Exchange{Operation: name, Args: append(json.RawMessage(nil), args...), Status: status, Duration: time.Since(start), Code: code, Body: body})
		}
		return body, err
	}
}

// PublicError deliberately excludes raw transport errors, request bodies and guest stack traces.
func PublicError(err error) *dispatch.Error {
	var dispatchErr *dispatch.Error
	var guestErr *sandbox.Error
	switch {
	case errors.As(err, &dispatchErr):
		return dispatchErr
	case errors.Is(err, context.DeadlineExceeded):
		return &dispatch.Error{Code: "timeout", Message: "execution deadline expired; narrow the page or reduce the work"}
	case errors.Is(err, context.Canceled):
		return &dispatch.Error{Code: "canceled", Message: "execution canceled"}
	case errors.As(err, &guestErr):
		return &dispatch.Error{Code: guestErr.Code, Message: guestErr.Message}
	default:
		return &dispatch.Error{Code: "runtime_error", Message: "execution failed inside the host"}
	}
}
