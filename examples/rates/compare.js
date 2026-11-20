// Which currencies had an ECB rate above the blended EUR rate on May 15, 2024?
const matches = [];
for (const quote of ["GBP", "USD"]) {
    const blended = await api.call("getRate", {
        path: {base: "EUR", quote},
        query: {date: "2024-05-15"}
    });
    const ecb = await api.call("getProviderRate", {
        path: {provider: "ECB", base: blended.base, quote: blended.quote},
        query: {date: blended.date}
    });
    if (ecb.date === blended.date && ecb.rate > blended.rate) {
        matches.push({date: ecb.date, quote, blended: blended.rate, ecb: ecb.rate});
    }
}
return matches;
