// Find all open museum loans at the given branch, and for each, determine if overdue as of asOf date, considering renewals.
async function findOverdueLoans(api) {
  const branch = api.input.branch;
  const asOf = api.input.asOf;
  // 1. Gather all open loans at the branch
  let loans = [];
  let cursor = undefined;
  do {
    const resp = await api.call("listLoans", { query: { branch, cursor } });
    loans = loans.concat(resp.loans.filter(l => l.status === "open"));
    cursor = resp.nextCursor;
  } while (cursor);

  // 2. Gather all renewals at the branch
  let renewals = [];
  cursor = undefined;
  do {
    const resp = await api.call("listRenewals", { query: { branch, cursor } });
    renewals = renewals.concat(resp.renewals);
    cursor = resp.nextCursor;
  } while (cursor);

  // 3. Map loanId to latest approved renewal date (if any)
  const renewalMap = {};
  for (const r of renewals) {
    if (r.decision === "approved") {
      if (!renewalMap[r.loanId] || renewalMap[r.loanId] < r.through) {
        renewalMap[r.loanId] = r.through;
      }
    }
  }

  // 4. For each open loan, determine effective due date (max of dueDate and latest approved renewal)
  const overdue = [];
  for (const l of loans) {
    let due = l.dueDate;
    if (renewalMap[l.loanId] && renewalMap[l.loanId] > due) {
      due = renewalMap[l.loanId];
    }
    if (due < asOf) {
      overdue.push({ loan: l.loanId, object: l.objectId, due });
    }
  }

  // 5. Sort by loan ID ascending
  overdue.sort((a, b) => a.loan.localeCompare(b.loan));
  return overdue;
}
return await findOverdueLoans(api);