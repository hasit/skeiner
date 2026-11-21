// Deno supplies the permission boundary. Only the loopback benchmark API is reachable.
async function evaluate({ code, input, baseUrl }) {
  async function get(path, query = {}) {
    const url = new URL(path, baseUrl);
    const target = new URL(url.pathname + url.search, baseUrl);
    for (const [key, value] of Object.entries(query)) {
      if (value !== undefined && value !== null) target.searchParams.set(key, String(value));
    }
    const response = await fetch(target, { redirect: "error" });
    if (!response.ok) throw new Error(`HTTP ${response.status}: ${(await response.text()).slice(0, 512)}`);
    return await response.json();
  }
  const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
  const run = new AsyncFunction("input", "baseUrl", "get", code);
  const result = JSON.stringify(await run(input, baseUrl, get));
  if (result === undefined) throw new Error("Return a JSON value from the function body");
  if (new TextEncoder().encode(result).length > 16384) throw new Error("JSON value exceeds 16 KiB");
  return result;
}

if (Deno.args[0] === "--session") {
  // Timing mode for already archived programs. Each call has its own local bindings.
  console.log(JSON.stringify({ ready: true }));
  let pending = "";
  for await (const chunk of Deno.stdin.readable.pipeThrough(new TextDecoderStream())) {
    pending += chunk;
    let newline;
    while ((newline = pending.indexOf("\n")) >= 0) {
      const line = pending.slice(0, newline);
      pending = pending.slice(newline + 1);
      try {
        console.log(JSON.stringify({ result: await evaluate(JSON.parse(line)) }));
      } catch (error) {
        console.log(JSON.stringify({ error: String(error) }));
      }
    }
  }
} else {
  try {
    const result = await evaluate(await new Response(Deno.stdin.readable).json());
    await Deno.stdout.write(new TextEncoder().encode(result));
  } catch (error) {
    console.error(String(error));
    Deno.exit(1);
  }
}
