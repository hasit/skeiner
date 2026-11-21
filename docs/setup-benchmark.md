# How much work does a new API integration take?

Skeiner and a packaged JavaScript OpenAPI server both exposed two approved 1Password Connect operations with a launch configuration and zero worker code. A custom JavaScript worker needed 30–34 nonblank lines even with an MCP SDK and YAML parser supplied. For basic discovery, bearer authentication and operation permissions, choosing a ready-made adapter accounted for most of the setup savings.

## From a spec to a working integration

The owner wants to list vault metadata by name and use the resulting UUID to fetch one vault. The starting point was the provider's complete 38,164-byte OpenAPI file. A loopback service returned synthetic records and required a fresh bearer token. The client discovered the approved inputs over MCP, performed both reads, then tried an unapproved activity read and a delete. The checker verified that only the two authorized requests reached the service. On restart it changed the host, credential and records, added a new optional query parameter to a temporary copy of the spec, and checked that clients could discover and use it. [Provider spec](https://github.com/1Password/connect/blob/420a61d55b8744b3732afa65f340ab5629526190/docs/openapi/spec.yaml) · [All 26 checks](../benchmarks/setup/contract.md)

| Starting point | First submission passed | Median time to pass | Mean model cost per author | Launch config | Worker code |
| --- | ---: | ---: | ---: | ---: | ---: |
| Skeiner 0.1.0-alpha.2 | 3/3 | 5.3 s | $0.030 | 251 bytes | 0 |
| JavaScript OpenAPI MCP server 1.18.0 | 3/3 | 5.6 s | $0.031 | 299 bytes | 0 |
| Custom JavaScript worker | 3/3 | 14.6 s | $0.065 | 158 bytes | 30–34 lines, 2,733–3,062 bytes |

Each author started from an empty integration directory, a shared operator brief, the spec and a short reference for their installed tool or libraries. We ran three rounds, with one independent GPT-6.2 Astra conversation per approach in each round. All nine submissions passed all 26 checks again in fresh processes. The packaged JavaScript server finished ahead of Skeiner in two of the three rounds. The subsecond difference between their medians provides little basis to choose between them. [Experiment design](../benchmarks/setup/design.md) · [Exact prompts](../benchmarks/setup/study.py) · [Submissions and receipts](../benchmarks/setup/results)

The two launch configurations express essentially the same owner decisions. Skeiner takes the allowed operation IDs and the name of the host token variable. The JavaScript package takes two allowed tool names in its explicit mode and a host-provided `Authorization` header. Both get the spec path and base URL from the launcher. The custom worker also owns input discovery, permission checks, URL and header construction, and MCP dispatch. Code sizes exclude supplied libraries. [Skeiner configuration](../benchmarks/setup/results/skeiner-1/launch.json) · [Package configuration](../benchmarks/setup/results/package-1/launch.json) · [Example custom worker](../benchmarks/setup/results/custom-1/worker.js)

## What can the client discover before its first read?

The original score required clients to discover operations and inputs. We then added a task that matters when an agent writes code against an unfamiliar API. Can it tell which fields the response contains before fetching any records?

The secondary check starts each frozen integration twice. The first discovery must describe the list as an array of vault objects and the detail as one vault object, with all nine typed fields and four possible values for `type`. The second discovery must also include an optional boolean property added to the source schema under a fresh name. Both checks observe the fixture to confirm discovery made zero HTTP requests. [Follow-up design](../benchmarks/setup/output-design.md) · [Discovery checker](../benchmarks/setup/output_discovery.py)

All three original Skeiner configurations passed. None of the three packaged JavaScript configurations or original custom workers published those fields. The package's explicit mode supplied the input names, types, an example filter and a vault ID pattern. Its dynamic mode can look up raw responses, but on this spec they refer back to the source file for the vault fields. The package also supports author-supplied MCP resources. This experiment used the published explicit allowlist configuration. [Captured MCP records](../benchmarks/setup/fixtures/discovery.json) · [Package documentation](https://github.com/ivo-toby/mcp-openapi-server/blob/d1a006eee9fa7dcdae288e0e56820f175a6290d1/README.md)

We gave each working custom worker to a fresh author with the additional requirement. All three authors resolved the response schema from the spec and published it in the tool metadata. Each revised worker passed all 42 checks on its first submission. The revisions added **13–17 nonblank lines** (830–850 bytes) and took a median **18.6 seconds** beyond the original setup, at a mean model cost of $0.107. Skeiner passed the same discovery requirement with its original configuration. We did not time adding an extension to the packaged JavaScript server. [Paired measurements](../benchmarks/setup/output-results/metrics.json) · [Revised workers and replay](../benchmarks/setup/output-results)

## Scope and reproducibility

The timer includes spec inspection, model calls and black-box checks. The checker searches each operation ID on Skeiner and lists named tools on the JavaScript workers. Model usage comes from the API receipts at medium reasoning and standard service, with zero cache reads and writes. Costs use $8 per million input tokens and $40 per million output tokens. Each setup study measures one model's work after tool selection. Human onboarding, production rollout and obtaining a real vault credential fall outside the timer. [Model prices](https://developers.openai.com/api/docs/models/gpt-6.2-astra) · [Cost calculation](../benchmarks/setup/report.py)

Compatibility matters before timing starts. With a bearer configured, Skeiner accepted **6 of the 15** operations in the complete Connect spec. We chose two of those six. During selection a Sentry candidate failed preflight on a trailing slash in its path. Operators can run `skeiner preflight` to check the routes they need. [Source selection and preflight commands](../benchmarks/setup/contract.md#source-selection-and-coverage)

Installation was timed separately on macOS arm64 with an empty install directory and download cache per trial. Median of three runs was 2.46 s for the Skeiner release, 1.86 s for the JavaScript package and 1.42 s to install and load the custom worker's libraries. Node, npm and Deno were already available. These numbers include network variation and exclude the time to install system runtimes. [Install measurements](../benchmarks/setup/install-results.json) · [Installer](../benchmarks/setup/install.py)

To replay, use Go, Python 3, Node 20+ and npm. These commands download the pinned spec, Deno and JavaScript dependencies. Replays run against a local service using generated credentials and make no model calls.

```sh
go build -o ./skeiner ./cmd/skeiner
python3 benchmarks/setup/spec.py
python3 benchmarks/discovery/client_install.py
npm ci --prefix benchmarks/setup/js_toolkit --ignore-scripts --no-audit --no-fund
npm ci --prefix benchmarks/setup/js_toolkit/custom --ignore-scripts --no-audit --no-fund
python3 benchmarks/setup/study.py replay --binary ./skeiner
python3 benchmarks/setup/report.py
python3 benchmarks/setup/output_study.py replay --binary ./skeiner
python3 benchmarks/setup/output_study.py report
```
