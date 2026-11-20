// Return the required checks that failed on a pull request's reviewed head.
const scan = await api.reducePages("listPullRequests", {
  cursorParam: "cursor",
  initial: [],
  next: page => page.nextCursor,
  reduce(matches, page) {
    for (const pr of page.items) {
      if (pr.review.head !== pr.head) continue;
      for (const check of pr.checks) {
        const required = pr.requiredChecks.some(
          rule => rule.name === check.name && rule.issuer === check.issuer
        );
        if (required && check.head === pr.head && check.status === "failed") {
          matches.push({pr: pr.number, review: pr.review.id, run: check.id});
        }
      }
    }
    return matches;
  }
});
if (!scan.complete) throw new Error("The fixture should fit in one execution");
return scan.state;
