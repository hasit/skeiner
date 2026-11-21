"""Compare authors discovering and fetching a real API from two JavaScript runtimes."""
import argparse
import hashlib
import json
import os
import re
import ssl
import subprocess
import sys
import tempfile
import time
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from client import CLIENT_EXECUTE, execute
from dataset import CASES, TASK, endpoint, expected, grounded

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
sys.path.insert(0, str(ROOT))
from examples.rates.quickstart import request

MODEL = "gpt-6.2-astra"
TURNS = 8
MAX_OUTPUT = 4000
ARMS = ("skeiner", "client")
TRIALS = (1, 2, 3)
AUTHOR_CASE = "git"
SPEC = HERE / "openapi.yaml"
INSTRUCTION = (
    "Use search to discover the API, then execute a reusable JavaScript function body that "
    "performs its own reads and returns the requested JSON. Supply the given input to execute. "
    "The source must also work unchanged for other inputs. We save the last executed source. "
    "Return its answer as JSON."
)
SETTINGS = {"model": MODEL, "reasoning": {"effort": "medium"}, "service_tier": "default",
            "store": False, "include": ["reasoning.encrypted_content"], "max_output_tokens": MAX_OUTPUT,
            "prompt_cache_options": {"mode": "explicit"}, "parallel_tool_calls": False}


def compact(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False)


def sha(text):
    return hashlib.sha256(text.encode()).hexdigest()


def author_prompt():
    return TASK + "\n" + INSTRUCTION + "\nInput: " + compact(CASES[AUTHOR_CASE])


def write(path, value):
    path.write_text(json.dumps(value, indent=2, ensure_ascii=False, allow_nan=False) + "\n")


def answer_json(text):
    if not isinstance(text, str):
        return None
    fence = re.fullmatch(r"\s*```(?:json)?\s*(.*?)\s*```\s*", text, flags=re.I | re.S)
    try:
        return json.loads(fence.group(1) if fence else text)
    except ValueError:
        return None


def correct(case, answer):
    # Grade the requested facts and tag order. Extra object fields are immaterial.
    def matches(reference, value):
        if type(value) is not type(reference):
            return False
        if isinstance(reference, dict):
            return all(key in value and matches(item, value[key]) for key, item in reference.items())
        if isinstance(reference, list):
            return len(reference) == len(value) and all(matches(a, b) for a, b in zip(reference, value))
        return reference == value
    return matches(expected(case), answer)


def completion(history, tools):
    payload = {**SETTINGS, "input": history, "tools": tools}
    req = Request("https://api.openai.com/v1/responses", data=compact(payload).encode(), headers={
        "Authorization": "Bearer " + os.environ["OPENAI_API_KEY"], "Content-Type": "application/json"})
    cafile = os.environ.get("SSL_CERT_FILE") or ssl.get_default_verify_paths().cafile
    if not cafile and Path("/etc/ssl/cert.pem").exists():
        cafile = "/etc/ssl/cert.pem"
    try:
        with urlopen(req, context=ssl.create_default_context(cafile=cafile), timeout=240) as response:
            return json.load(response)
    except HTTPError as exc:
        error = json.loads(exc.read()).get("error", {})
        raise RuntimeError(f"Model HTTP {exc.code}: {error.get('message')}") from exc


@contextmanager
def toolset(arm, binary, base):
    command = [str(binary), "serve", "--spec", str(SPEC), "--base-url", base, "--token-env", ""]
    with subprocess.Popen(command, env={"PATH": os.defpath}, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                          text=True) as child:
        request(child, 1, "initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                        "clientInfo": {"name": "discovery-study", "version": "1"}})
        request(child, None, "notifications/initialized", {})
        tools = request(child, 2, "tools/list", {})["tools"]
        schemas = []
        for tool in tools:
            if arm == "client" and tool["name"] == "execute":
                schema = CLIENT_EXECUTE["function"]
            else:
                schema = {"name": tool["name"], "description": tool["description"],
                          "parameters": tool["inputSchema"]}
            schemas.append({"type": "function", **schema, "strict": False})
        counter = 2

        def call(name, args):
            nonlocal counter
            if arm == "client" and name == "execute":
                return execute(base, args)
            counter += 1
            reply = request(child, counter, "tools/call", {"name": name, "arguments": args})
            return reply["content"][0]["text"], not reply.get("isError", False)

        yield schemas, call


def measure(binary, folder, arm, trial):
    prompt = author_prompt()
    history = [{"role": "user", "content": prompt}]
    reads = []
    report = {"arm": arm, "trial": trial, "case": AUTHOR_CASE, "settings": SETTINGS,
              "prompt_sha256": sha(prompt), "elapsed_seconds": None, "reads": reads, "turns": [], "calls": [],
              "answer": None, "stop": "turn_limit"}
    target = folder / f"{arm}-{trial}.json"
    code = mode = None
    with endpoint(reads) as base:
        start = time.perf_counter()
        with toolset(arm, binary, base) as (schemas, call):
            report["tools_sha256"] = sha(compact(schemas))
            for turn in range(1, TURNS + 1):
                started = time.perf_counter()
                reply = completion(history, schemas)
                report["turns"].append({"model": reply["model"], "usage": reply["usage"],
                                        "service_tier": reply.get("service_tier"), "status": reply["status"],
                                        "elapsed_seconds": round(time.perf_counter() - started, 3)})
                items = reply["output"]
                calls = [item for item in items if item["type"] == "function_call"]
                if reply["status"] != "completed":
                    report["stop"] = reply.get("incomplete_details") or reply["status"]
                    break
                if not calls:
                    messages = [item for item in items if item["type"] == "message"]
                    if messages:
                        text = "".join(block["text"] for block in messages[-1]["content"]
                                       if block["type"] == "output_text")
                        report["answer"] = answer_json(text)
                        if report["answer"] is None:
                            report["raw_answer"] = text
                    report["stop"] = "final"
                    break
                # Keep reasoning items only in this in-memory conversation, as required for stateless tools.
                history.extend(items)
                for item in calls:
                    name = item["name"]
                    record = {"turn": turn, "name": name, "argument_bytes": len(item["arguments"].encode())}
                    try:
                        args = json.loads(item["arguments"])
                        record["arguments"] = args
                        if name == "execute":
                            code, mode = args.get("code"), args.get("mode")
                        raw, ok = call(name, args)
                    except (ValueError, KeyError, TypeError, RuntimeError) as exc:
                        raw, ok = compact({"error": str(exc)}), False
                    record.update(ok=ok, reply_bytes=len(raw.encode()), result=answer_json(raw))
                    if record["result"] is None:
                        record["result"] = raw
                    report["calls"].append(record)
                    history.append({"type": "function_call_output", "call_id": item["call_id"], "output": raw})
                write(target, report)
        report["elapsed_seconds"] = round(time.perf_counter() - start, 3)
    if isinstance(code, str):
        (folder / f"{arm}-{trial}.js").write_bytes(code.encode())
        report.update(program_sha256=sha(code), saved_mode=mode)
    report["correct"] = correct(AUTHOR_CASE, report["answer"])
    report["grounded"] = grounded(AUTHOR_CASE, reads)
    write(target, report)
    print(arm, trial, report["correct"], report["grounded"], report["elapsed_seconds"], flush=True)


def run_program(binary, arm, program, receipt, case, base):
    args = {"code": program.read_text(), "input": CASES[case]}
    if arm == "client":
        return execute(base, args)
    with tempfile.TemporaryDirectory() as folder:
        inp = Path(folder) / "input.json"
        inp.write_text(compact(CASES[case]))
        command = [str(binary), "run", "--spec", str(SPEC), "--base-url", base, "--token-env", "",
                   "--program", str(program), "--input-file", str(inp)]
        if receipt["saved_mode"]:
            command.extend(["--mode", receipt["saved_mode"]])
        proc = subprocess.run(command, env={"PATH": os.defpath}, capture_output=True, text=True, timeout=45)
        return (proc.stdout if proc.returncode == 0 else proc.stderr), proc.returncode == 0


def replay(binary, folder, arm, trial, case, receipt):
    program = folder / f"{arm}-{trial}.js"
    reads = []
    row = {"case": case, "answer": None, "correct": False, "grounded": False, "elapsed_seconds": None,
           "reads": reads, "ok": False}
    if not receipt.get("program_sha256"):
        return row
    if sha(program.read_bytes().decode()) != receipt["program_sha256"]:
        raise ValueError(f"source differs from measured program: {program}")
    with endpoint(reads) as base:
        start = time.perf_counter()
        raw, ok = run_program(binary, arm, program, receipt, case, base)
        row.update(elapsed_seconds=round(time.perf_counter() - start, 3), ok=ok)
    row["answer"] = answer_json(raw) if ok else None
    row["correct"] = correct(case, row["answer"])
    row["grounded"] = grounded(case, reads)
    return row


def enrolled():
    for trial in TRIALS:
        for arm in (ARMS if trial % 2 else ARMS[::-1]):
            yield arm, trial


def verify(binary, folder, record=False):
    observations = {}
    for arm, trial in enrolled():
        name = f"{arm}-{trial}"
        receipt = json.loads((folder / f"{name}.json").read_text())
        if (receipt["arm"], receipt["trial"], receipt["case"], receipt["settings"]) != (
                arm, trial, AUTHOR_CASE, SETTINGS):
            raise ValueError(f"unexpected enrollment: {name}")
        if receipt["prompt_sha256"] != sha(author_prompt()):
            raise ValueError(f"archived prompt differs from the measured task: {name}")
        if (receipt["correct"] != correct(AUTHOR_CASE, receipt["answer"])
                or receipt["grounded"] != grounded(AUTHOR_CASE, receipt["reads"])):
            raise ValueError(f"archived grade differs from independent reference: {name}")
        if not 1 <= len(receipt["turns"]) <= TURNS:
            raise ValueError(f"unexpected turn count: {name}")
        for turn in receipt["turns"]:
            usage = turn["usage"]
            detail = usage["input_tokens_details"]
            if (not turn["model"].startswith(MODEL)
                    or usage["input_tokens"] < detail["cached_tokens"] + detail.get("cache_write_tokens", 0)
                    or usage["output_tokens"] > MAX_OUTPUT
                    or usage["output_tokens_details"]["reasoning_tokens"] > usage["output_tokens"]):
                raise ValueError(f"unexpected model or usage: {name}")
        calls = [c for c in receipt["calls"] if c["name"] == "execute"]
        code = calls[-1].get("arguments", {}).get("code") if calls else None
        if (sha(code) if isinstance(code, str) else None) != receipt.get("program_sha256"):
            raise ValueError(f"wrong archived source: {name}")
        observations[name] = [replay(binary, folder, arm, trial, case, receipt) for case in CASES]
    path = folder / "replays.json"
    if record:
        write(path, observations)
    else:
        def stable(table):
            return {k: [{f: v for f, v in row.items() if f != "elapsed_seconds"} for row in rows]
                    for k, rows in table.items()}
        if stable(json.loads(path.read_text())) != stable(observations):
            raise ValueError("replays differ from the recorded observations")
    print(f"Verified {len(observations)} submitted programs on {len(CASES)} cases.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("measure", "replay"))
    parser.add_argument("--binary", type=Path, default=ROOT / "skeiner")
    parser.add_argument("--out", type=Path, default=HERE / "results")
    args = parser.parse_args()
    binary = args.binary.resolve()
    if args.action == "replay":
        verify(binary, args.out)
        return
    if not os.getenv("OPENAI_API_KEY"):
        parser.error("measure requires OPENAI_API_KEY")
    args.out.mkdir(parents=True, exist_ok=True)
    if any(args.out.iterdir()):
        parser.error("measure requires an empty output directory")
    for trial in TRIALS:
        order = ARMS if trial % 2 else ARMS[::-1]
        with ThreadPoolExecutor(max_workers=2) as pool:
            jobs = [pool.submit(measure, binary, args.out, arm, trial) for arm in order]
            for job in jobs:
                job.result()
    verify(binary, args.out, record=True)


if __name__ == "__main__":
    main()
