// Package transport exposes the skeiner tools on any MCP SDK transport.
package transport

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hasit/skeiner/internal/dispatch"
	"github.com/hasit/skeiner/internal/sandbox"
	"github.com/hasit/skeiner/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var Implementation = &mcp.Implementation{Name: "skeiner", Version: "0.1.0-alpha.1"}

func reply(value json.RawMessage, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		value, _ = json.Marshal(service.PublicError(err))
	}
	return &mcp.CallToolResult{IsError: err != nil, Content: []mcp.Content{&mcp.TextContent{Text: string(value)}}}, nil
}

func addTool(server *mcp.Server, name, description, required string, properties map[string]any, fn func(context.Context, map[string]*string) (json.RawMessage, error)) {
	tool := &mcp.Tool{Name: name, Description: description, InputSchema: map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             []string{required},
		"additionalProperties": false,
	}}
	server.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Keep SDK validation diagnostics from echoing user arguments to callers.
		var args map[string]*string
		if json.Unmarshal(req.Params.Arguments, &args) != nil || args[required] == nil {
			return reply(nil, &dispatch.Error{Code: "invalid_arguments", Message: "expected a string field named " + required})
		}
		for key, value := range args {
			if _, ok := properties[key]; !ok || value == nil {
				return reply(nil, &dispatch.Error{Code: "invalid_arguments", Message: "use only the string fields listed in the tool schema"})
			}
		}
		return reply(fn(ctx, args))
	})
}

func New(s *service.Service) *mcp.Server {
	server := mcp.NewServer(Implementation, nil)
	textField := map[string]string{"type": "string"}
	addTool(server, "search", "Discover approved operations by keyword. Results describe inputs and response JSON. Use execute to retrieve data.", "query",
		map[string]any{"query": textField}, func(_ context.Context, args map[string]*string) (json.RawMessage, error) {
			return s.Search(*args["query"])
		})
	description := fmt.Sprintf("Run a JavaScript function body and return JSON. For new code, set mode to async and await api.call. A complete body is `return await api.call(\"listIncidents\");` if search lists that operation. Pass inputs as {path:{...}, query:{...}} and omit unused groups. With mode omitted, ordinary bodies use synchronous calls and bare top-level await api.call(...) also works. "+
		"Each execution has %d calls and a %d KiB final JSON limit. Combine related reads and stop when you have sufficient evidence. As you scan each page, compute requested counts, discard nonmatching rows, and keep only the requested fields and original source IDs for matches. "+
		"For unfinished pagination, check api.remainingCalls before each call. At zero, return {complete:false,evidence,nextCursor} with source IDs and the next unread cursor. Resume from that cursor in a fresh execute with the same operation and filters.", sandbox.MaxCalls, sandbox.MaxOutputBytes/1024)
	addTool(server, "execute", description, "code",
		map[string]any{
			"code": textField,
			"mode": map[string]any{"type": "string", "enum": []sandbox.Mode{sandbox.ModeSync, sandbox.ModeAsync}},
		},
		func(ctx context.Context, args map[string]*string) (json.RawMessage, error) {
			mode := sandbox.ModeAuto
			if value := args["mode"]; value != nil {
				mode = sandbox.Mode(*value)
				if mode != sandbox.ModeSync && mode != sandbox.ModeAsync {
					return nil, &dispatch.Error{Code: "invalid_arguments", Message: "mode must be sync or async"}
				}
			}
			return s.ExecuteMode(ctx, *args["code"], mode)
		})
	return server
}
