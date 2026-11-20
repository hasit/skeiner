async function getOverdueLoans() {
  const branch = api.input.branch;
  const asOf = new Date(api.input.asOf);

  // Step 1: Get all open loans at the requested branch
  let loans = [];
  let cursor = undefined;
  do {
    const response = await api.call("listLoans", {query: {branch: branch, cursor: cursor}});
    loans = loans.concat(response.loans.filter(loan => loan.status === "Open"));
    cursor = response.nextCursor || undefined;
  } while (cursor);

  // Step 2: Get all renewals at the branch
  let renewals = [];
  let renewalCursor = undefined;
  do {
    const renewalResponse = await api.call("listRenewals", {query: {branch: branch, cursor: renewalCursor}});
    renewals = renewals.concat(renewalResponse.renewals.filter(r => r.decision === "Approved"));
    renewalCursor = renewalResponse.nextCursor || undefined;
  } while (renewalCursor);

  // Step 3: Calculate effective due date for each loan
  const overdueLoans = [];
  for (const loan of loans) {
    let effectiveDueDate = new Date(loan.dueDate);

    // Find all approved renewals for this loan
    const loanRenewals = renewals.filter(r => r.loanId === loan.loanId);

    // Find the latest renewal through date
    for (const renewal of loanRenewals) {
      const renewalDueDate = new Date(renewal.through);
      if (renewalDueDate > effectiveDueDate) {
        effectiveDueDate = renewalDueDate;
      }
    }

    // Step 4: Check if effective due date is before the asOf date (overdue)
    if (effectiveDueDate < asOf) {
      // Step 5: Get the object details for the loan
      const objectResponse = await api.call("getObject", {path: {id: loan.objectId}});
      overdueLoans.push({loan: loan.loanId, object: objectResponse.object.id, due: effectiveDueDate.toISOString().split('T')[0]});
    }
  }

  // Step 6: Sort by loan ID ascending
  overdueLoans.sort((a, b) => a.loan.localeCompare(b.loan));

  return overdueLoans;
}

return await getOverdueLoans();