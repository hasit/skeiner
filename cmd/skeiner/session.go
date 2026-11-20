package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hasit/skeiner/internal/eval"
	"github.com/hasit/skeiner/internal/service"
	"github.com/hasit/skeiner/internal/transport"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type evalSession struct {
	client  *mcp.ClientSession
	server  *mcp.ServerSession
	cleanup func()
}

func openSession(ctx context.Context, s *service.Service, cleanup func()) (*evalSession, error) {
	a, b := mcp.NewInMemoryTransports()
	server, err := transport.New(s).Connect(ctx, a, nil)
	if err != nil {
		return nil, err
	}
	client, err := mcp.NewClient(transport.Implementation, nil).Connect(ctx, b, nil)
	if err != nil {
		server.Close()
		return nil, err
	}
	return &evalSession{client: client, server: server, cleanup: cleanup}, nil
}

func (s *evalSession) Close() {
	_ = s.client.Close()
	_ = s.server.Close()
	s.cleanup()
}

func (s *evalSession) Tools(ctx context.Context) ([]eval.Tool, error) {
	result, err := s.client.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return nil, err
	}
	tools := make([]eval.Tool, 0, len(result.Tools))
	for _, t := range result.Tools {
		schema, err := json.Marshal(t.InputSchema)
		if err != nil {
			return nil, err
		}
		tools = append(tools, eval.Tool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	return tools, nil
}

func (s *evalSession) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	res, err := s.client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("MCP %s failed: %w", name, err)
	}
	if len(res.Content) != 1 {
		return nil, errors.New("unexpected MCP content count")
	}
	part, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		return nil, errors.New("expected JSON in MCP text content")
	}
	if res.IsError {
		return nil, errors.New(part.Text)
	}
	raw := json.RawMessage(part.Text)
	if !json.Valid(raw) {
		return nil, errors.New("MCP tool returned invalid JSON")
	}
	return raw, nil
}
