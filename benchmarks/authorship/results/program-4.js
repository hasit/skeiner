async function getOverdueLoans() {
  const branch = api.input.branch;
  const asOf = api.input.asOf;

  // Step 1: Get all open loans for the branch
  let loans = [];
  let cursor = undefined;
  do {
    const response = await api.call("listLoans", {query: {branch, cursor}});
    loans = loans.concat(response.loans.filter(loan => loan.status === "open"));
    cursor = response.nextCursor || undefined;
  } while (cursor);

  // Step 2: Get all renewals for the branch
  let renewals = [];
  let rCursor = undefined;
  do {
    const rResponse = await api.call("listRenewals", {query: {branch, cursor: rCursor}});
    renewals = renewals.concat(rResponse.renewals);
    rCursor = rResponse.nextCursor || undefined;
  } while (rCursor);

  // Step 3: Calculate effective due date for each loan
  const overdueLoans = [];
  for (const loan of loans) {
    // Filter renewals for this loan that are approved
    const validRenewals = renewals.filter(r => r.loanId === loan.loanId && r.decision === "approved");

    // Find the latest renewal date
    const latestRenewalDate = validRenewals.reduce((latest, r) => {
      const rDate = new Date(r.through);
      return rDate > latest ? rDate : latest;
    }, new Date(loan.dueDate));

    const effectiveDueDate = latestRenewalDate.toISOString().slice(0, 10);

    // Check if overdue as of the given date (due on asOf is on time)
    if (effectiveDueDate < asOf) {
      overdueLoans.push({loan: loan.loanId, object: loan.objectId, due: effectiveDueDate});
    }
  }

  // Sort by loan ID ascending
  overdueLoans.sort((a, b) => a.loan.localeCompare(b.loan));

  return overdueLoans;
}

return await getOverdueLoans();