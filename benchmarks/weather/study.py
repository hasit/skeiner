"""Enroll fresh weather conversations, or verify the archived programs without a model."""
import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
import time
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
from pathlib import Path
from urllib.parse import urlencode
from urllib.request import Request, urlopen

from dataset import ALL_CASES, CASES, EXTENSION, endpoint, expected, fetch, native, project, query, tls_context

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
sys.path.insert(0, str(ROOT))
from examples.rates.quickstart import request

MODEL = "gpt-4.1-mini-2025-04-14"
AUTHOR_CASE = "phoenix"
OPERATION = "getDailyWeather"
TRIALS = range(1, 4)
TURN_LIMIT = 6
TASK = (
    "Find the longest spell of consecutive local calendar days in the requested period "
    "whose daily maximum temperature is at or above threshold (degrees Celsius). "
    "A missing temperature breaks a spell. On a tie choose the earliest start. "
    "Answer with one JSON object: start and end (inclusive ISO dates), days (count), "
    "and peak_c (maximum temperature within the chosen spell). If there is no spell, "
    "use null for start, end, and peak_c and 0 for days. "
)
INSTRUCTION = {
    "direct": "Use the daily weather tool to calculate the answer.",
    "skeiner": (
        "Author a reusable JavaScript function body. Discover the archive operation, then "
        "execute your program on the supplied input (pass the input in execute.input and "
        "use api.input in your code). The threshold is for the calculation. Pass the other "
        "fields as the archive query. We save the last executed code unchanged to run on "
        "other locations, date ranges and thresholds. Return the computed answer."
    ),
    "reducer": (
        "Author a reusable JavaScript function body for the client-side reduce_weather tool. "
        "The host retrieves the daily series for the supplied query. Execute your function. "
        "We save the last executed code unchanged to run on other locations, date ranges and "
        "thresholds. Return the computed answer."
    ),
}
REDUCER_BINDINGS = "const {values, start_date, threshold} = api.input;\n"


def compact(value):
    return json.dumps(value, separators=(",", ":"), ensure_ascii=False, allow_nan=False)


def function(name, description, properties, required=()):
    return {"type": "function", "function": {"name": name, "description": description,
            "parameters": {"type": "object", "properties": properties, "required": list(required),
                           "additionalProperties": False}}}


DIRECT = function("daily_weather", (
    "Read daily maximum temperatures in Celsius for the supplied query. The compact "
    "default is an array in consecutive local calendar-day order beginning on start_date. "
    "Set dated=true for explicit [ISO date,temperature] pairs to simplify matching dates."
), {"dated": {"type": "boolean"}})
REDUCER = function("reduce_weather", (
    "Run a JavaScript function body over the supplied query's daily values. Its local "
    "variables are values (numbers or null in consecutive local calendar-day order), "
    "start_date (ISO date of the first value) and threshold (degrees Celsius). "
    "Return a JSON-serializable value. Dates can be computed with UTC calendar arithmetic."
), {"code": {"type": "string"}}, ["code"])


def enrolled(extension=False):
    for trial in TRIALS:
        for case in (EXTENSION if extension else CASES):
            yield "direct", trial, case
        if not extension:
            for arm in ("skeiner", "reducer"):
                yield arm, trial, AUTHOR_CASE


def read_weather(base, case):
    with urlopen(f"{base}/v1/archive?" + urlencode(query(case)), timeout=10) as response:
        return json.load(response)


@contextmanager
def session(binary, base, allow):
    command = [str(binary), "serve", "--spec", str(HERE / "openapi.yaml"), "--base-url", base,
               "--allow", allow, "--token-env", ""]
    with subprocess.Popen(command, env={"PATH": os.defpath}, stdin=subprocess.PIPE,
                          stdout=subprocess.PIPE, text=True) as child:
        request(child, 1, "initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                       "clientInfo": {"name": "weather-study", "version": "1"}})
        request(child, None, "notifications/initialized", {})
        tools = request(child, 2, "tools/list", {})["tools"]
        seq = 2

        def call(name, args):
            nonlocal seq
            seq += 1
            reply = request(child, seq, "tools/call", {"name": name, "arguments": args})
            return reply["content"][0]["text"], not reply.get("isError", False)

        yield tools, call


@contextmanager
def toolset(arm, case, binary, base):
    inputs = ALL_CASES[case]
    if arm == "direct":
        def direct(name, args):
            if name != "daily_weather":
                raise ValueError("use daily_weather")
            raw = read_weather(base, case)
            values = project(raw)
            if args.get("dated"):
                values = list(zip(raw["daily"]["time"], values))
            return compact(values), True

        yield [DIRECT], direct
        return

    with session(binary, base, OPERATION if arm == "skeiner" else "") as (tools, call):
        if arm == "skeiner":
            yield [{"type": "function", "function": {"name": t["name"], "description": t["description"],
                    "parameters": t["inputSchema"]}} for t in tools], call
            return

        def reduce(name, args):
            if name != "reduce_weather":
                raise ValueError("use reduce_weather")
            values = project(read_weather(base, case))
            return call("execute", {"code": REDUCER_BINDINGS + args["code"], "mode": "sync", "input": {
                "values": values, "threshold": inputs["threshold"], "start_date": inputs["start_date"]}})

        yield [REDUCER], reduce


def completion(messages, tools):
    payload = {"model": MODEL, "messages": messages, "tools": tools, "temperature": 0.2,
               "max_completion_tokens": 1200, "parallel_tool_calls": False, "store": False,
               "response_format": {"type": "json_object"}}
    req = Request("https://api.openai.com/v1/chat/completions", method="POST",
                  headers={"Authorization": "Bearer " + os.environ["OPENAI_API_KEY"],
                           "Content-Type": "application/json"}, data=compact(payload).encode())
    with urlopen(req, timeout=90, context=tls_context()) as response:
        return json.load(response)


def digest(source):
    return hashlib.sha256(source.encode()).hexdigest()


def answer_json(value):
    if not isinstance(value, str):
        return None
    fenced = re.fullmatch(r"\s*```(?:json)?\s*(.*?)\s*```\s*", value, flags=re.S | re.I)
    try:
        return json.loads(fenced.group(1) if fenced else value)
    except ValueError:
        return None


def grade(answer, case):
    reference = expected(case)
    if not isinstance(answer, dict):
        return False
    # Ignore ordering and incidental extra fields; require all facts as typed values.
    return all(k in answer and answer[k] == v and type(answer[k]) is not bool
               for k, v in reference.items())


def write(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")


def measure(binary, folder, job):
    arm, trial, case = job
    messages = [{"role": "system", "content": "Use the provided tools and respond with the requested JSON."},
                {"role": "user", "content": TASK + INSTRUCTION[arm] + " Input: " + compact(ALL_CASES[case])}]
    code, mode = None, None
    report = {"arm": arm, "trial": trial, "case": case, "model": MODEL, "turns": [], "calls": [],
              "reads": [], "tool_reply_bytes": 0, "input_tokens": 0, "cached_input_tokens": 0,
              "output_tokens": 0, "answer": None}
    with endpoint(report["reads"]) as base:
        start = time.perf_counter()
        with toolset(arm, case, binary, base) as (tools, call):
            for _ in range(TURN_LIMIT):
                reply = completion(messages, tools)
                if reply["model"] != MODEL:
                    raise ValueError(f"provider returned unexpected model {reply['model']}")
                choice = reply["choices"][0]
                msg = choice["message"]
                usage = reply["usage"]
                counts = {"input_tokens": usage["prompt_tokens"], "output_tokens": usage["completion_tokens"],
                          "cached_input_tokens": (usage.get("prompt_tokens_details") or {}).get("cached_tokens", 0)}
                for key, count in counts.items():
                    report[key] += count
                report["turns"].append({**counts, "finish_reason": choice["finish_reason"]})
                calls = msg.get("tool_calls") or []
                if not calls:
                    report["answer"] = answer_json(msg.get("content"))
                    if report["answer"] is None:
                        report["unparsed"] = msg.get("content")
                    break
                messages.append({"role": "assistant", "content": msg.get("content"), "tool_calls": calls})
                if len(calls) != 1:
                    raise ValueError("parallel tool calls were disabled")
                item = calls[0]
                name = item["function"]["name"]
                record = {"name": name}
                try:
                    args = json.loads(item["function"]["arguments"])
                    if name in ("reduce_weather", "execute"):
                        code, mode = args.get("code"), args.get("mode")
                        if isinstance(code, str):
                            record["source_sha256"] = digest(code)
                    record["arguments"] = {k: v for k, v in args.items() if k != "code"}
                    result, ok = call(name, args)
                except (KeyError, ValueError, RuntimeError, TypeError) as error:
                    result, ok = compact({"error": str(error)}), False
                record.update(ok=ok, reply_bytes=len(result.encode()))
                if not ok:
                    record["error"] = result
                report["tool_reply_bytes"] += record["reply_bytes"]
                report["calls"].append(record)
                messages.append({"role": "tool", "tool_call_id": item["id"], "content": result})
        report["elapsed_seconds"] = round(time.perf_counter() - start, 3)
    if isinstance(code, str):
        (folder / f"{arm}-{trial}.js").write_text(code)
        report.update(program_sha256=digest(code), saved_mode=mode)
    report["model_turns"] = len(report["turns"])
    report["correct"] = grade(report["answer"], case)
    write(folder / f"{arm}-{trial}-{case}.json", report)
    print(arm, trial, case, report["correct"], flush=True)


def replay(binary, folder, report, case):
    arm, trial = report["arm"], report["trial"]
    path = folder / f"{arm}-{trial}.js"
    reads = []
    answer = None
    if path.exists():
        code = path.read_text()
        if digest(code) != report.get("program_sha256"):
            raise ValueError(f"source digest mismatch: {path}")
        args = {"code": code}
        if arm == "skeiner":
            args["input"] = ALL_CASES[case]
            if report.get("saved_mode"):
                args["mode"] = report["saved_mode"]
        with endpoint(reads) as base:
            start = time.perf_counter()
            with toolset(arm, case, binary, base) as (_, call):
                raw, ok = call("execute" if arm == "skeiner" else "reduce_weather", args)
                if ok:
                    answer = answer_json(raw)
            elapsed = round(time.perf_counter() - start, 3)
    else:
        if report.get("program_sha256"):
            raise ValueError(f"missing archived source: {path}")
        elapsed = None
    return {"case": case, "answer": answer, "correct": grade(answer, case),
            "reads": reads, "elapsed_seconds": elapsed}


def check_receipt(report, job):
    arm, trial, case = job
    if (report["arm"], report["trial"], report["case"]) != job:
        raise ValueError("misidentified receipt")
    turns = report["turns"]
    if report["model"] != MODEL or not 1 <= len(turns) == report["model_turns"] <= TURN_LIMIT:
        raise ValueError("wrong model or turn count")
    for key in ("input_tokens", "output_tokens", "cached_input_tokens"):
        if report[key] != sum(turn[key] for turn in turns):
            raise ValueError(f"per-turn token counts differ from recorded {key}")
    if any(not 0 <= turn["cached_input_tokens"] <= turn["input_tokens"] or
           not 0 <= turn["output_tokens"] <= 1200 for turn in turns):
        raise ValueError("invalid per-turn usage")
    if report["tool_reply_bytes"] != sum(call["reply_bytes"] for call in report["calls"]):
        raise ValueError("inconsistent tool byte count")
    if report["correct"] != grade(report["answer"], case):
        raise ValueError("recorded grade differs from independent oracle")
    if arm in ("skeiner", "reducer"):
        author_calls = [call for call in report["calls"] if call["name"] in ("execute", "reduce_weather")]
        last_digest = author_calls[-1].get("source_sha256") if author_calls else None
        if report.get("program_sha256") != last_digest:
            raise ValueError("saved program differs from the last tool invocation")


def read_receipt(folder, job):
    arm, trial, case = job
    return json.loads((folder / f"{arm}-{trial}-{case}.json").read_text())


def stable(table):
    return {k: [{kk: vv for kk, vv in row.items() if kk != "elapsed_seconds"} for row in rows]
            for k, rows in table.items()}


def verify(binary, folder, extension=False, record=False):
    cases = EXTENSION if extension else CASES
    authors = HERE / "results" if extension else folder
    jobs = list(enrolled(extension))
    reports = (json.loads((folder / "direct.json").read_text()) if extension
               else [read_receipt(folder, job) for job in jobs])
    if len(jobs) != len(reports):
        raise ValueError("unexpected number of enrolled receipts")
    for report, job in zip(reports, jobs):
        check_receipt(report, job)
    for case in cases:
        if not grade(native(fetch(case), ALL_CASES[case]["threshold"]), case):
            raise ValueError(f"native client disagrees with the independent SQL result: {case}")
    rows = {}
    for arm, trial, case in enrolled():
        if arm == "direct":
            continue
        report = read_receipt(authors, (arm, trial, case))
        check_receipt(report, (arm, trial, case))
        rows[f"{arm}-{trial}"] = [replay(binary, authors, report, name) for name in cases]
    archive = folder / "replays.json"
    if record:
        write(archive, rows)
    elif stable(json.loads(archive.read_text())) != stable(rows):
        raise ValueError("replayed observations differ from the archive")
    print(f"Verified {len(reports)} receipts and {sum(len(v) for v in rows.values())} program cases.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("measure", "replay", "extension-measure", "extension-replay"))
    parser.add_argument("--binary", type=Path, default=ROOT / "skeiner")
    parser.add_argument("--out", type=Path)
    args = parser.parse_args()
    extension = args.action.startswith("extension-")
    folder = args.out or HERE / ("extension-results" if extension else "results")
    binary = args.binary.resolve()
    if args.action.endswith("replay"):
        verify(binary, folder, extension)
        return
    if not os.environ.get("OPENAI_API_KEY"):
        parser.error("measure requires OPENAI_API_KEY")
    folder.mkdir(parents=True, exist_ok=True)
    if any(folder.iterdir()):
        parser.error("measure requires an empty output directory")
    jobs = list(enrolled(extension))
    with ThreadPoolExecutor(max_workers=3) as pool:
        list(pool.map(lambda job: measure(binary, folder, job), jobs))
    if extension:
        write(folder / "direct.json", [read_receipt(folder, job) for job in jobs])
    verify(binary, folder, extension, record=True)
    if extension:
        for arm, trial, case in jobs:
            (folder / f"{arm}-{trial}-{case}.json").unlink()


if __name__ == "__main__":
    main()
