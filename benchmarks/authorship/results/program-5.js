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

  // Step 3: Group renewals by loanId and filter approved only
  const renewalsByLoan = {};
  for (const renewal of renewals) {
    if (renewal.decision === "approved") {
      if (!renewalsByLoan[renewal.loanId]) {
        renewalsByLoan[renewal.loanId] = [];
      }
      renewalsByLoan[renewal.loanId].push(renewal);
    }
  }

  // Step 4: Determine effective due date for each loan and check overdue
  const asOfDate = new Date(asOf);
  const overdueLoans = [];

  for (const loan of loans) {
    const loanRenewals = renewalsByLoan[loan.loanId] || [];
    let latestRenewalDate = null;
    for (const renewal of loanRenewals) {
      const renewalDate = new Date(renewal.through);
      if (!latestRenewalDate || renewalDate > latestRenewalDate) {
        latestRenewalDate = renewalDate;
      }
    }

    const contractualDueDate = new Date(loan.dueDate);
    const effectiveDueDate = latestRenewalDate && latestRenewalDate > contractualDueDate ? latestRenewalDate : contractualDueDate;

    if (effectiveDueDate < asOfDate) {
      overdueLoans.push({loan: loan.loanId, object: loan.objectId, due: effectiveDueDate.toISOString().slice(0, 10)});
    }
  }

  overdueLoans.sort((a, b) => a.loan.localeCompare(b.loan));
  return overdueLoans;
}

return await getOverdueLoans();