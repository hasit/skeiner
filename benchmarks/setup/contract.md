# What the setup checker measures

The input is 1Password Connect 1.7.1's complete, published OpenAPI document. Run `python3 benchmarks/setup/spec.py` to install all 38,164 bytes at `bin/setup/connect.yaml`. The installer verifies SHA-256 `e597c52210719e5bf9b27556d6f3ead28434a35a8e8c827f71eec19899cc873d`. The study uses a loopback HTTP service with invented vault metadata and a fresh test bearer token. [Pinned provider document](https://github.com/1Password/connect/blob/420a61d55b8744b3732afa65f340ab5629526190/docs/openapi/spec.yaml)

## Acceptance

1. Start a fresh integration process and connect a client using MCP stdio. Browse named tools with `tools/list`, or use `search("vault")` on Skeiner. Only `GetVaults` and `GetVaultById` should be exposed. For a search catalog, the checker also searches every operation ID in the document. Read the optional string `filter` and required string `vaultUuid` from the published input metadata.
2. Send a SCIM name filter containing a newly generated vault name. Use the ID from the matching result to retrieve its details. Compare both results with the fixture records. Check the origin's log for the exact method, path, query parameters and host-supplied bearer token on both requests. The client supplies ordinary operation arguments. The checker scans MCP messages it reads for the test token.
3. Invoke the unapproved `GetApiActivity` read and `DeleteVaultItem` write using the same dispatch interface. Both calls must return MCP errors and the origin must have received only the two authorized reads. The activity call tests an owner restriction on a read Skeiner could otherwise serve. The delete operation also falls outside Skeiner's supported response types.
4. Restart the integration with a fresh origin, credential, name and ID. For this second pass only, supply a temporary copy of the same document with one randomly named optional string query parameter added to `GetVaults`. Verify the parameter is discoverable, send a fresh value, and observe it arrive at the origin. This tests whether input discovery and forwarding follow that change on the list route. Both passes use the same submitted integration files.

The result reports 12 checks against the original, then 14 against the modified input. A full pass requires all 26. Several checks inspect the same request. This measures two ordinary reads and two ordinary denials through the advertised client interface. It measures neither production credential provisioning nor sandbox isolation. [Checker](acceptance.py) · [Local fixture](fixtures/connect.py)

## What clients could discover before a read

We also captured discovery metadata from the two ready-made products using their passing configurations and the original document. Initialize an MCP stdio client and inspect `tools/list` for the explicit-mode JavaScript server. For Skeiner, call `search` with `{"query":"vault"}` and inspect each result's `response["200"]`. Neither discovery step made an upstream HTTP request. [Captured MCP metadata](fixtures/discovery.json)

| Observation | Skeiner 0.1.0-alpha.2 | JavaScript package 1.18.0 in explicit mode |
| --- | --- | --- |
| Allowed operation and input discovery | Both reads, input names, types, required flags and locations | Both reads, input names, types, required flags and locations, plus the filter example and ID pattern |
| Successful response discovery | List of objects for the filtered read, object for the detail read. Both included all nine typed vault fields, including `id`, `name`, `contentVersion` and the enum for `type`. | Both tool records omitted the output schema and response fields. The underlying OpenAPI document contains them. |

The common setup score measures operation and input discovery. This separate observation captures a difference in output discovery. Skeiner's returned metadata summarizes the successful response, including property types. For example, it omits the source document's `date-time` format and `readOnly` annotations for the timestamp fields.

To reproduce a stricter output-discovery check on the nine frozen, passing setups, run `python3 benchmarks/setup/output_study.py replay` from the repository root. It looks for the array and object, all nine typed properties, and the four values of the vault type enum. It then adds a fresh optional boolean to the shared Vault schema and checks both operations again. Each of the two starts must produce zero upstream requests. The checker reads Skeiner search results, named tools' output schemas (including a single-field wrapper for an array), or response schemas published in metadata or as JSON in tool descriptions. It resolves references using only what the client received.

All three original Skeiner setups passed the 16 output checks. The three explicit-mode package setups and three original custom workers each passed the two zero-request checks. Their primary authoring brief required input discovery. [Checker](output_discovery.py) · [Per-setup results](output-results/baseline.json)

## Source selection and coverage

The full Connect document declares 15 operations on 11 paths. With a bearer configured and no allowlist, Skeiner preflight enables six. For example, it excludes `GetVaultItemById` because its response schema uses `allOf`. Both reads selected for this benchmark are among the six.

```sh
CONNECT_BENCH_TOKEN=fixture ./skeiner preflight --spec bin/setup/connect.yaml \
  --base-url http://127.0.0.1:8080/v1 --token-env CONNECT_BENCH_TOKEN
```

A preflight during source selection also tried Sentry's `listOrganizationEnvironments` on its complete dereferenced specification. The selected GET route ends in a slash. Skeiner reported `unsupported path segment ""` and an unavailable allowlist operation. This candidate was excluded from the timed study. The timing results therefore apply to a supported operation pair selected after preflight. [Pinned Sentry specification](https://github.com/getsentry/sentry-api-schema/blob/d8e95f7f3c95b8aa5f60983f6f2239b71ac529d6/openapi-derefed.json)

```sh
curl -fLsS -o /tmp/skeiner-setup-sentry.json \
  https://raw.githubusercontent.com/getsentry/sentry-api-schema/d8e95f7f3c95b8aa5f60983f6f2239b71ac529d6/openapi-derefed.json
SENTRY_BENCH_TOKEN=fixture ./skeiner preflight --spec /tmp/skeiner-setup-sentry.json \
  --base-url https://us.sentry.io/api/0 --token-env SENTRY_BENCH_TOKEN \
  --allow listOrganizationEnvironments
```
