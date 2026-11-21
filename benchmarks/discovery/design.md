# Release provenance benchmark

Can an agent using Skeiner produce a correct reusable investigation more cheaply or quickly than an agent that can search the same API catalog and run client-side JavaScript?

Given only a public GitHub repository and release tag, find the commit, its subject, and the signature presence on the commit and each annotated tag that leads to it. A lightweight tag has an empty tag chain. Signature presence is a property of the recorded Git object. Establishing a signer's identity or trust is outside this task.

| Repository | Tag | Use |
| --- | --- | --- |
| git/git | v2.47.0 | Discover, author, verify |
| golang/go | go1.22.0 | Reuse |
| cli/cli | v2.59.0 | Reuse |
| curl/curl | curl-8_11_0 | Reuse |

We chose GPT-6.2 Astra at medium reasoning for both arms. Enroll three pairs of independent authors, each starting a new Responses API conversation. Each receives the same task, input, search tool, operation catalog, turn limit and token budget. Skeiner executes JavaScript with operation-ID dispatch. Client-side Deno executes JavaScript with native fetch and a GET-to-JSON convenience function. Either program may fetch, branch, combine records and return just the answer. The host supplies no weather-style data preparation. Model prompts contain no route names. Archive all attempts.

The primary outcomes are complete correct answers grounded in the requisite reads, and model spend across the four releases including discovery, retries and initial authorship. Freeze the last executed program before grading it on the three other releases. Report elapsed time separately for authorship, cold offline replay and warm live replay. Use captured public API data for repeatable authoring and a native Git object oracle for grading.

For the live comparison, run all submitted programs with warm runtimes against the same GitHub gateway on the same machine. Alternate the order of the two arms across pairs and cases. Log answers, errors, request paths, response sizes and elapsed time. Repeat one complete pass from a second machine if available. Real GitHub results may include fields removed from published fixtures. A speed or cost advantage applies only to the configuration in which it was measured.
