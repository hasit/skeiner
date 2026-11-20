async function getOverdueLoans() {
  const branch = api.input.branch;
  const asOf = api.input.asOf;

  // Step 1: Get all open loans for the branch
  let loans = [];
  let cursor = undefined;
  do {
    const response = await api.call("listLoans", {query: {branch: branch, cursor: cursor}});
    loans = loans.concat(response.loans.filter(loan => loan.status === "Open"));
    cursor = response.nextCursor || undefined;
  } while (cursor);

  // Step 2: Get all renewals for the branch
  let renewals = [];
  cursor = undefined;
  do {
    const response = await api.call("listRenewals", {query: {branch: branch, cursor: cursor}});
    renewals = renewals.concat(response.renewals.filter(r => r.decision === "Approved"));
    cursor = response.nextCursor || undefined;
  } while (cursor);

  // Group renewals by loanId
  const renewalsByLoan = {};
  for (const renewal of renewals) {
    if (!renewalsByLoan[renewal.loanId]) {
      renewalsByLoan[renewal.loanId] = [];
    }
    renewalsByLoan[renewal.loanId].push(renewal);
  }

  // Step 3: Calculate effective due date for each loan
  const overdueLoans = [];
  for (const loan of loans) {
    const contractualDueDate = new Date(loan.dueDate);
    const approvedRenewals = renewalsByLoan[loan.loanId] || [];
    let maxRenewalDate = contractualDueDate;
    for (const renewal of approvedRenewals) {
      const renewalDate = new Date(renewal.through);
      if (renewalDate > maxRenewalDate) {
        maxRenewalDate = renewalDate;
      }
    }

    // Step 4: Check if effective due date is before asOf date (overdue)
    const asOfDate = new Date(asOf);
    if (maxRenewalDate < asOfDate) {
      // Fetch object info for the loan
      const objectResponse = await api.call("getObject", {path: {id: loan.objectId}});
      overdueLoans.push({
        loan: loan.loanId,
        object: objectResponse.object.id,
        due: maxRenewalDate.toISOString().split('T')[0]
      });
    }
  }

  // Step 5: Sort by loan ID ascending
  overdueLoans.sort((a, b) => a.loan.localeCompare(b.loan));

  return overdueLoans;
}

return await getOverdueLoans();