# JavaScript setup benchmark dependencies

Both JavaScript arms start with installed libraries and an empty integration directory. Run these commands from the repository root.

```sh
npm ci --prefix benchmarks/setup/js_toolkit --ignore-scripts --no-audit --no-fund
npm ci --prefix benchmarks/setup/js_toolkit/custom --ignore-scripts --no-audit --no-fund
python3 benchmarks/discovery/client_install.py
```

The package gateway uses Node. Its locked dependencies require Node 20 or newer. The custom worker runs in Deno 2.9.8. The npm lockfiles record the versions and integrity hashes for each arm.

## Packaged gateway

The packaged arm uses `@ivotoby/openapi-mcp-server` 1.18.0 (MIT). Its stdio executable accepts `--openapi-spec <file>` and `--api-base-url <url>`. Set `--tools explicit` and repeat `--tool <name-or-id>` to publish selected operations. Tool names derived from operation IDs use kebab case. The package also accepts method-and-path IDs such as `GET::users`. The default mode loads all operations.

Clients discover operations through MCP `tools/list`. Each named tool includes its input schema. The host can supply API headers through the `API_HEADERS` environment variable, formatted as comma-separated `Header:Value` pairs. An integration that uses a bearer token must form the header value, including the `Bearer ` prefix, in its host configuration. [Upstream usage and configuration](https://github.com/ivo-toby/mcp-openapi-server/blob/d1a006eee9fa7dcdae288e0e56820f175a6290d1/README.md)

## Custom worker

The benchmark evaluates the submitted `worker.js` as an async JavaScript function body. It has these ordinary library bindings.

| Bindings | Package |
| --- | --- |
| `McpServer`, `Server`, `StdioServerTransport`, `ListToolsRequestSchema`, `CallToolRequestSchema` | MCP TypeScript SDK 1.32.2 |
| `parseYaml` | `yaml` 2.9.1 (`parse`) |
| `z` | Zod 4.6.7 |

The author can use `Deno.env.get(name)`, `Deno.readTextFile(path)`, standard JavaScript, and `fetch`. The worker gets only the `SPEC`, `BASE_URL`, and `TOKEN` environment values supplied in its launch configuration. Its network access is scoped to the fixture origin. The author writes the spec interpretation, MCP tool registration, schema generation, dispatch, request construction, and host authentication. [MCP SDK](https://github.com/modelcontextprotocol/typescript-sdk/tree/3b73021a0a2272c0c04c3917bcdb03398b3ec63f) · [YAML](https://github.com/eemeli/yaml/tree/1440ecd3d1bff41e4ac399f8f6839e810328bd16) · [Deno permissions](https://docs.deno.com/runtime/reference/permissions/)
