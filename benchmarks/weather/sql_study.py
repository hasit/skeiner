"""Measure three fresh SQL authors on the community Open-Meteo MCP, then reuse their SQL."""
import argparse
import json
import math
import os
import re
import time
from datetime import datetime, timezone
from pathlib import Path

from dataset import CASES
from mcp_reference import HISTORICAL, QUERY, WeatherMCP, historical_args
from study import (AUTHOR_CASE, MODEL, TASK, TRIALS, TURN_LIMIT, answer_json, compact,
                   completion, digest, function, grade, write)

HERE = Path(__file__).resolve().parent
SQL_TOOL = function("query_weather", (
    "Execute a read-only DuckDB query against the staged Open-Meteo data. Use {{table}} "
    "as the table identifier and :threshold as the numeric input parameter. The table "
    "has time (VARCHAR), temperature_2m_max (numeric, degrees Celsius) and temperature_2m "
    "(numeric). Daily rows have ISO dates in time and the daily maximum in temperature_2m_max; "
    "hourly rows have ISO datetimes in time and null in temperature_2m_max. The host returns "
    "the query rows. Use one SELECT or WITH statement."
), {"sql": {"type": "string"}}, ["sql"])
INSTRUCTION = (
    "Author and execute a reusable SQL query. The host staged the archive for this input "
    "and will substitute the table identifier and threshold parameter. Have the SQL return "
    "one row with columns start, end, days and peak_c, including the no-spell case. "
    "We save the last executed SQL unchanged for other locations, date ranges and thresholds. "
    "Return the answer as JSON."
)


def stage(client, case):
    response, stats = client.call(HISTORICAL, historical_args(case, sql=True))
    data = response["structuredContent"]
    return data["canvas_id"], data["table_name"], stats


def run_query(client, source, canvas, table, threshold):
    # The two substitutions are host data. The model never sees the canvas handle.
    if not re.fullmatch(r"[a-zA-Z_][a-zA-Z_0-9]*", table) or not math.isfinite(threshold):
        raise ValueError("unexpected table identifier or threshold")
    sql = source.replace("{{table}}", table).replace(":threshold", str(threshold))
    if not re.match(r"\s*(select|with)\b", sql, re.I) or ";" in sql.strip().rstrip(";"):
        raise ValueError("expected a single read-only query")
    result, stats = client.call(QUERY, {"canvas_id": canvas, "sql": sql})
    payload = result["structuredContent"]
    if payload.get("error"):
        raise ValueError(str(payload["error"]))
    return payload["rows"], stats


def safe_message(exc, canvas, table):
    return str(exc).replace(canvas, "<canvas>").replace(table, "{{table}}")[:1800]


def grade_rows(rows, case):
    """DuckDB BIGINTs may arrive as decimal strings, dates as midnight timestamps."""
    if not isinstance(rows, list) or len(rows) != 1 or not isinstance(rows[0], dict):
        return False
    facts = dict(rows[0])
    days = facts.get("days")
    if isinstance(days, str) and re.fullmatch(r"0|[1-9][0-9]*", days):
        facts["days"] = int(days)
    for key in ("start", "end"):
        value = facts.get(key)
        if isinstance(value, str):
            midnight = re.fullmatch(r"(\d{4}-\d\d-\d\d)[T ]00:00:00(?:\.0+)?(?:Z|[+]00:00)?", value)
            if midnight:
                facts[key] = midnight[1]
    return grade(facts, case)


def measure(folder, trial):
    messages = [{"role": "system", "content": "Use the provided tools and respond with the requested JSON."},
                {"role": "user", "content": TASK + INSTRUCTION + " Input: " + compact(CASES[AUTHOR_CASE])}]
    client = WeatherMCP()
    clock = time.perf_counter()
    report = {"arm": "community-sql", "case": AUTHOR_CASE, "trial": trial, "model": MODEL,
              "started_at": datetime.now(timezone.utc).isoformat(), "input_tokens": 0,
              "cached_input_tokens": 0, "output_tokens": 0, "turns": [], "calls": [], "answer": None}
    client.initialize()
    canvas, table, stats = stage(client, AUTHOR_CASE)
    report.update(server=client.info, stage=stats)
    code = None
    for _ in range(TURN_LIMIT):
        response = completion(messages, [SQL_TOOL])
        if response["model"] != MODEL:
            raise ValueError("unexpected model")
        usage = response["usage"]
        counts = {"input_tokens": usage["prompt_tokens"], "output_tokens": usage["completion_tokens"],
                  "cached_input_tokens": (usage.get("prompt_tokens_details") or {}).get("cached_tokens", 0)}
        for key, value in counts.items():
            report[key] += value
        choice = response["choices"][0]
        report["turns"].append({**counts, "finish_reason": choice["finish_reason"]})
        msg = choice["message"]
        calls = msg.get("tool_calls") or []
        if not calls:
            report["answer"] = answer_json(msg.get("content"))
            break
        messages.append({"role": "assistant", "content": msg.get("content"), "tool_calls": calls})
        for item in calls:
            call = {"name": item["function"]["name"]}
            try:
                if call["name"] != "query_weather":
                    raise ValueError("unknown tool")
                code = json.loads(item["function"]["arguments"])["sql"]
                call["source_sha256"] = digest(code)
                rows, stats = run_query(client, code, canvas, table, CASES[AUTHOR_CASE]["threshold"])
                call.update(rows=rows, ok=True, **stats)
                content = compact(rows)
            except (ValueError, KeyError, RuntimeError, OSError) as exc:
                error = safe_message(exc, canvas, table)
                call.update(ok=False, error=error)
                content = compact({"error": error})
            call["model_reply_bytes"] = len(content.encode())
            report["calls"].append(call)
            messages.append({"role": "tool", "tool_call_id": item["id"], "content": content})
    report.update(elapsed_seconds=round(time.perf_counter() - clock, 3), correct=grade(report["answer"], AUTHOR_CASE))
    if code is not None:
        (folder / f"community-sql-{trial}.sql").write_bytes(code.encode())
        report["program_sha256"] = digest(code)
    write(folder / f"community-sql-{trial}-phoenix.json", report)
    print("SQL author", trial, report["correct"], len(report["calls"]), flush=True)


def replay(folder):
    report = {}
    for trial in TRIALS:
        receipt = json.loads((folder / f"community-sql-{trial}-phoenix.json").read_text())
        if "program_sha256" not in receipt:
            report[str(trial)] = [{"case": case, "correct": False, "error": "No saved SQL"} for case in CASES]
            continue
        code = (folder / f"community-sql-{trial}.sql").read_bytes().decode()
        if digest(code) != receipt["program_sha256"]:
            raise ValueError("SQL differs from the enrolled program")
        runs = []
        for case, inp in CASES.items():
            clock = time.perf_counter()
            client = WeatherMCP()
            client.initialize()
            canvas, table, stats = stage(client, case)
            row = {"case": case, "stage": stats, "rows": None, "correct": False}
            try:
                rows, stats = run_query(client, code, canvas, table, inp["threshold"])
                row.update(query=stats, rows=rows, correct=grade_rows(rows, case))
            except (ValueError, RuntimeError, OSError) as exc:
                row["error"] = safe_message(exc, canvas, table)
            row["elapsed_seconds"] = round(time.perf_counter() - clock, 3)
            print("SQL replay", trial, case, row["correct"], flush=True)
            runs.append(row)
        report[str(trial)] = runs
    return report


def verify(folder):
    archived = json.loads((folder / "community-sql-replays.json").read_text())
    if set(archived) != {str(trial) for trial in TRIALS}:
        raise ValueError("wrong number of enrolled SQL sources")
    correct = 0
    for trial in TRIALS:
        receipt = json.loads((folder / f"community-sql-{trial}-phoenix.json").read_text())
        if (receipt["model"], receipt["trial"], receipt["case"]) != (MODEL, trial, AUTHOR_CASE):
            raise ValueError("incorrect SQL trial metadata")
        if receipt["correct"] != grade(receipt["answer"], AUTHOR_CASE):
            raise ValueError("SQL author's recorded answer grade differs from the oracle")
        for key in ("input_tokens", "output_tokens", "cached_input_tokens"):
            if receipt[key] != sum(turn[key] for turn in receipt["turns"]):
                raise ValueError("token count disagrees with per-turn receipts")
        sources = [c["source_sha256"] for c in receipt["calls"] if "source_sha256" in c]
        if sources:
            source = (folder / f"community-sql-{trial}.sql").read_bytes().decode()
            if digest(source) != sources[-1] or digest(source) != receipt.get("program_sha256"):
                raise ValueError("saved SQL differs from the last submitted program")
        runs = archived[str(trial)]
        if [r["case"] for r in runs] != list(CASES):
            raise ValueError("expected all three SQL replay cases")
        for run in runs:
            if run["correct"] != grade_rows(run.get("rows"), run["case"]):
                raise ValueError("recorded SQL replay grade differs from the oracle")
            correct += run["correct"]
    print(f"Verified {len(archived)} SQL receipts and {len(archived) * len(CASES)} archived answers ({correct} correct).")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["measure", "replay", "verify"])
    parser.add_argument("--out", type=Path, default=HERE / "results")
    args = parser.parse_args()
    if args.action == "measure":
        if not os.getenv("OPENAI_API_KEY"):
            parser.error("measure requires OPENAI_API_KEY")
        if any(args.out.glob("community-sql-*")):
            parser.error("choose a directory without enrolled SQL runs")
        args.out.mkdir(exist_ok=True, parents=True)
        for trial in TRIALS:
            measure(args.out, trial)
        write(args.out / "community-sql-replays.json", replay(args.out))
        verify(args.out)
    elif args.action == "replay":
        print(json.dumps(replay(args.out), indent=2))
    else:
        verify(args.out)


if __name__ == "__main__":
    main()
