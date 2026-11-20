"""Compare fresh direct answers with authoring and replaying a saved investigation."""
import argparse
import hashlib
import json
import os
import subprocess
import sys
import tempfile
import time
from contextlib import contextmanager
from pathlib import Path
from urllib.error import HTTPError
from urllib.parse import urlencode
from urllib.request import Request, urlopen

from fixture import CASES, OPERATIONS, TASK, endpoint, expected

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
sys.path.insert(0, str(ROOT))
from examples.rates.quickstart import request

MODEL = "gpt-4.1-mini-2025-04-14"
SPEC = HERE / "openapi.yaml"
AUTHOR = (
    " Author one reusable JavaScript function body. Read the branch and asOf from api.input "
    "so the same source will run on different inputs and current records. Discover the API "
    "and execute that body with the provided input and async mode. We save your last execute "
    "program exactly as sent. Return its answer in the requested JSON format."
)
DIRECT_TOOLS = [
    {"name": op, "description": desc + " Pass nextCursor as cursor, omitting it on the first page.",
     "inputSchema": {"type": "object", "properties": {
         "branch": {"type": "string"}, "cursor": {"type": "string"}},
         "required": ["branch"], "additionalProperties": False}}
    for op, desc in (
        ("listLoans", "Read a museum branch page with loans[{loanId,objectId,dueDate,status (open or returned)}] and nextCursor."),
        ("listRenewals", "Read a museum branch page with renewals[{loanId,through,decision (approved or withdrawn)}] and nextCursor."))
]


def compact(value):
    return json.dumps(value, separators=(",", ":"))


def fingerprint(code):
    return hashlib.sha256(code.encode()).hexdigest()


@contextmanager
def tools_for(arm, binary, base):
    if arm == "direct":
        def read(name, args):
            url = f"{base}/{OPERATIONS[name]}?" + urlencode(args)
            with urlopen(url, timeout=10) as response:
                return response.read().decode(), True

        yield DIRECT_TOOLS, read
        return

    with subprocess.Popen([str(binary), "serve", "--spec", str(SPEC), "--base-url", base,
                           "--allow", ",".join(OPERATIONS), "--token-env", ""],
                          env={"PATH": os.defpath}, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                          text=True) as child:
        request(child, 1, "initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                        "clientInfo": {"name": "authorship-study", "version": "1"}})
        request(child, None, "notifications/initialized", {})
        tools = request(child, 2, "tools/list", {})["tools"]
        seq = 3

        def call(name, args):
            nonlocal seq
            seq += 1
            response = request(child, seq, "tools/call", {"name": name, "arguments": args})
            return response["content"][0]["text"], not response.get("isError", False)

        yield tools, call


def completion(messages, tools, model):
    payload = {"model": model, "messages": messages, "tools": tools, "temperature": 0.2,
               "max_completion_tokens": 1200, "store": False}
    req = Request("https://api.openai.com/v1/chat/completions", method="POST",
                  headers={"Authorization": "Bearer " + os.environ["OPENAI_API_KEY"],
                           "Content-Type": "application/json"}, data=json.dumps(payload).encode())
    with urlopen(req, timeout=75) as response:
        return json.load(response)


def measure(arm, trial, case, binary, out, model):
    data = CASES[case][0]
    messages = [{"role": "system", "content": "Use the available tools. Return only the requested JSON."},
                {"role": "user", "content": TASK + " Input: " + compact(data) + (AUTHOR if arm == "author" else "")}]
    report = {"trial": trial, "arm": arm, "case": case, "model": model,
              "input_tokens": 0, "cached_input_tokens": 0, "output_tokens": 0,
              "model_turns": 0, "calls": [], "tool_reply_bytes": 0, "answer": None}
    path = out / f"{arm}-{trial}-{case}.json"
    if path.exists():
        raise ValueError(f"choose an unused output directory or trial number: {path}")
    code = None
    with endpoint(case) as (base, reads), tools_for(arm, binary, base) as (tools, call):
        if arm == "author":
            execute = next(t for t in tools if t["name"] == "execute")
            if "input" not in execute["inputSchema"]["properties"]:
                raise ValueError("this binary needs MCP execute.input for the authorship arm")
        schemas = [{"type": "function", "function": {"name": t["name"], "description": t["description"],
                    "parameters": t["inputSchema"]}} for t in tools]
        start = time.perf_counter()
        for turn in range(10):
            reply = completion(messages, schemas, model)
            usage = reply["usage"]
            report["model_turns"] += 1
            report["input_tokens"] += usage["prompt_tokens"]
            report["cached_input_tokens"] += (usage.get("prompt_tokens_details") or {}).get("cached_tokens", 0)
            report["output_tokens"] += usage["completion_tokens"]
            message = reply["choices"][0]["message"]
            calls = message.get("tool_calls", [])
            if not calls:
                try:
                    report["answer"] = json.loads(message.get("content") or "")
                except ValueError:
                    report["answer_format_error"] = True
                    report["unparsed_answer"] = message.get("content")
                break
            messages.append({"role": "assistant", "content": message.get("content"), "tool_calls": calls})
            for item in calls:
                name = item["function"]["name"]
                try:
                    args = json.loads(item["function"]["arguments"])
                    if name == "execute":
                        code = args.get("code")
                        report["saved_mode"] = args.get("mode")
                    result, ok = call(name, args)
                except (ValueError, KeyError, RuntimeError, HTTPError) as exc:
                    result, ok = compact({"error": str(exc)}), False
                report["calls"].append({"name": name, "ok": ok})
                report["tool_reply_bytes"] += len(result.encode())
                messages.append({"role": "tool", "tool_call_id": item["id"], "content": result})
        report["elapsed_seconds"] = round(time.perf_counter() - start, 3)
        report["reads"] = list(reads)
    report["correct"] = report["answer"] == expected(case)
    if isinstance(code, str):
        (out / f"program-{trial}.js").write_text(code)
        report["program_sha256"] = fingerprint(code)
    path.write_text(json.dumps(report, indent=2) + "\n")
    print(arm, trial, case, "correct:", report["correct"], "reads:", len(reads), flush=True)


def unordered(answer, reference):
    """Compare all IDs and effective dates, preserving duplicate multiplicity."""
    if not isinstance(answer, list):
        return False
    rows = lambda values: sorted(json.dumps(row, sort_keys=True) for row in values)
    return rows(answer) == rows(reference)


def direct_program(case, base):
    """A hand-authored direct HTTP client for the zero-model replay control."""
    inputs = CASES[case][0]
    pages = {}
    for collection in OPERATIONS.values():
        items = []
        query = {"branch": inputs["branch"]}
        while True:
            with urlopen(base + "/" + collection + "?" + urlencode(query), timeout=10) as response:
                page = json.load(response)
            items.extend(page[collection])
            if page["nextCursor"] is None:
                break
            query["cursor"] = page["nextCursor"]
        pages[collection] = items
    extensions = {}
    for r in pages["renewals"]:
        if r["decision"] == "approved":
            extensions[r["loanId"]] = max(extensions.get(r["loanId"], ""), r["through"])
    matches = []
    for loan in pages["loans"]:
        due = max(loan["dueDate"], extensions.get(loan["loanId"], ""))
        if loan["status"] == "open" and due < inputs["asOf"]:
            matches.append({"loan": loan["loanId"], "object": loan["objectId"], "due": due})
    return sorted(matches, key=lambda row: row["loan"])


def replay(binary, program, mode=None):
    rows = []
    with tempfile.TemporaryDirectory() as folder:
        input_path = Path(folder) / "input.json"
        for case, (inputs, _) in CASES.items():
            input_path.write_text(compact(inputs))
            with endpoint(case) as (base, reads):
                args = [str(binary), "run", "--spec", str(SPEC), "--base-url", base, "--token-env", "",
                        "--allow", ",".join(OPERATIONS), "--program", str(program),
                        "--input-file", str(input_path)]
                if mode:
                    args.extend(["--mode", mode])
                start = time.perf_counter()
                run = subprocess.run(args, env={"PATH": os.defpath}, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                     text=True, timeout=45)
                elapsed = round(time.perf_counter() - start, 3)
                answer = json.loads(run.stdout) if run.returncode == 0 else None
                rows.append({"case": case, "correct": answer == expected(case),
                             "correct_unordered": unordered(answer, expected(case)), "reads": list(reads),
                             "answer": answer, "elapsed_seconds": elapsed})
    return rows


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=ROOT / "skeiner")
    commands = parser.add_subparsers(dest="action", required=True)
    live = commands.add_parser("measure", help="start fresh paid model conversations")
    live.add_argument("--arm", choices=("direct", "author"), required=True)
    live.add_argument("--trial", type=int, required=True)
    live.add_argument("--model", choices=(MODEL, "gpt-4.1-2025-04-14"), default=MODEL)
    live.add_argument("--out", type=Path, required=True)
    offline = commands.add_parser("replay", help="grade the archived programs using the local binary")
    offline.add_argument("--dir", type=Path, default=HERE / "results")
    offline.add_argument("--require-correct", action="store_true", help="require every archived program to pass")
    args = parser.parse_args()
    binary = args.binary.resolve()
    if args.action == "measure":
        args.out.mkdir(parents=True, exist_ok=True)
        for case in (["original"] if args.arm == "author" else CASES):
            measure(args.arm, args.trial, case, binary, args.out, args.model)
    else:
        receipts = sorted(args.dir.glob("author-*-original.json"))
        if not receipts:
            parser.error("no author-*-original.json files in --dir")
        observation = args.dir / "replays.json"
        observed_rows = json.loads(observation.read_text()) if observation.exists() else {}
        if observed_rows:
            receipt_trials = {str(json.loads(path.read_text())["trial"]) for path in receipts}
            if receipt_trials != set(observed_rows):
                parser.error("recorded trials do not match the available author receipts")
        correct = True
        for case in CASES:
            with endpoint(case) as (base, reads):
                answer = direct_program(case, base)
                correct &= answer == expected(case)
                print(json.dumps({"direct_program": case, "correct": answer == expected(case), "reads": len(reads)}))
        for path in receipts:
            receipt = json.loads(path.read_text())
            trial = str(receipt["trial"])
            program = args.dir / f"program-{trial}.js"
            if not program.exists():
                print(json.dumps({"trial": trial, "error": "no captured execute program"}))
                correct = False
                continue
            if receipt["program_sha256"] != fingerprint(program.read_text()):
                parser.error(f"source differs from the measured program: {program.name}")
            rows = replay(binary, program, receipt["saved_mode"])
            print(json.dumps({"trial": trial, "replay": rows}))
            if args.require_correct or trial not in observed_rows:
                correct &= all(row["correct"] for row in rows)
            else:
                fields = ("case", "correct", "correct_unordered", "reads", "answer")
                if len(rows) != len(observed_rows[trial]):
                    parser.error(f"snapshot count differs from the archived trial: {trial}")
                for current, recorded in zip(rows, observed_rows[trial]):
                    correct &= all(current[field] == recorded[field] for field in fields)
        sys.exit(0 if correct else 1)


if __name__ == "__main__":
    main()
