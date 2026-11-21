# From an OpenAPI document to a permissioned MCP integration

Can an API owner expose two approved reads from a new specification with less setup work using Skeiner? Compare three ways to make the same operations discoverable and callable over MCP stdio.

| Starting point | Work the author owns |
| --- | --- |
| Released Skeiner | Select operations and configure the API origin and host credential |
| Packaged JavaScript OpenAPI MCP server | Configure the same capabilities using the package's native interface |
| A new JavaScript worker | Write the integration using general-purpose JavaScript, protocol and parsing libraries |

All authors start with an empty integration directory, a shared operator brief and the same unmodified, checksum-pinned 1Password Connect OpenAPI document. Use a local service that returns synthetic vault metadata. Allow clients to list vaults with a filter and look up a vault by UUID. Keep the bearer credential in the host. Ordinary requests for other operations, including a different read and a write, must not reach the fixture. The checker accepts equivalent named-tool discovery or catalog search. Check that a client can find the approved inputs, that the program uses the configured origin and current token, and that the fixture actually received the intended requests.

Enroll three independent GPT-6.2 Astra authors per starting point, medium reasoning, fresh conversations and the same submission budget of six. An author may submit a candidate, see the acceptance failures and revise. Stop an author at the first full pass or at the limit. Retain every attempt. Freeze the last submitted files and replay their checks in a fresh process. Supply each arm a tool reference. Keep provider-specific launch examples out of those references.

Measure attempts to first pass, clean replays, elapsed time to a checked setup, billable model tokens, and the files the owner had to write. Distinguish declarative configuration from executable glue and bundled library code. Separately time installation of the two ready-made products with empty local download caches. Local system runtimes, benchmark authoring, procurement of real service credentials and human engineering time are outside the timed model task.

The comparison covers discovery, dispatch, authentication and permissions for this policy. Sandboxing, secret scrubbing, support for other OpenAPI features and production operations require separate evaluations.
