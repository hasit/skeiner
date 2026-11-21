# GitHub release data

The fixtures contain public GitHub REST responses captured on November 20, 2026 with API version `2022-11-28`. The four cases cover a lightweight tag on an unsigned commit (Go), a lightweight tag on a signed commit (GitHub CLI), a signed annotated tag on an unsigned commit (Git), and a signed annotated tag on a signed commit (curl). The benchmark asks about an embedded signature's presence. Evaluating the signer or the trust chain would require a separate check. [Git](https://github.com/git/git/tree/v2.47.0) · [Go](https://github.com/golang/go/tree/go1.22.0) · [GitHub CLI](https://github.com/cli/cli/tree/v2.59.0) · [curl](https://github.com/curl/curl/tree/curl-8_11_0)

Each repository snapshot maps paths to the observed HTTP status and a projection of the JSON body. It includes nearby repository, branch, release and tree operations plus the exact tag reference, matching references, annotated tags, and two ways of reading a commit. The loopback server returns 404 for an unrecorded path. Full object IDs, types, URLs, verification status, and first-line messages come from GitHub. Nonempty signature bodies and multiline message bodies are replaced with placeholders. Personal contact fields, patches, release assets and tree entries are omitted. The host serves the same JSON to both clients. These projections support answer and workflow comparisons; they give smaller payloads than the full live API. [Reference](https://docs.github.com/en/rest/git/refs) · [Annotated tag](https://docs.github.com/en/rest/git/tags) · [Git commit](https://docs.github.com/en/rest/git/commits) · [Repository commit](https://docs.github.com/en/rest/commits/commits)

The oracle uses an independent shallow Git fetch of each named tag. It reads the tag and commit objects with `git cat-file`, verifies each native object ID against its content, and extracts signature presence from the unaltered Git objects. The published answer includes just the subject, identifiers and boolean flags. [Oracle](oracle.json) · [Fixture checksums](sha256.json) · [Capture and verification](../dataset.py)

From the repository root:

```sh
python3 benchmarks/discovery/dataset.py verify
python3 benchmarks/discovery/dataset.py verify-git
```

The first command checks the frozen data offline. The second reads the four public Git repositories again. `dataset.py capture` purposefully refreshes all snapshots, including mutable neighboring routes, from GitHub REST and Git. Optional `GH_TOKEN` or `GITHUB_TOKEN` raises the REST rate limit on this public data.
