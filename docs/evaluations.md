# Evaluating investigations

An evaluation runs a task against a known API state, records which upstream responses the program obtained, and grades the answer against the API owner's rubric. Use it to distinguish a program that returns a plausible answer from one that can cite the records behind it.

## Run a deterministic replay

Run the three included CI cases from the repository root. The replay driver uses a checked-in program and a local HTTP fixture, with a fresh snapshot per case. No model account is needed.

```sh
skeiner eval --spec examples/ci/openapi.yaml --suite evals/ci.yaml
```

The JSON report includes each answer, checked facts and citations, upstream call statuses, and elapsed time. Inspect `results[].checks` for individual failures and `results[].trace` for tool and upstream activity. The trace omits intermediate HTTP payloads. The command exits nonzero when any case fails. [CI replay driver](../examples/ci/replay) · [Replay cases](../evals/ci.yaml)

To see a failing control, keep the original assertion rubric and drop its decisive records from the source.

```sh
skeiner eval --spec examples/ci/openapi.yaml --suite evals/ci.yaml \
  --case assertion --state missing-assertion
```

The missing page removes the assertion and its expected and actual values. The grader reports missing evidence. Repeating the command without the state override restores the original snapshot.

## Grade facts and citations

To validate your own saved JavaScript, pass it to `eval --program`. Skeiner executes the same file once per case over MCP and observes its actual upstream reads. Each case can supply a public JSON `input`, available as `api.input` in the program and included in task exports. Keep the fixture state, expected facts and required evidence in the owner-side rubric.

```sh
skeiner eval --spec examples/ci/openapi.yaml --allow getAttemptLogs \
  --suite evals/log-scan.yaml --program examples/ci/find-lines.js
```

The same file finds a failure on an early page, a passing line in a different attempt, and zero matches after a page is removed. To exercise a regression, add `--case early-failure --state missing-assertion`. That run must fail because the original rubric requires a record the API no longer returns. For the version 2 grader, return a `facts` object and a `citations` array. The example includes the complete request arguments for each cited page. [Saved log investigation](../examples/ci/find-lines.js) · [Cases and expected evidence](../evals/log-scan.yaml)

The replay suite uses the version 1 format. It checks regular expressions against a summary and corroborates cited records in successful responses. For a new investigation, use a version 2 suite to specify typed facts and where the supporting evidence must come from. [Structured CI suite](../evals/ci-structured.yaml)

A version 2 case has two audiences.

| Agent receives | Owner supplies for grading |
| --- | --- |
| Task prompt, public fact field names, scalar types and descriptions, source ID and text field names | Fixture selection, one expected value per fact field, exact record IDs and text, source operation and required resource arguments |

The answer contains `facts` and `citations`. Missing, extra or incorrect facts fail. The grader checks each cited record against a successful exchange with its full operation ID and arguments. For example, a log citation includes the page cursor if the actual call used one. In the rubric the owner can specify just the resource-defining argument, such as the attempt ID, so a correct citation from any page of that attempt can pass. A copied line from a different attempt cannot satisfy that source requirement.

To use the built-in model driver, provide `OPENAI_API_KEY` in the host environment, and set `MODEL` to the model you want to evaluate. This command sends the public task, tool descriptions and model-visible results to OpenAI and incurs model usage.

```sh
skeiner eval --spec examples/ci/openapi.yaml --suite evals/ci-structured.yaml \
  --driver openai --model "$MODEL"
```

The driver interacts through an in-memory MCP session using the same tools as `serve`. The built-in grader labels these receipts `observed-upstream`. A passing score describes the tested facts, quotes and source scope on that snapshot. To check whether a saved investigation still works when the data changes, run it on another snapshot with independently chosen expectations.

For a prepared API, repeat `--state-url NAME=URL` to map suite states to their endpoints. A state URL with a different base origin or path prefix clears the original bearer and header credentials. Use a process configured for the matching base URL when a state needs those credentials.

### Fresh CI diagnosis trials

We ran the assertion and retry cases twice each with GPT-4.1. We then repeated the four conversations with extra instructions to discover parameter types first and retain the page arguments beside each citation. Each conversation started fresh and used the same fixture, tools and grading rules.

| Instructions | All requested facts correct | Facts and citations both passed | HTTP responses received |
| --- | ---: | ---: | ---: |
| Published | 1/4 | 0/4 | 21 |
| Additional discovery and citation guidance | 3/4 | 0/4 | 20 |

In one trial the model returned both correct monetary amounts but attributed a log record to the wrong page. More guidance improved the factual count in this small sample and still produced no fully grounded diagnosis. The published driver keeps the original wording. Use the case-level checks when qualifying a saved program or an agent for unattended use. [Eight graded answers and exact instruction change](../benchmarks/ci-diagnosis/results.json)

## Use an external driver

For a program that returns an ordinary array or object, compare the output of `run` with an independently prepared expected JSON value. For example, after writing `actual.json`, use `jq -e --slurpfile expected expected.json '. == $expected[0]' actual.json`. This checks the returned values and array order. Use the structured suite when you also need to verify which API record supplied a citation. The museum benchmark exercises both known failures and correct programs against changed inputs. [Museum replay and grader](authorship.md)

`tasks` exports the public contract and answer instructions. Give the external driver only the value for its task. Have the owner-side harness record successful upstream exchanges independently and collect the driver's final answer.

```sh
skeiner tasks --suite evals/ci-structured.yaml
skeiner grade --suite evals/ci-structured.yaml --case "$CASE_ID" --input receipt.json
```

Set `CASE_ID` to the case you ran. The receipt contains an `answer` object and an `exchanges` array. Each exchange supplies `operation`, `args`, HTTP `status` and the parsed JSON `body`. Record the full arguments, including any cursor. The offline grader labels its result `owner-supplied-offline` and trusts the harness to have observed those exchanges. [Receipt format and checks](../internal/eval/structured.go)
