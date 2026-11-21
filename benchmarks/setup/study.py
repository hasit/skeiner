"""Measure and replay independent authors setting up a permissioned OpenAPI integration."""
import argparse
import hashlib
import json
import os
import re
import ssl
import subprocess
import tempfile
import time
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
from datetime import datetime, timezone
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from acceptance import check
from js_toolkit.custom_runtime import custom_command, package_command
from spec import DEFAULT_SPEC, verify

HERE = Path(__file__).resolve().parent
ARMS = ("skeiner", "package", "custom")
TURNS = 12
ATTEMPTS = 6
SETTINGS = {"model": "gpt-6.2-astra", "reasoning": {"effort": "medium"}, "service_tier": "default",
            "store": False, "include": ["reasoning.encrypted_content"], "max_output_tokens": 5000,
            "prompt_cache_options": {"mode": "explicit"}, "parallel_tool_calls": False}
TASK = """Set up an MCP stdio integration for an API owner. You start with an empty integration
directory and an OpenAPI YAML file. The team needs to discover and invoke exactly the two
approved reads: list vault metadata, optionally filtering the list, and get one vault's
metadata by its UUID. Return the upstream JSON array and object respectively. Use the
operation identifiers from the spec (native tool-name casing conventions are fine). For
named MCP tools return the JSON as a text content block. Discovery must describe the inputs
and their required/optional status. All other operations must be absent from discovery and
unavailable through normal tool calls, including the activity read and any delete. An
unauthorized call must return an MCP error and must never make an upstream request.

The operator provides an OpenAPI file path, a base URL including its service prefix, and a
raw bearer token on each launch. Construct authenticated HTTP requests to that base URL.
Keep the credential out of all client-visible results. After restart against a revised spec
with a new optional string query parameter on the approved list operation, the same setup
must discover it and forward its value. Hosts, credentials, values and responses vary.

Use read_spec to inspect the supplied document if useful. Use submit_setup to replace your
complete setup and run the acceptance checks with a synthetic local upstream. You may revise
from check feedback. Stop at first full pass. You have at most 12 tool turns and 6 submissions.
The launcher accepts a command, an argument array and a map of environment variable names
to string values. It expands ${SPEC}, ${BASE_URL} and ${TOKEN} wherever used in args/env.
No host-provided variables are inherited implicitly. Write whatever mappings your adapter
needs. Only the named runtime and arguments documented for your starting point are available.
Use the tool to submit files. There is no shell.
"""
REFS = {
    "skeiner": """Your starting point is the released Skeiner CLI. Use command 'skeiner', args beginning
with 'serve', and no worker source. The native search/execute MCP tools already implement
schema discovery and dispatch. Supported flags:
--spec FILE            Local OpenAPI JSON or YAML.
--base-url URL         Overrides the upstream URL, including optional path prefix.
--token-env NAME       Read bearer token from that host environment variable.
--allow IDs            Expose comma-separated OpenAPI operation IDs.
--allow-get PATH       Expose a GET using an exact OpenAPI path template, repeatable.
--header-env KEY=NAME  Bind a supported header to a host environment variable.
SKEINER_BASE_URL, SKEINER_ALLOW, and SKEINER_TOKEN_ENV can provide defaults.""",
    "package": """Your starting point is @ivotoby/openapi-mcp-server 1.18.0. Use command
'openapi-mcp-server', its native CLI args, and no worker source. The server generates named
MCP tools with schemas and dispatch from the spec. Supported flags:
--openapi-spec FILE    OpenAPI document.
--api-base-url URL     Overrides the upstream URL, including optional path prefix.
--tools MODE           'explicit' loads only selected tools. Default loads all operations.
--tool NAME            Include an operation; repeat for each one. Tool names derived from
                       an operationId are kebab-case (ReadBookById becomes read-book-by-id).
--headers HEADERS      Supply comma-separated Header:Value pairs. The host can instead set
                       API_HEADERS in the environment to the same string.
The header value for bearer authentication includes the literal 'Bearer ' prefix.""",
    "custom": """Your starting point is a new JavaScript worker. Use command 'deno-worker', args
['worker.js'], and supply the entire worker source in worker. Its three available environment
variable names are SPEC, BASE_URL and TOKEN. worker.js is an async function body (await allowed).
The harness provides the following imported library bindings without additional import statements:
Server, McpServer, StdioServerTransport, ListToolsRequestSchema, CallToolRequestSchema from MCP SDK
1.32.2, parseYaml from yaml 2.9.1, z from Zod 4.6.7. For low-level MCP, new Server({name,version},
{capabilities:{tools:{}}}) accepts setRequestHandler(ListToolsRequestSchema, async req => ...),
setRequestHandler(CallToolRequestSchema, async req => ...), and await server.connect(new
StdioServerTransport()). Named tools use name, description and inputSchema. Tool calls receive
req.params.name and req.params.arguments. For success return {content:[{type:'text',text:...}]}.
Deno.env.get(), Deno.readTextFile(), fetch(), standard JS, URL and stdio are available. The
worker owns spec parsing, the catalog, permissions, auth, and dispatch. It may use only local reads
of its own source/spec and network access to the specific test service.""",
}
COMMANDS = {"skeiner": "skeiner", "package": "openapi-mcp-server", "custom": "deno-worker"}
READ = {"type": "function", "name": "read_spec", "description": "Read 1-based YAML lines, or locate a literal string then read around its first match starting at start. Returns numbered lines, up to 120.",
        "parameters": {"type": "object", "properties": {"search": {"type": "string"}, "start": {"type": "integer"},
                                                        "lines": {"type": "integer"}}}, "strict": False}
SUBMIT = {"type": "function", "name": "submit_setup", "description": "Replace the entire integration and run black-box acceptance checks.",
          "parameters": {"type": "object", "properties": {"command": {"type": "string"},
                         "args": {"type": "array", "items": {"type": "string"}},
                         "env": {"type": "object", "additionalProperties": {"type": "string"}},
                         "worker": {"type": "string", "description": "Complete worker.js source for the custom worker only."}},
                         "required": ["command", "args", "env"], "additionalProperties": False}, "strict": False}
TOOLS = [READ, SUBMIT]


def pretty(value):
    return json.dumps(value, ensure_ascii=False, indent=2, allow_nan=False) + "\n"


def save(path, value):
    path.write_text(pretty(value))


def sha(value):
    return hashlib.sha256(value.encode()).hexdigest()


def read_spec(spec, search="", start=1, lines=55):
    content = spec.read_text().splitlines()
    index = max(0, int(start) - 1)
    if search:
        match = next((i for i in range(index, len(content)) if search.lower() in content[i].lower()), None)
        if match is None:
            return "No match."
        index = max(index, match - 4)
    count = max(1, min(120, int(lines)))
    return "\n".join(f"{i + 1}: {content[i]}" for i in range(index, min(index + count, len(content))))


def manifest(arm, sub):
    if sub.get("command") != COMMANDS[arm]:
        raise ValueError(f"Expected command {COMMANDS[arm]}")
    args, env = sub.get("args"), sub.get("env")
    if not isinstance(args, list) or not all(isinstance(arg, str) for arg in args):
        raise ValueError("args must be an array of strings")
    if not isinstance(env, dict) or not all(re.fullmatch(r"[A-Za-z_]\w*", k) and isinstance(v, str) for k, v in env.items()):
        raise ValueError("env must map environment names to strings")
    if arm == "custom":
        if args != ["worker.js"] or not isinstance(sub.get("worker"), str):
            raise ValueError("deno-worker expects [worker.js] and source in worker")
        if set(env) - {"SPEC", "BASE_URL", "TOKEN"}:
            raise ValueError("deno-worker accepts SPEC, BASE_URL, TOKEN as environment names")
    elif sub.get("worker"):
        raise ValueError("This packaged tool accepts only launch configuration")
    return {"command": sub["command"], "args": args, "env": env}


def checked_options(args, supported):
    options = {}
    i = 0
    while i < len(args):
        name, equal, value = args[i].partition("=")
        if not name.startswith("-") or name.lstrip("-") not in supported:
            raise ValueError(f"Unsupported option: {name}")
        if not equal:
            i += 1
            if i == len(args):
                raise ValueError(f"Missing argument to {name}")
            value = args[i]
        options.setdefault(name.lstrip("-"), []).append(value)
        i += 1
    return options


def packaged_args(arm, config):
    args, env = config["args"], config["env"]
    if arm == "skeiner":
        if args[:1] != ["serve"]:
            raise ValueError("Expected skeiner serve")
        options = checked_options(args[1:], {"base-url", "spec", "allow", "allow-get", "token-env", "header-env"})
        spec, base = options.get("spec"), options.get("base-url", [env.get("SKEINER_BASE_URL")])
        # Only credential variables explicitly named in declarative options/defaults are forwarded.
        names = set(options.get("token-env", [env.get("SKEINER_TOKEN_ENV", "GITHUB_TOKEN")]))
        names.update(v.partition("=")[2] for v in options.get("header-env", []))
        env_names = names | {"SKEINER_BASE_URL", "SKEINER_TOKEN_ENV", "SKEINER_ALLOW"}
    else:
        options = checked_options(args, {"api-base-url", "openapi-spec", "tools", "tool", "headers"})
        spec, base = options.get("openapi-spec"), options.get("api-base-url")
        env_names = {"API_HEADERS"}
    if spec != ["${SPEC}"] or base != ["${BASE_URL}"]:
        raise ValueError("Map the spec file and base URL options to ${SPEC} and ${BASE_URL}")
    for name in env:
        if name not in env_names or re.match(r"^(PATH$|HOME$|LD_|DYLD_|NODE_|GODEBUG$|GOTRACEBACK$)", name):
            raise ValueError(f"Unsupported environment name: {name}")
    return args


def setup_check(arm, submission, binary, spec, checker=check):
    config = manifest(arm, submission)
    if arm != "custom":
        packaged_args(arm, config)

    @contextmanager
    def launch(base, token, spec_path):
        values = {"BASE_URL": base, "TOKEN": token, "SPEC": str(spec_path)}
        def expand(value):
            return re.sub(r"\$\{(BASE_URL|TOKEN|SPEC)\}", lambda m: values[m[1]], value)
        env = {k: expand(v) for k, v in config["env"].items()}
        args = [expand(value) for value in config["args"]]
        with tempfile.TemporaryDirectory() as scratch:
            work = Path(scratch)
            if arm == "custom":
                worker = work / "worker.js"
                worker.write_text(submission["worker"])
                argv, env = custom_command(work, spec_path, base, env, worker)
            else:
                program = [str(binary)] if arm == "skeiner" else package_command()
                argv = program + args
                env = {"PATH": os.defpath, "HOME": scratch, **env}
            p = subprocess.Popen(argv, cwd=work, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                 stderr=subprocess.DEVNULL, text=True)
            try:
                yield p
            finally:
                try:
                    p.stdin.close()
                except BrokenPipeError:
                    pass
                try:
                    p.wait(timeout=1)
                except subprocess.TimeoutExpired:
                    p.kill()
                    p.wait()
                p.stdout.close()
    return checker(launch, spec)


def model_call(history):
    payload = {**SETTINGS, "input": history, "tools": TOOLS}
    request = Request("https://api.openai.com/v1/responses", data=json.dumps(payload).encode(),
                      headers={"Authorization": "Bearer " + os.environ["OPENAI_API_KEY"], "Content-Type": "application/json"})
    trust = os.environ.get("SSL_CERT_FILE") or ssl.get_default_verify_paths().cafile
    if not trust and Path("/etc/ssl/cert.pem").exists():
        trust = "/etc/ssl/cert.pem"
    try:
        with urlopen(request, context=ssl.create_default_context(cafile=trust), timeout=240) as response:
            return json.load(response)
    except HTTPError as exc:
        error = json.loads(exc.read()).get("error", {})
        raise RuntimeError(f"Provider HTTP {exc.code}: {error.get('message')}") from exc


def write_artifacts(folder, sub):
    config = {k: sub.get(k) for k in ("command", "args", "env")}
    save(folder / "launch.json", config)
    source = sub.get("worker", "")
    if source:
        (folder / "worker.js").write_text(source)
    config_text = pretty(config)
    return {"config_bytes": len(config_text.encode()), "config_nonblank_lines": len(config_text.splitlines()),
            "config_arg_count": len(config["args"]), "config_env_count": len(config["env"]),
            "source_bytes": len(source.encode()), "source_nonblank_lines": sum(bool(s.strip()) for s in source.splitlines())}


def measure(arm, trial, out, binary, spec, *, prompt=None, checker=check):
    folder = out / f"{arm}-{trial}"
    folder.mkdir()
    receipt = folder / "receipt.json"
    if prompt is None:
        prompt = TASK + "\n" + REFS[arm]
    report = {"arm": arm, "trial": trial, "settings": SETTINGS, "prompt_sha256": sha(prompt),
              "started_utc": datetime.now(timezone.utc).isoformat(), "turns": [], "steps": [],
              "attempts": [], "passed": False, "stop": "turn_limit"}
    history = [{"role": "user", "content": prompt}]
    started = time.perf_counter()
    save(receipt, report)
    try:
        for turn in range(1, TURNS + 1):
            t = time.perf_counter()
            response = model_call(history)
            report["turns"].append({"model": response["model"], "usage": response["usage"], "status": response["status"],
                                    "service_tier": response.get("service_tier"), "elapsed_seconds": round(time.perf_counter()-t, 3)})
            history.extend(response["output"])
            calls = [item for item in response["output"] if item["type"] == "function_call"]
            if response["status"] != "completed" or not calls:
                report["stop"] = response.get("incomplete_details") or "no_tool_call"
                break
            for call in calls:
                step = {"turn": turn, "name": call["name"]}
                report["steps"].append(step)
                try:
                    sub = json.loads(call["arguments"])
                    if call["name"] == "read_spec":
                        step["arguments"] = sub
                        feedback = read_spec(spec, **sub)
                        step["output_sha256"] = sha(feedback)
                    elif call["name"] == "submit_setup":
                        t = time.perf_counter()
                        number = len(report["attempts"]) + 1
                        try:
                            grade = setup_check(arm, sub, binary, spec, checker)
                        except (ValueError, TypeError, KeyError) as exc:
                            grade = {"passed": False, "error": str(exc)}
                        report["attempts"].append({"attempt": number, "submission": sub, "grade": grade,
                                                  "elapsed_seconds": round(time.perf_counter()-t, 3)})
                        step["attempt"] = number
                        feedback = json.dumps(grade)
                        report["passed"] = grade["passed"]
                    else:
                        raise ValueError("Unknown tool")
                    step["ok"] = True
                except (ValueError, KeyError, TypeError) as exc:
                    step.update(ok=False, error=str(exc))
                    feedback = f"Invalid tool arguments: {exc}"
                history.append({"type": "function_call_output", "call_id": call["call_id"], "output": feedback})
            save(receipt, report)
            if report["passed"] or len(report["attempts"]) >= ATTEMPTS:
                report["stop"] = "first_pass" if report["passed"] else "attempt_limit"
                break
    except Exception as exc:
        report["stop"] = "infrastructure_error"
        report["error"] = str(exc)
        raise
    finally:
        report["elapsed_seconds"] = round(time.perf_counter() - started, 3)
        if report["attempts"]:
            report["final_metrics"] = write_artifacts(folder, report["attempts"][-1]["submission"])
        save(receipt, report)
        print(arm, trial, report["stop"], report["elapsed_seconds"], flush=True)


def replay(out, binary, spec, record=None, *, checker=check):
    success = True
    results = []
    for path in sorted(out.glob("*/receipt.json")):
        report = json.loads(path.read_text())
        if not report["attempts"]:
            print(path.parent.name, "no submitted setup", flush=True)
            complete = report.get("elapsed_seconds") is not None and not report["passed"]
            success &= complete and report["stop"] != "infrastructure_error"
            results.append({"arm": report["arm"], "trial": report["trial"], "stop": report["stop"],
                            "submitted": False, "complete": complete})
            continue
        sub = json.loads(path.with_name("launch.json").read_text())
        worker = path.with_name("worker.js")
        if worker.exists():
            sub["worker"] = worker.read_text()
        saved = report["attempts"][-1]
        if sub != saved["submission"]:
            raise ValueError(f"Frozen files differ from final submission: {path.parent.name}")
        try:
            grade = setup_check(report["arm"], sub, binary, spec, checker)
        except (ValueError, TypeError, KeyError) as exc:
            grade = {"passed": False, "error": str(exc)}
        same = grade == saved["grade"]
        results.append({"arm": report["arm"], "trial": report["trial"], "passed": grade["passed"],
                        "matches": same, "checks_passed": grade.get("checks_passed"),
                        "checks_total": grade.get("checks_total"),
                        "launch_sha256": sha(path.with_name("launch.json").read_text()),
                        "worker_sha256": sha(sub["worker"]) if sub.get("worker") else None})
        print(path.parent.name, "pass" if grade["passed"] else "fail", "matches" if same else "changed", flush=True)
        success &= same
    if record:
        save(record, {"completed_utc": datetime.now(timezone.utc).isoformat(), "matched": success,
                      "binary_version": subprocess.check_output([str(binary), "version"], text=True).strip(),
                      "runs": results})
    return success


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("action", choices=["measure", "replay"])
    p.add_argument("--out", type=Path, default=HERE / "results")
    p.add_argument("--binary", type=Path, default=HERE.parents[1] / "bin/setup/skeiner")
    p.add_argument("--spec", type=Path, default=DEFAULT_SPEC)
    p.add_argument("--write-replay", type=Path, help="write replay outcomes to a new JSON file")
    args = p.parse_args()
    binary = args.binary.resolve(strict=True)
    verify(args.spec)
    package_command()
    if args.action == "replay":
        if len(list(args.out.glob("*/receipt.json"))) != 9:
            p.error("Expected nine author receipts")
        if args.write_replay and args.write_replay.exists():
            p.error("Choose a new replay output file")
        return 0 if replay(args.out, binary, args.spec, args.write_replay) else 1
    if args.write_replay:
        p.error("--write-replay applies to the replay action")
    if subprocess.check_output([str(binary), "version"], text=True).strip() != "0.1.0-alpha.2":
        p.error("Use the v0.1.0-alpha.2 release for measurement")
    if args.out.exists() or "OPENAI_API_KEY" not in os.environ:
        p.error("measure requires a new output directory and OPENAI_API_KEY")
    args.out.mkdir(parents=True)
    for i in range(3):
        with ThreadPoolExecutor(max_workers=3) as pool:
            jobs = [pool.submit(measure, arm, i + 1, args.out, binary, args.spec) for arm in ARMS[i:] + ARMS[:i]]
            for job in jobs:
                job.result()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
