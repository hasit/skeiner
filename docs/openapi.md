# OpenAPI and HTTP

Start with preflight to see what your client can discover and call. Preflight reads local configuration and writes a JSON report with the enabled routes, excluded routes, warnings and any configuration error. It makes no upstream requests.

```sh
skeiner preflight --spec examples/ci/openapi.yaml --allow getRun,getAttemptLogs
```

An unintentionally empty catalog exits with an error. Review the reason beside each excluded operation, for example a missing credential or an unsupported request body. Use the same flags when launching `serve` or `run`.

## Selecting operations

By default, Skeiner exposes supported named operations that the configured credentials permit. This can include operations with side effects. For a read-only integration, select the individual reads that the client needs.

| Option | Behavior |
| --- | --- |
| `--allow getRun,listRuns` | Selects the listed operation IDs. An explicitly empty `--allow ''` selects zero operations when used alone. |
| `--allow-get '/runs/{runId}'` | Selects an exact GET path template. Repeat to add paths. An unnamed GET receives the callable ID `GET /runs/{runId}`. A named GET keeps its declared ID. |
| `--base-url URL` | Sets one absolute HTTP(S) origin and optional canonical path prefix for every request. The default comes from the first root server in the spec. |
| `--token-env NAME` | Reads the bearer token from the named environment variable and attaches it to requests sent to the configured base URL. |
| `--header-env NAME=ENV_NAME` | Binds a supported header parameter to a host environment variable. Repeat for each binding. |

`SKEINER_BASE_URL`, `SKEINER_TOKEN_ENV` and `SKEINER_ALLOW` provide environment defaults. Specifying either selection flag replaces the environment selection. Explicit `--allow` and `--allow-get` selections combine. Each process serves one specification and credential policy. Restart it to apply a new specification or policy.

For example, if `CI_READ_TOKEN` is already set in the host environment, this launches a client-facing server with three operations.

```sh
skeiner serve --spec ./ci.yaml \
  --base-url https://ci.example.org/api \
  --token-env CI_READ_TOKEN \
  --allow getRun,listJobAttempts,getAttemptLogs
```

Use an explicit trusted base URL when passing credentials with a third-party specification. The URL in the spec otherwise determines where Skeiner sends a configured bearer token. Descriptions, parameter names and enum choices become model-visible discovery content.

## Supported subset

| Feature | What the compiler accepts |
| --- | --- |
| Document | One local OpenAPI 3.0.x, 3.1.x or 3.2.x document in YAML or JSON, up to 16 MiB. Local `#/…` JSON Pointer references can resolve path items, parameters, responses and schemas. External references, reference siblings and unresolved chains produce exclusions or warnings. |
| Operations | GET, POST, PUT, PATCH, DELETE and OPTIONS without request bodies. Each callable ID must be unique, nonempty and at most 256 bytes. Operation-specific and path-specific servers are unsupported. A relative or templated root server needs a base URL override. |
| Inputs | Scalar strings, numbers, integers and booleans in `path` or `query`. Simple path and form query serialization. Operation parameters can override path-item parameters. Discovery reports declared enum values. Application constraints such as enum membership and ranges are enforced by the upstream. |
| Paths | Literal segments and whole-segment required placeholders. Explicitly selected GETs can have a single fixed extension, such as `/jobs/{jobId}.json`. Supply the parameter value without the extension. The host rejects empty path values, dot segments and ambiguous expansions. |
| Responses | Up to eight documented success responses, each with `application/json` as its sole media type. Discovery summarizes objects, arrays and primitives per status. Actual JSON passes into the guest, including a top-level array or scalar. Composition such as `oneOf` or `allOf` is outside the supported subset. |
| Authentication | Anonymous and HTTP bearer alternatives. An explicitly selected GET can use required, simple string header parameters named `Api-Key` and `Api-Username`. Bind their values using `--header-env` and supply an explicit HTTPS base URL, or HTTP on loopback. OpenAPI `apiKey` security schemes, OAuth flows and arbitrary guest-supplied headers are unsupported. |

Search returns up to eight enabled operations. For GET response schemas in OpenAPI 3.1 and 3.2, a type such as `["string","null"]` appears as `{"type":"string","nullable":true}`. An unconstrained schema is marked `incomplete`. A `truncated` marker identifies a shape that exceeded the discovery depth, field or size budget. `enumTruncated` identifies omitted choices. Store large integer identifiers as strings in the API contract so they survive the JavaScript boundary exactly.

## HTTP behavior

A program supplies an operation ID plus optional `path` and `query` objects. The host selects the method, encodes the parameters, attaches credentials and sends the request. It refuses redirects and disables environment proxies. A payload-bearing success must yield valid, uncompressed JSON within the 256 KiB body limit. HTTP 204 and 205 produce `null`. An oversized response produces `response_truncated`. Request a smaller page if the API supports one.

Skeiner scrubs recognized credential fields and configured credential values from response data. An object key containing the configured API key or bearer token causes a `sensitive_response` error. Other upstream data remains available to the program. The API and its caller credentials govern which records the client may retrieve. [Dispatch implementation](../internal/dispatch/client.go)
