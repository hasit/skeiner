// Count commit subjects using the Git project's localization and merge prefixes.
const {sha, limit, perPage} = api.input;
const mix = {localization: 0, merges: 0, other: 0};
for (let page = 1; page <= Math.ceil(limit / perPage); page++) {
  const commits = await api.call("listCommits", {query: {sha, page, per_page: perPage}});
  const needed = Math.min(perPage, limit - (page - 1) * perPage);
  if (commits.length < needed) throw new Error("Incomplete commit page.");
  for (const row of commits.slice(0, needed)) {
    const message = row.commit.message;
    if (message.startsWith("l10n:")) mix.localization++;
    else if (message.startsWith("Merge ")) mix.merges++;
    else mix.other++;
  }
}
return mix;
