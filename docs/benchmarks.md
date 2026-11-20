# Benchmarks

Two local workloads measure different costs. The inventory replay measures how much data reaches a caller that already has a verified program. The runtime benchmark measures how long Skeiner spends indexing, searching and running a program over a loopback API. Both run with Go and the repository fixtures. Only the initial Go dependency download requires network access.

Build the CLI from the repository root before running either workload.

```sh
CGO_ENABLED=0 go build -trimpath -o ./skeiner ./cmd/skeiner
```

## How much reaches the caller?

Find every pull request whose current head has both a review and a failed required check. A check must match the required name and issuer, and the result must include the original review and check-run IDs.

The fixture has 54 pull requests in six pages. It includes stale reviews, checks from previous commits, checks from another issuer, optional checks, and passing checks. The page-at-a-time client retrieves each page through MCP and filters it in Go. The saved program applies the same selection in JavaScript inside a single MCP execution. Both use the same production server, operation, HTTP fixture and result format. The benchmark checks the ordered evidence against an independent answer and verifies the sequence of pages at the HTTP server. [Saved program](../benchmarks/inventory/blocked.js) · [OpenAPI contract](../benchmarks/inventory/openapi.yaml) · [Fixture and verifier](../cmd/skeiner-bench/inventory.go)

```sh
go run ./cmd/skeiner-bench -binary ./skeiner -inventory
```

| Snapshot | Approach | Matches | HTTP reads | MCP execute calls | JSON reply bytes |
| --- | --- | ---: | ---: | ---: | ---: |
| Original | Page at a time | 8 | 6 | 6 | 18,277 |
| Original | Saved program | 8 | 6 | 1 | 366 |
| Updated | Page at a time | 8 | 6 | 6 | 18,279 |
| Updated | Saved program | 8 | 6 | 1 | 368 |

The updated snapshot gives PR 2 a current review, repairs the check on PR 7, moves PR 37 to a new head, and gives PR 54 a failed rerun. The same program correctly replaces PRs 7 and 37 with PRs 2 and 54 and cites the rerun ID. The amount of JSON delivered to the caller falls by **98.0%** on both snapshots. Backend work stays at six reads each.

Reply bytes sum the UTF-8 lengths of the JSON values in MCP text content. The count excludes request code, JSON-RPC framing and discovery. Timing, model tokens, authoring cost and agent success rates require separate experiments. The byte counts are deterministic for this fixture.

## How much time does the runtime add?

```sh
go run ./cmd/skeiner-bench -binary ./skeiner \
  -n 100 -cold 10 -operations 1000 -parallel 4
```

One local run on an Apple M5 Pro with Go 1.27.4 on darwin/arm64, 15 logical CPUs and GOMAXPROCS=15 produced these nearest-rank percentiles. Cold samples start a fresh server process. Each search and execution series has one untimed warm-up.

| Measurement | Samples | p50 (ms) | p95 (ms) |
| --- | ---: | ---: | ---: |
| Cold process through MCP tools/list | 10 | 297.539 | 334.229 |
| Read and index the five-operation YAML fixture | 100 | 0.403 | 0.909 |
| Search the five-operation catalog | 100 | 0.008 | 0.039 |
| Search a generated 1,000-operation catalog | 100 | 0.335 | 0.493 |
| Execute three dependent HTTP reads in JavaScript | 100 | 4.242 | 5.332 |
| Execute the same three reads in Go | 100 | 0.264 | 0.367 |

Cold startup includes loading the 5,275-byte fixture, compiling QuickJS/Wasm, initializing MCP and listing tools over stdio. OS file caches can be warm. Teardown falls outside the timer. Indexing reads and parses the YAML on each sample. Warm search measures lexical ranking and JSON serialization inside the service. The generated catalog is 256,855 bytes.

Warm execution creates a fresh guest, fetches a run, follows its job ID to an attempt, then follows the attempt ID to its log. Both execution paths reuse the same Go dispatcher and HTTP client, parse all three responses, and verify the same two log IDs on every sample. The JavaScript median was about 4 ms above the direct Go median on this host. These handler timings exclude MCP transit and model or remote-network latency. [Runtime harness](../cmd/skeiner-bench/main.go) · [CI fixture contract](../examples/ci/openapi.yaml)

A final concurrency check holds each execution at its first request until all four have reached the loopback API, then verifies all four answers. On Linux the harness also prints the server's lifetime peak resident memory from `/proc/<pid>/status`, including startup. The macOS sample verified concurrent execution and has no RSS value.
