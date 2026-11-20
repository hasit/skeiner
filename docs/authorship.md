# Authoring and rerunning an investigation

A museum lending register exposes paginated loans and renewal decisions. Which open loans are overdue at a given branch on a given date? The answer must include each loan ID, object ID, and effective due date in loan-ID order. The task requires joining both collections, ignoring withdrawn renewals, and handling loans due on the cutoff date.

Three fresh authors using discovery with response enums each wrote a program that answered the original question and both changed snapshots correctly. Before that change, both GPT-4.1 mini authors produced a plausible empty list. Their programs checked `Open` and `Approved`; the provider returns `open` and `approved`. The OpenAPI spec contained these exact values throughout. The previous search result described the fields only as strings. [Contract](../benchmarks/authorship/openapi.yaml) · [Archived programs and session measurements](../benchmarks/authorship/results)

## Results

Every direct answer gets a fresh conversation. An author gets one conversation on the original snapshot. The owner captures the last program actually sent to MCP `execute`, including unsuccessful programs, and re-executes the identical source with the CLI on all three snapshots. The model receives only the task, the caller input and its tools. The local grader compares the full JSON value with hand-curated answers.

| Interface | Model | Fresh conversations | Exact conversational answers | Exact saved-program replays |
| --- | --- | ---: | ---: | ---: |
| Direct named reads | GPT-4.1 mini | 6 | 0/6 | — |
| Direct named reads | GPT-4.1 | 3 | 0/3 | — |
| Skeiner, before response enums | GPT-4.1 mini | 2 | 0/2 | 0/6 |
| Skeiner, before response enums | GPT-4.1 | 1 | 1/1 | 3/3 |
| Skeiner, with response enums | GPT-4.1 mini | 2 | 2/2 | 6/6 |
| Skeiner, with response enums | GPT-4.1 | 1 | 1/1 | 3/3 |

All nine direct conversations read every page. Three returned the complete set of correct records and dates in the wrong order. Four returned valid JSON with incorrect membership. Two mini conversations returned text that the JSON parser rejected; the original harness retained the parse-failure flag only, so their underlying facts are ungraded. The two successful mini authors in the final configuration each tried two unsuccessful tool calls before discovery. Those attempts are included in the measurements.

The updated museum snapshot marks one previously overdue item as returned, grants another a renewal, introduces a new loan and changes the cutoff. The third case selects a different branch and an earlier cutoff. The archived programs receive these changes through `api.input`. A hand-authored Python client in the harness also passes the three cases in five, five and four HTTP reads. A verified direct client likewise needs zero model tokens per rerun. [Fixtures and expected answers](../benchmarks/authorship/fixture.py) · [Harness and direct client](../benchmarks/authorship/study.py) · [Per-program replay results](../benchmarks/authorship/results/replays.json)

## What authoring costs

Each row gives the cost of the first answer and the full three-snapshot sequence. The direct estimate covers three fresh model conversations per trial. The Skeiner estimate covers the complete initial authoring conversation. Its three CLI verification and replay calls consume no model tokens. Estimates use the provider's reported cached input, uncached input and output, at standard per-token list prices. Charges for local compute, data transfer and upstream reads are outside the estimate. [GPT-4.1 mini pricing](https://developers.openai.com/api/docs/models/gpt-4.1-mini) · [GPT-4.1 pricing](https://developers.openai.com/api/docs/models/gpt-4.1)

| Model | Configuration | Trial | First answer, USD | All three, USD | Total input tokens (cached) | Total output tokens |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| Mini | Direct | 1 | 0.00164 | 0.00486 | 8,540 (0) | 904 |
| Mini | Direct | 2 | 0.00130 | 0.00420 | 7,461 (0) | 760 |
| Mini | Author, before | 1 | 0.00464 | 0.00464 | 8,755 (7,424) | 2,101 |
| Mini | Author, before | 2 | 0.00564 | 0.00564 | 11,163 (8,832) | 2,388 |
| Mini | Author, with enums | 4 | 0.00417 | 0.00417 | 7,647 (5,504) | 1,728 |
| Mini | Author, with enums | 5 | 0.00438 | 0.00438 | 8,235 (6,912) | 1,976 |
| GPT-4.1 | Direct | 3 | 0.00610 | 0.01739 | 6,405 (0) | 573 |
| GPT-4.1 | Author, before | 3 | 0.01904 | 0.01904 | 4,634 (0) | 1,221 |
| GPT-4.1 | Author, with enums | 6 | 0.01023 | 0.01023 | 2,668 (0) | 612 |

Each author made five HTTP reads. Independently re-executing the captured source to verify the first answer cost five more. The two changed inputs cost five and four reads, for **19 backend reads per fully verified sequence**, compared with **14** for three direct answers. Each CLI invocation starts a separate process. The receipts include elapsed times, which depend on local startup, model latency, and API prompt caching.

This study covers one synthetic task and two model snapshots. The initial two mini trials per configuration expose a concrete discovery failure, and the single GPT-4.1 trial per configuration checks sensitivity to a stronger model. Both stages used the same prompt, tools, fixtures, temperature and turn limit. Independent owner-side verification is part of the cost. Direct answers had zero exact passes in this sample, so it supplies no cost crossover at equal answer quality. Testing adoption by human API owners requires separate evidence.

## Reproduce

From the repository root, build the binary and replay every captured program. The default command checks each program against its archived outcome, including the two known failures. Use `--require-correct` to treat every incorrect program as an error. Replay requires Python 3.9 or later and a loopback interface.

```sh
go build -o ./skeiner ./cmd/skeiner
python3 benchmarks/authorship/study.py --binary ./skeiner replay
```

For a new paid experiment, supply `OPENAI_API_KEY` in the caller's environment and choose a fresh output directory. This runs one author conversation on the first snapshot and one direct conversation on each of the three. Each conversation is limited to ten model turns and 1,200 output tokens per turn. The historical model snapshots are `gpt-4.1-mini-2025-04-14` and `gpt-4.1-2025-04-14`; availability depends on the provider. The direct tool descriptions include the operation's output fields and enum choices. Skeiner retrieves those details through its production MCP search tool.

```sh
python3 benchmarks/authorship/study.py --binary ./skeiner measure \
  --arm author --trial 1 --out ./bin/museum-study
python3 benchmarks/authorship/study.py measure \
  --arm direct --trial 1 --out ./bin/museum-study
python3 benchmarks/authorship/study.py --binary ./skeiner replay --dir ./bin/museum-study
```
