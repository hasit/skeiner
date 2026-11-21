# Gateway placement check

We added this sensitivity check after the co-located authoring and macOS timing results. Use the same six frozen programs, four releases, three rounds and live GitHub gateway. Add a fixed 50 ms round trip between client and gateway. A Deno HTTP request pays it once, including when several independent reads overlap. A Skeiner program dispatched to the gateway host pays it once per execution. Record all paired timings on macOS.

This artificial delay isolates the cost of sending dependent requests across a distant link. It represents a deployment where both clients must use the same credential gateway and Skeiner can execute beside it. The production server in this benchmark speaks stdio. A remote deployment would also need a transport and authentication. The co-located measurements remain the primary comparison.
