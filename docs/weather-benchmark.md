# Can a saved program find hot spells more reliably?

We asked which consecutive stretch of days met a temperature threshold. Three independently authored Skeiner programs each got all ten cities right. Fresh model answers got six cities right in each repetition. Including initial authorship, Skeiner's model cost averaged 34% less over ten cities. A client-side JavaScript tool matched its accuracy at 37% of its model cost. [Historical Weather API](https://open-meteo.com/en/docs/historical-weather-api) · [Model receipts and programs](../benchmarks/weather/results)

## Results

| Approach | First 3 cities, exact | Mean cost for 3 | All 10 cities, exact | Mean cost for 10 |
| --- | ---: | ---: | ---: | ---: |
| Fresh model with a daily weather tool | 6/9 | $0.001544 | 18/30 | $0.005542 |
| Skeiner, author once and replay | 9/9 | $0.003644 | 30/30 | $0.003644 |
| JavaScript over host-fetched daily highs, author once and replay | 9/9 | $0.001358 | 30/30 | $0.001358 |

Phoenix's high reached 40 °C every day from July 25 through August 20, a **27-day spell** with a peak of 47.8 °C. The direct model reported 16 days once and 48 days twice. It correctly identified Paris's eight-day spell and London's empty result every time. All nine direct conversations requested explicit dates alongside the temperatures even though the tool offered a shorter values-only series. All three Skeiner authors needed to correct at least one unsuccessful execution. [Frozen data and independent SQL oracle](../benchmarks/weather/dataset.py) · [Data attribution](../benchmarks/weather/fixtures/README.md)

Skeiner cost 2.36× as much as fresh answers across the first three cities. That result suggested a crossover around eight cities. We then fixed seven more cities and new dates before collecting their data or asking for answers. All six saved programs ran unchanged. On the seven new cities, each Skeiner and client-side program got 7/7. The direct model got 4/7 in each repetition. Its errors included Madrid, where it picked the wrong spell, and Sydney, where the winning spell crossed from January into February. [Follow-up design and case order](https://github.com/hasit/skeiner/commit/a575fb2) · [Follow-up measurements](../benchmarks/weather/extension-results)

Model spend for fresh answers first exceeded Skeiner's authoring cost at city seven **on average** in the specified order. Skeiner was cheaper over ten cities in two of three individual repetitions. Each verification or reuse of either saved JavaScript program used one archive read and zero model tokens. Counting the extra read during authorship, each code approach made 33 reads across the three repetitions. Fresh answers made 30. All model cost estimates include failed calls and discounts for cached input. [Cost calculation](../benchmarks/weather/report.py) · [GPT-4.1 mini standard prices](https://developers.openai.com/api/docs/models/gpt-4.1-mini)

The model conversations used frozen responses captured from Open-Meteo so every arm saw the same daily highs. We then ran the first enrolled Skeiner program and a handwritten Python client directly against the live archive. Both reproduced all three answers. Each needed one archive GET per city. Python took 0.68–0.70 s per city and a fresh Skeiner CLI process took 0.92–0.94 s in this spot check. [Live measurements](../benchmarks/weather/live-results.json) · [Live harness](../benchmarks/weather/live.py)

## Could an existing weather MCP do this?

Yes. The hosted community Open-Meteo MCP v0.6.1 returned all 276 dates and daily highs exactly as captured from REST. A scripted client computed all three answers from one tool call per city. The server also supports DuckDB SQL when a result is large enough to stage. We requested hourly temperature alongside the daily maximum to get a queryable table. Each city then required two MCP calls and staged 2,300 rows. A handwritten SQL query returned all three correct answers. [Server implementation](https://github.com/cyanheads/open-meteo-mcp-server/blob/v0.6.1/src/mcp-server/tools/spill-utils.ts) · [Live MCP measurements](../benchmarks/weather/mcp_reference_results.json)

We also asked the same model to write the SQL in three fresh conversations. The host staged the data, supplied the schema and bound the table name and threshold. The saved queries got **5/9** answers right at a mean model cost of $0.001806 per three-city sequence. Two authors combined separate hot spells and reported 71 days in Phoenix. The third query passed all three cities. All three queries hardcoded the original summer dates. A first-case acceptance check would have rejected the first two. This supplementary experiment used the live community server and a host that kept the staging preview out of the model's context. [SQL authorship harness](../benchmarks/weather/sql_study.py) · [Saved SQL results](../benchmarks/weather/results/community-sql-replays.json)

## Study design

We chose three northern-hemisphere summers before model sampling. All cover June 1 through August 31, 2020. We use ERA5 reanalysis, each city's local time zone and temperatures in °C. These are estimates for a weather model grid cell.

| City | Time zone | Daily high threshold | Role |
| --- | --- | ---: | --- |
| Phoenix | America/Phoenix | 40 °C | Author and verify the first program |
| Paris | Europe/Paris | 30 °C | Reuse with new inputs |
| London | Europe/London | 35 °C | Reuse with new inputs |

Return the start, end, length and peak of the longest qualifying spell within the requested dates. An equal high qualifies, a missing high breaks a spell, and the earliest start wins ties. An empty result has zero days and null dates and peak. Grade the four values against an independent calculation.

We ran three repetitions with GPT-4.1 mini (`gpt-4.1-mini-2025-04-14`), temperature 0.2, up to six turns and 1,200 output tokens per turn. In each repetition, the direct agent got a daily weather tool and answered once per city. A Skeiner author got the Phoenix task with access to OpenAPI discovery and execution. A second author got the same task with a JavaScript tool that receives the projected daily series from its host. It used Skeiner's embedded JavaScript runtime with API operations disabled to isolate the value of reusable computation. The two code paths fetched the same data. [Harness and prompts](../benchmarks/weather/study.py) · [Study design fixed before model sampling](https://github.com/hasit/skeiner/commit/92f2a87)

For each author we saved the last executed program and verified it on Phoenix. The nine further cities were unseen during authoring. Three independent ten-city repetitions make up the denominator of 30. Median elapsed time for the first three cities was 4.63 s for fresh answers, 12.38 s for Skeiner and 6.05 s for client-side JavaScript. These times include first-case verification and both new-city runs for the authors, with cold starts against the frozen weather endpoint. Three conversations ran concurrently. Engineering effort to integrate the tools or build the verifier, infrastructure and API fees are outside the estimates. [Primary replay observations](../benchmarks/weather/results/replays.json) · [Follow-up replay observations](../benchmarks/weather/extension-results/replays.json)

## Reproduce

Build from the repository root, then revalidate the archived results and recalculate the table offline.

```sh
go build -o ./skeiner ./cmd/skeiner
python3 benchmarks/weather/study.py replay
python3 benchmarks/weather/study.py extension-replay
python3 benchmarks/weather/sql_study.py verify
python3 benchmarks/weather/report.py --extension benchmarks/weather/extension-results
```

Run the live archive comparison with `python3 benchmarks/weather/live.py` or the community server comparison with `python3 benchmarks/weather/mcp_reference.py`. To run the original 15 paid model conversations, set `OPENAI_API_KEY` and use an empty output directory.

```sh
python3 benchmarks/weather/study.py measure --out /tmp/weather-trials
python3 benchmarks/weather/report.py --dir /tmp/weather-trials
```

Use `python3 benchmarks/weather/study.py extension-measure --out /tmp/weather-extension` for 21 new direct conversations and replays of the six original programs. Use `python3 benchmarks/weather/sql_study.py measure --out /tmp/weather-sql` for three new paid SQL authors on the community server.
