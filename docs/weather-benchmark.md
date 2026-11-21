# Can a saved program find hot spells more reliably?

Open-Meteo can return one daily maximum per date. The question is which consecutive stretch of days met a temperature threshold. This is a test of a recurring calculation over public data from a real API. [Historical Weather API](https://open-meteo.com/en/docs/historical-weather-api)

## Study design

We chose three northern-hemisphere summers before model sampling. All cover June 1 through August 31, 2020. We use ERA5 reanalysis, each city's local time zone and temperatures in °C. These are estimates for a weather model grid cell.

| City | Time zone | Daily high threshold | Role |
| --- | --- | ---: | --- |
| Phoenix | America/Phoenix | 40 °C | Author and verify the first program |
| Paris | Europe/Paris | 30 °C | Reuse with new inputs |
| London | Europe/London | 35 °C | Reuse with new inputs |

Return the start, end, length and peak of the longest qualifying spell within the requested dates. An equal high qualifies, a missing high breaks a spell, and the earliest start wins ties. An empty result has zero days and null dates and peak. Grade the four values against an independent calculation.

We enroll three independent repetitions using the same model snapshot. In each repetition, the direct agent gets a compact list of daily highs and answers once per city. A Skeiner author gets the Phoenix task and access to OpenAPI discovery and execution. A second author gets the same task with a general JavaScript tool that receives the daily series from its host. We save each author's last program and grade its unchanged source on all three cities. We also run a handwritten API client.

The primary comparison is factual accuracy and model spend over all three answers, including first-time authorship. Verification and replay use executable code. We report failed attempts, timing and actual tokens. Engineering effort to integrate the tools or design the verifier is outside the dollar estimate. A win against a model reading rows supports reuse for this task. A separate advantage over a host that already supports reusable code would require evidence from that comparison.
