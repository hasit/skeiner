# Writing a Skeiner program

An `execute` call takes a JavaScript **function body**. Use an explicit `return` to send a JSON value to the client. For new programs, choose `mode: "async"` and `await api.call(...)`. This body uses the bundled CI API to retrieve a run and select the failed job names.

```js
const run = await api.call("getRun", {path: {runId: "814"}});
return {
  commit: run.sha,
  failedJobs: run.jobs.filter(job => job.status === "failed").map(job => job.name)
};
```

Save the body as `failed-jobs.js`. Start `skeiner fixture` in one terminal and run the file from the repository root in another.

```sh
skeiner run --spec examples/ci/openapi.yaml --allow getRun \
  --program failed-jobs.js --mode async
```

On the built-in fixture this returns `{"commit":"abc123","failedJobs":["unit-linux"]}`. Rerun the same file after the upstream changes to compute a new answer. For MCP, send the file contents as `code` and choose the same mode. [CI specification](../examples/ci/openapi.yaml)

## Execution modes

| Mode | Behavior of `api.call` |
| --- | --- |
| `async` | Returns a promise that fulfills with parsed JSON or rejects with a host error. The program can await it or return the promise directly. |
| `sync` | Returns parsed JSON immediately or throws. |
| Omitted | The runtime first compiles a synchronous function. A syntax error during compilation triggers the async parser. The compiled program executes once. |

The host dispatches calls sequentially inside one execution in either mode. Omit the arguments for an operation without inputs. For others, use the `path` and `query` objects described by search. Return finite, acyclic JSON. Resolve nested promises before returning an array or object. Use string IDs for values outside JavaScript's safe integer range.

Each execution starts fresh. Combine related reads in one body when the first response supplies an ID or selection rule for the next. Keep just the fields needed for the answer and any source record IDs needed to verify it. A reusable example compares blended and provider-specific exchange rates. [Saved rates program](../examples/rates/compare.js)

## Paginating a GET

`api.reducePages` is an optional helper for cursor-based GETs. Specify the API's cursor parameter and how to extract the next cursor from its actual response. The reducer processes each page immediately. This body reads the lines of a failed CI attempt and keeps only matching records.

```js
return await api.reducePages("getAttemptLogs", {
  args: {path: {attemptId: "att-814-unit-1"}},
  cursorParam: "cursor",
  initial: [],
  next: page => page.nextCursor,
  reduce: (matches, page) => matches.concat(
    page.lines.filter(line => line.text.startsWith("FAIL "))
      .map(line => ({id: line.id, text: line.text}))
  )
});
```

The CI fixture yields one matching line.

```json
{
  "state": [{"id": "814-U1-L003", "text": "FAIL TestInvoiceRounding"}],
  "complete": true,
  "nextCursor": null
}
```

The first request omits the cursor unless you specify `cursor`. Return the next opaque string or safe integer from `next`, and return `null` at the end. Fixed inputs go in `args`. Page reads and ordinary calls share the execution's 16-call budget.

If calls run out, the helper returns `complete: false` with the accumulated `state` and the **next unread cursor**. Start a fresh execution with the same operation, filters and callbacks, passing the prior `state` as `initial` and the prior `nextCursor` as `cursor`. Keep each checkpoint under 16 KiB. An invalid cursor, a failed request or an oversized checkpoint fails the execution. Cursor-cycle detection covers one execution. Across executions, the client owns the stopping condition, and the provider determines whether a cursor sees a stable snapshot.

For early exits or other paging conventions, read pages with `api.call`. The read-only property `api.remainingCalls` reports the remaining budget so a program can return partial evidence before it reaches the limit.

## Resource limits

| Resource | Limit |
| --- | ---: |
| Program source | 32 KiB |
| JavaScript heap / WebAssembly linear memory per instance | 16 MiB / 32 MiB |
| Concurrent active executions per engine | 4 |
| Wait for an execution slot | 5 seconds |
| Cumulative guest time after admission | 5 seconds |
| Overall time, including admission and HTTP | 30 seconds or earlier caller deadline |
| Calls per execution, including failed attempts at dispatch | 16 |
| HTTP arguments per call / upstream response body | 32 KiB / 256 KiB |
| Guest-to-host frame / host reply into guest | 64 KiB / 1 MiB |
| Final JSON value | 16 KiB |

The guest timer counts wall time spent in guest execution, including instance setup. It pauses during host dispatch. The overall deadline continues during HTTP and the caller can cancel an in-flight request. Host errors retain a machine-readable code in the MCP error response. For example, request a smaller upstream page after `response_truncated`, and return fewer fields after `output_limit`. The Go host closes the guest when execution finishes. [Engine](../internal/sandbox/engine.go) · [Guest API](../internal/sandbox/guest.js)
