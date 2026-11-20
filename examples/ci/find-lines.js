// Return matching log records and the exact page request that retrieved each one.
const {attemptId, contains} = api.input;
const operation = "getAttemptLogs";
const citations = [];
let cursor = null;
do {
  const args = {path: {attemptId}};
  if (cursor !== null) args.query = {cursor};
  const page = await api.call(operation, args);
  for (const line of page.lines) {
    if (line.text.includes(contains)) {
      citations.push({source: {operation, args}, id: line.id, text: line.text});
    }
  }
  cursor = page.nextCursor;
} while (cursor !== null);
return {facts: {matches: citations.length}, citations};
