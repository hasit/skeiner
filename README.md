# Skeiner

[![M8ven Score](https://m8ven.ai/badge/mcp/hasit-skeiner-k9ycvw?v=cb04a7cc9b5c00418c3cf0081fc30e50)](https://m8ven.ai/mcp/hasit-skeiner-k9ycvw?s=readme)

Skeiner is an MCP server for OpenAPI. An agent uses `search` to find operations and `execute` to call them from JavaScript. Save a working program and rerun it from the CLI.

Compare Skeiner with JavaScript on three measured workflows. [Permissioned API setup](docs/setup-benchmark.md) · [GitHub release audit](docs/discovery-benchmark.md) · [Historical weather](docs/weather-benchmark.md)

## Run the offline quickstart

Extract a macOS or Linux release and run the bundled example with Python 3. It reads four rates from a local snapshot and returns the currency whose ECB rate exceeded the blended rate. [Download Skeiner](https://github.com/hasit/skeiner/releases/tag/v0.1.0-alpha.2)

```sh
python3 examples/rates/quickstart.py
```

```json
[{"date":"2024-05-15","quote":"USD","blended":1.0824,"ecb":1.0832}]
```

From a source checkout, build the CLI first with `go build -o ./skeiner ./cmd/skeiner` (Go 1.25+), then run the same example.

## Connect your API

Configure your MCP client to launch this command over stdio. Use absolute paths and replace the example URL and operation IDs with yours.

```sh
/absolute/path/to/skeiner serve --spec /absolute/path/to/openapi.yaml \
  --base-url https://api.example.com --allow listIncidents,getIncidentEvents
```

[OpenAPI and authentication](docs/openapi.md) · [Writing programs](docs/sandbox.md) · [Architecture](docs/architecture.md)
