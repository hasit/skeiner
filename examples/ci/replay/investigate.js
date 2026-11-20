// input contains only IDs parsed from the prompt and operations found by search.
const run = api.call(input.runOp, {path: {runId: input.runID}});
if (run.sha !== input.sha) throw new Error("The requested SHA does not match the run");
const summary = ["Run " + run.id + " at commit " + run.sha + " ultimately " + run.status + "."];
const citations = [];
let testsNeverStarted = false;
for (const job of run.jobs) {
  if (run.status === "failed" && job.status !== "failed") continue;
  const log = api.call(input.attemptsOp, {path: {jobId: job.id}});
  for (const attempt of log.attempts.sort((a, b) => a.number - b.number)) {
    summary.push(job.name + " attempt " + attempt.number + " " + attempt.status + ".");
    let cursor = null;
    do {
      const page = api.call(input.logsOp, {
        path: {attemptId: attempt.id}, query: cursor ? {cursor: cursor} : {}
      });
      for (const line of page.lines) {
        if (/\b(fail(ed|ure)?|expected|error|pass(ed)?)\b|never started/i.test(line.text)) {
          citations.push({id: line.id, text: line.text});
          summary.push(line.text + ".");
        }
        if (/test process never started/i.test(line.text)) testsNeverStarted = true;
      }
      cursor = page.nextCursor;
    } while (cursor);
  }
}
if (testsNeverStarted) summary.push("No application code change is justified by this run.");
return {summary: summary.join(" "), citations: citations};
