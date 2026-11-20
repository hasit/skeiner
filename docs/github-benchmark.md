# Skeiner and GitHub MCP on live Git history

How many commits leading up to Git 2.47.0 have localization or merge subjects? We asked Skeiner and GitHub's official MCP server to read the history of `git/git` at the release commit. Both produced the same counts. For 60 commits they each needed one GitHub page. Skeiner returned a 42-byte answer to the client. GitHub MCP returned 35,905 bytes of projected JSON, or 28,771 bytes with its optional CSV output. [Release commit](https://github.com/git/git/commit/777489f9e09c8d0dd6b12f9d90de6376330577a2) · [Saved investigation](../benchmarks/github/release-mix.js)

## Results

Each cell under **Reply** is the UTF-8 size of the tool-result text delivered to the client. **Arguments** includes the JavaScript source and input each time Skeiner executes. Latency is the median of three live trials on one machine, from the first tool call through parsing and computing the answer. [All 27 measurements](../benchmarks/github/results.json)

| Task | Tool | GitHub commit pages | MCP calls | Arguments (bytes) | Reply (bytes) | Median (ms) |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| Count 60 subjects | GitHub MCP · JSON | 1 | 1 | 119 | 35,905 | 408 |
| | GitHub MCP · CSV | 1 | 1 | 119 | 28,771 | 412 |
| | Skeiner | 1 | 1 | 845 | 42 | 467 |
| Count 200 subjects | GitHub MCP · JSON | 2 | 2 | 240 | 153,184 | 813 |
| | GitHub MCP · CSV | 2 | 2 | 240 | 127,467 | 817 |
| | Skeiner | 4 | 1 | 846 | 43 | 1,569 |
| Last commit touching one file | GitHub MCP · JSON | 1 | 1 | 152 | 52 | 354 |
| | GitHub MCP · CSV | 1 | 1 | 152 | 45 | 369 |
| | Skeiner | 1 | 1 | 243 | 52 | 339 |

For the 200-commit scan, arguments plus reply totaled **889 bytes for Skeiner** and **127,707 bytes for GitHub MCP's CSV mode**. GitHub MCP fetched 100 commits per page. Skeiner fetched 50 because it caps each full upstream response at 256 KiB, and took nearly twice as long in this run. For the one-file lookup, GitHub's native path filter and SHA projection already gave the caller exactly what it needed.

## What was compared

- **GitHub MCP v2.2.0** ran as the official native stdio server in read-only mode. The client requested only `fields: ["commit"]` for the count, the narrowest supported projection containing the message. It requested `fields: ["sha"]` for the lookup. The client computed the counts locally. We ran both stable JSON and the optional `csv_output` feature. [Official tool implementation](https://github.com/github/github-mcp-server/blob/v2.2.0/pkg/github/repositories.go) · [CSV feature](https://github.com/github/github-mcp-server/blob/v2.2.0/docs/insiders-features.md#csv-output-for-list-tools)
- **Skeiner v0.1.0-alpha.2** exposed the same live REST endpoint through a small, hand-written OpenAPI definition for `git/git`. A saved program processed the pages inside one MCP `execute` call. [Benchmark spec](../benchmarks/github/openapi.yaml) · [GitHub REST endpoint](https://docs.github.com/en/rest/commits/commits#list-commits)
- The harness rotated execution order and checked every answer against a separate Git object checkout. Page counts come from the successful pagination loops. Argument bytes use compact JSON serialization. Reply bytes come from the original MCP text blocks. Process startup, tool discovery, JSON-RPC framing and upstream response bytes are outside these totals. This benchmark uses a prewritten program and a scripted client. It measures the payload available to an agent and makes no estimate of model cost. [Harness](../benchmarks/github/benchmark.py)

## Reproduce

From a source checkout on macOS or Linux, use Go 1.25+, Python 3.9+, curl, and a GitHub token able to read public repository contents. The harness uses `GITHUB_TOKEN`, `GH_TOKEN`, `GITHUB_PERSONAL_ACCESS_TOKEN`, or an existing `gh` login. The installer verifies the official release archive's SHA-256. You can also run the **Live GitHub benchmark** workflow and download its JSON artifact. [GitHub Actions](https://github.com/hasit/skeiner/actions/workflows/github-benchmark.yml)

```sh
go build -o ./skeiner ./cmd/skeiner
python3 benchmarks/github/download_server.py
python3 benchmarks/github/benchmark.py > github-results.json
```

To check the answers independently, run the following commands. The counts are **11 localization, 31 merge, 18 other** for 60 commits and **11, 62, 127** for 200. The file lookup returns `9decd56cc9a41773bffa517caaa07db3f78e8646`. Classification uses the exact, case-sensitive subject prefixes `l10n:` and `Merge `.

```sh
git clone --bare --filter=blob:none --single-branch --branch=v2.47.0 --depth=210 \
  https://github.com/git/git git-oracle
rev=777489f9e09c8d0dd6b12f9d90de6376330577a2
for n in 60 200; do
  git -C git-oracle log -n "$n" --format=%s "$rev" |
    awk '/^l10n:/ {l++; next} /^Merge / {m++; next} {o++} END {print l, m, o}'
done
git -C git-oracle log -1 --format=%H "$rev" -- Documentation/git-merge.txt
```
