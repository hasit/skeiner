# Remote release audit with both workers at the gateway

Compare the same six frozen GPT-6.2 Astra programs from the release provenance study. Run the three Skeiner workers and three Deno workers on one disposable GitHub-hosted Linux machine. Every worker uses the same live GitHub gateway and shared upstream connection pool. The macOS client sends one HTTPS request per audit over an actual network connection. Both arms dispatch a saved program by identifier and receive the same JSON answer format.

Run four rounds of the original three author pairs on Git, Go, GitHub CLI and curl. The resulting 48 matched pairs contain 96 audits. Place the two members of each pair next to each other and balance which arm goes first. Warm both runtimes, the upstream connection and the client connection before timing. Include complete request and response time at the client, including any connection reestablishment during sampling.

The primary measurement is the median within-pair difference in elapsed time at the remote client, calculated as Skeiner minus Deno. Also record which arm finishes first, the median difference measured at the worker, and the correctness and required GitHub reads of every answer. Report measured effects for this client, host and session. Four rounds over four releases cannot establish a universal performance difference.

Use the existing native Git oracle. Keep all requests and grades, including failures. Report an interrupted session separately if a full rerun is necessary. Program authorship and installation were measured separately in the original study. This experiment measures warm reuse.
