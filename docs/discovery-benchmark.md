# Can Skeiner beat client-side JavaScript on a GitHub release audit?

Three GPT-6.2 Astra authors per approach independently discovered the endpoints, wrote JavaScript, and fetched the data. Both approaches produced **12 of 12 correct, evidence-backed audits**. Client-side JavaScript used about 5% less model spend. With both runtimes on the same host, warm replay was effectively tied on live GitHub. [Design recorded before model calls](../benchmarks/discovery/design.md) · [Programs and provider usage](../benchmarks/discovery/results)

A later test used a real remote client with **both workers beside the gateway**. Both got all 96 audits right. Deno finished first in 64 of 96 matched pairs and was 13.8 ms faster at the paired median. [Remote comparison](#real-remote-client-equal-worker-placement)

## The task

Given only a repository and a release tag, identify the commit SHA and subject, and report whether the commit and each annotated tag on the way to it contain a signature. This takes two dependent reads for a lightweight tag and three for the signed annotated tags in this sample. Native Git fetched each tag and inspected its objects independently. This check establishes signature presence. Signer trust would require separate verification. [GitHub reference API](https://docs.github.com/en/rest/git/refs#get-a-reference) · [Annotated tags](https://docs.github.com/en/rest/git/tags#get-a-tag) · [Cases and oracle](../benchmarks/discovery/fixtures/README.md)

Each author received the same prompt and could search the same ten documented GitHub operations. Skeiner programs called the discovered operation IDs. Deno programs could use native fetch or a GET-to-JSON helper, follow returned URLs, and keep intermediate responses in JavaScript. Authors started with Git v2.47.0. Their last executed sources were frozen and rerun on Git, Go, GitHub CLI and curl. Those releases cover both annotated and lightweight tags on signed and unsigned commits. None needed more than one annotated tag lookup. [Catalog](../benchmarks/discovery/openapi.yaml) · [Prompts and grading](../benchmarks/discovery/study.py)

## Authorship and reuse

| Metric | Skeiner | Client-side JavaScript |
| --- | ---: | ---: |
| Correct audits with all required records retrieved | 12/12 | 12/12 |
| Mean model cost to author and produce four audits | $0.05218 | $0.04970 |
| Median time for the authoring conversation | 9.41 s | 9.44 s |
| Searches / executions per author | 1 / 1 | 1 / 1 |
| HTTP reads to replay the four releases per program | 10 | 10 |
| Median cold replay against local captures | 215 ms | 31 ms |

All three authors per approach succeeded on their first execution. Each used three model turns and three initial HTTP reads. The four verification and reuse runs required no additional model calls. Skeiner's execute tool description and schema added 108 input tokens on the first turn. Across the nine turns in each arm Skeiner used 965 more input tokens and seven fewer output tokens, which accounts for the price gap. Tool replies sent to the model totaled exactly 16,443 UTF-8 bytes per arm. The client shared Skeiner's catalog search implementation, so building or acquiring equivalent discovery tooling is outside the cost comparison. [Archived runs](../benchmarks/discovery/results/replays.json) · [Cost calculation](../benchmarks/discovery/report.py)

GPT-4.1 mini was an inexpensive fixed model for the earlier weather study. Here we chose GPT-6.2 Astra for its coding and tool-use capabilities. We used the Responses API, medium reasoning, standard service, fresh conversations and no prompt-cache reads or writes. The estimates use the provider's reported input and output tokens at $8 and $40 per million respectively. These results cover one model and workflow. [OpenAI coding model guidance](https://developers.openai.com/api/docs/guides/code-generation) · [Model and pricing](https://developers.openai.com/api/docs/models/gpt-6.2-astra)

## Warm replay against live GitHub

Each machine ran 36 matched pairs, alternating arm order across three rounds, three author pairs and four releases. Each pair shared a credential gateway and a warm upstream connection pool. The gateway forwarded raw live GitHub responses. Authoring used projections of previously captured responses to keep the data identical across conversations. Every live answer matched native Git and every program made the required reads. [Gateway and timer](../benchmarks/discovery/live.py) · [Linux workflow](https://github.com/hasit/skeiner/actions/runs/44865951723)

| Machine | Pairs where Skeiner finished first | Median paired difference, Skeiner minus client |
| --- | ---: | ---: |
| macOS arm64 | 15/36 | +2.59 ms |
| GitHub-hosted Linux x86-64 | 18/36 | −0.49 ms |

Both arms made 90 live reads on each machine. The paired differences changed direction across hosts and are small compared with a GitHub request. [macOS observations](../benchmarks/discovery/results/live/macos.json) · [Linux observations](../benchmarks/discovery/results/live/linux.json)

## When execution runs beside a distant gateway

We repeated the same 36 macOS pairs against live GitHub with **an emulated 50 ms client-to-gateway round trip**. Each client-side GET crossed that link. Skeiner paid the same delay once to dispatch the program, then made its two or three dependent GETs from the gateway's side. Skeiner finished first in **34/36 pairs**, by **85 ms at the paired median**. Both arms still made 90 GitHub reads and returned 36 correct answers. [Placement experiment](../benchmarks/discovery/placement.md) · [All paired observations](../benchmarks/discovery/results/live/macos-link50.json)

This result measures the benefit of executing near a required gateway. The remote-client experiment below puts both workers beside it. To reproduce the delay experiment, pass `--gateway-rtt-ms 50` to the live harness.

## Real remote client, equal worker placement

A macOS client dispatched one saved audit per HTTPS request through a temporary Cloudflare tunnel to a GitHub-hosted Linux machine. Both persistent workers ran there beside the same live GitHub gateway. The remote client specified the author, release and worker. Each worker followed the required Git objects itself and returned the same JSON answer format. Neither arm had an artificial delay. Each session ran the same 48 adjacent, order-balanced pairs. [Experiment design](../benchmarks/discovery/remote-design.md) · [Gateway host](../benchmarks/discovery/remote_host.py) · [Client and verifier](../benchmarks/discovery/remote_client.py)

| Session | Skeiner client median | Deno client median | Median paired gap at client¹ | Median paired gap at worker¹ | Pairs Deno won at client |
| --- | ---: | ---: | ---: | ---: | ---: |
| First | 580.9 ms | 581.0 ms | +8.7 ms | +7.7 ms | 29/48 |
| Repeat on a fresh runner | 565.9 ms | 518.2 ms | +28.9 ms | +22.9 ms | 35/48 |
| Both sessions | 574.0 ms | 531.7 ms | **+13.8 ms** | **+13.2 ms** | **64/96** |

¹ We subtract Deno's duration from Skeiner's for each matched pair, then take the median of those differences. Positive values favor Deno. Each worker timer covers dispatch, program execution and live GitHub reads. The client timer also includes the HTTPS request and complete response. The setup request opened the connection before timing. Every measured audit reused it. [First session receipts](../benchmarks/discovery/results/remote/session-1) · [Second session receipts](../benchmarks/discovery/results/remote/session-2)

Across both sessions, **each worker made 240 measured upstream GETs and returned 96/96 correct answers**. Every pair had identical request paths, statuses, upstream body sizes and final answer bytes. Once both workers could follow the dependent reads at the gateway, Skeiner showed no accuracy, request-count or end-to-end speed advantage on this workload. The measurements include upstream latency, so they cannot attribute the entire gap to either JavaScript engine.

The first session's separate shutdown request failed after all 96 timed requests had been recorded at both ends. We kept the original receipts. For the repeat, the host closed itself after flushing the final measured response. The second workflow completed successfully. [First workflow](https://github.com/hasit/skeiner/actions/runs/44868080481) · [Repeat workflow](https://github.com/hasit/skeiner/actions/runs/44868328865)

## Reproduce

From the repository root on macOS or Linux, use Go, Python 3 and the included installer for Deno 2.9.8. Only the dependency and runtime installations need network access for the archived replay.

```sh
go build -trimpath -o ./skeiner ./cmd/skeiner
python3 benchmarks/discovery/client_install.py
python3 benchmarks/discovery/dataset.py verify
python3 benchmarks/discovery/study.py replay
python3 benchmarks/discovery/report.py benchmarks/discovery/results/live/*.json
python3 benchmarks/discovery/remote_client.py published
```

Run `python3 benchmarks/discovery/live.py` with a GitHub token or an authenticated `gh` session for new live measurements. Run `python3 benchmarks/discovery/dataset.py verify-git` to refetch the upstream Git objects. To enroll six new paid authors, set `OPENAI_API_KEY` and run `python3 benchmarks/discovery/study.py measure --out /tmp/release-authors` with an empty output directory.

To run the real remote comparison, use a recent authenticated GitHub CLI with workflow dispatch access to the repository. The following command starts a temporary Actions host, runs the 96 requests from your machine, downloads both receipts and verifies them. It reuses the published programs and incurs no model calls.

```sh
python3 benchmarks/discovery/remote_run.py --out /tmp/skeiner-remote-audit
```
