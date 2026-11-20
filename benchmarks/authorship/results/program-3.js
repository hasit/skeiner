// Reusable function to get overdue museum loans at a branch as of a given date
async function getOverdueLoans(api) {
    const branch = api.input.branch;
    const asOf = api.input.asOf;
    let loans = [];
    let cursor = undefined;
    // Fetch all open loans for the branch
    do {
        const resp = await api.call("listLoans", { query: { branch, ...(cursor ? { cursor } : {}) } });
        for (const loan of resp.loans) {
            if (loan.status === "open") {
                loans.push({ loanId: loan.loanId, objectId: loan.objectId, dueDate: loan.dueDate });
            }
        }
        cursor = resp.nextCursor;
    } while (cursor);
    // Fetch all renewals for the branch
    let renewals = [];
    cursor = undefined;
    do {
        const resp = await api.call("listRenewals", { query: { branch, ...(cursor ? { cursor } : {}) } });
        for (const r of resp.renewals) {
            if (r.decision === "approved") {
                renewals.push({ loanId: r.loanId, through: r.through });
            }
        }
        cursor = resp.nextCursor;
    } while (cursor);
    // Map loanId to latest approved renewal date
    const renewalMap = {};
    for (const r of renewals) {
        if (!renewalMap[r.loanId] || renewalMap[r.loanId] < r.through) {
            renewalMap[r.loanId] = r.through;
        }
    }
    // For each loan, determine the effective due date
    const overdue = [];
    for (const loan of loans) {
        let effectiveDue = loan.dueDate;
        if (renewalMap[loan.loanId] && renewalMap[loan.loanId] > effectiveDue) {
            effectiveDue = renewalMap[loan.loanId];
        }
        if (effectiveDue < asOf) {
            overdue.push({ loan: loan.loanId, object: loan.objectId, due: effectiveDue });
        }
    }
    overdue.sort((a, b) => a.loan.localeCompare(b.loan));
    return overdue;
}
return await getOverdueLoans(api);