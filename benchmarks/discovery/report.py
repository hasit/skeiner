"""Calculate study outcomes and matched live latency differences from the published receipts."""
import argparse
import json
from pathlib import Path
from statistics import mean, median

from study import ARMS, HERE, MODEL, TRIALS


def read(path):
    return json.loads(path.read_text())


def authoring():
    folder = HERE / "results"
    replays = read(folder / "replays.json")
    report = {}
    for arm in ARMS:
        rows = []
        cold_ms = []
        for trial in TRIALS:
            name = f"{arm}-{trial}"
            receipt = read(folder / (name + ".json"))
            runs = replays[name]
            cold_ms.extend(row["elapsed_seconds"] * 1000 for row in runs if row["elapsed_seconds"] is not None)
            inputs = outputs = 0
            for turn in receipt["turns"]:
                usage = turn["usage"]
                cache = usage["input_tokens_details"]
                if turn["model"] != MODEL or cache["cached_tokens"] or cache["cache_write_tokens"]:
                    raise ValueError("expected the enrolled model and uncached usage")
                inputs += usage["input_tokens"]
                outputs += usage["output_tokens"]
            rows.append({"trial": trial, "usd": (8 * inputs + 40 * outputs) / 1_000_000,
                         "author_seconds": receipt["elapsed_seconds"], "input_tokens": inputs,
                         "output_tokens": outputs, "correct_grounded": sum(r["correct"] and r["grounded"] for r in runs),
                         "author_reads": len(receipt["reads"]), "replay_reads": sum(len(r["reads"]) for r in runs),
                         "searches": sum(c["name"] == "search" for c in receipt["calls"]),
                         "tool_errors": sum(not c["ok"] for c in receipt["calls"])})
        report[arm] = {"trials": rows, "mean_usd": round(mean(row["usd"] for row in rows), 6),
                       "median_author_seconds": median(row["author_seconds"] for row in rows),
                       "median_cold_ms": median(cold_ms)}
    return report


def live(path):
    runs = read(path)["runs"]
    pairs = {}
    for r in runs:
        pair = pairs.setdefault((r["round"], r["trial"], r["case"]), {})
        if r["arm"] in pair:
            raise ValueError(f"duplicate arm for {pair}")
        pair[r["arm"]] = r
    if any(set(pair) != set(ARMS) for pair in pairs.values()):
        raise ValueError("incomplete live pair")
    deltas = [p["skeiner"]["elapsed_ms"] - p["client"]["elapsed_ms"] for p in pairs.values()
              if all(r["correct"] and r["grounded"] for r in p.values())]
    report = {arm: {"samples": len(pairs),
                    "correct_grounded": sum(p[arm]["correct"] and p[arm]["grounded"] for p in pairs.values()),
                    "reads": sum(len(p[arm]["reads"]) for p in pairs.values())} for arm in ARMS}
    report.update(successful_pairs=len(deltas), skeiner_faster_pairs=sum(d < 0 for d in deltas),
                  median_skeiner_minus_client_ms=round(median(deltas), 2) if deltas else None)
    return report


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("live_files", type=Path, nargs="*", help="optional live measurement JSON files")
    args = parser.parse_args()
    print(json.dumps({"authoring": authoring(), "live": {p.name: live(p) for p in args.live_files}}, indent=2))
