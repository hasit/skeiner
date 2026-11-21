"""Summarize the nine enrolled setup authors from their recorded API usage."""
import argparse
import json
from pathlib import Path
from statistics import mean, median

HERE = Path(__file__).resolve().parent
ARMS = ("skeiner", "package", "custom")


def cost(turns):
    # Standard short-context GPT-6.2 Astra prices in USD per million tokens.
    total = 0
    for turn in turns:
        usage = turn["usage"]
        detail = usage["input_tokens_details"]
        if (turn["model"] != "gpt-6.2-astra" or turn["service_tier"] != "default"
                or usage["input_tokens"] > 272_000 or detail["cached_tokens"] or detail["cache_write_tokens"]):
            raise ValueError("Unexpected pricing tier, model, context length or cache usage")
        total += 8 * usage["input_tokens"] + 40 * usage["output_tokens"]
    return total / 1_000_000


def summarize(folder):
    names = {f"{arm}-{trial}" for arm in ARMS for trial in range(1, 4)}
    if {p.parent.name for p in folder.glob("*/receipt.json")} != names:
        raise ValueError("Expected one receipt for each of the nine enrolled authors")
    result = []
    for arm in ARMS:
        reports = []
        for trial in range(1, 4):
            report = json.loads((folder / f"{arm}-{trial}" / "receipt.json").read_text())
            if (report["arm"], report["trial"]) != (arm, trial) or report.get("stop") == "infrastructure_error":
                raise ValueError(f"Incomplete or misfiled enrollment: {arm}-{trial}")
            reports.append(report)
        passed = [r for r in reports if r["passed"]]
        metrics = [r["final_metrics"] for r in passed]
        result.append({
            "arm": arm,
            "enrolled": len(reports),
            "passed": len(passed),
            "first_submission_passed": sum(bool(r["attempts"] and r["attempts"][0]["grade"]["passed"])
                                           for r in reports),
            "submissions": [len(r["attempts"]) for r in reports],
            "seconds_to_pass_median": median(r["elapsed_seconds"] for r in passed) if passed else None,
            "elapsed_seconds_all": [r["elapsed_seconds"] for r in reports],
            "mean_usd_per_author": round(mean(cost(r["turns"]) for r in reports), 6),
            "passing_config_bytes": [m["config_bytes"] for m in metrics],
            "passing_worker_nonblank_lines": [m["source_nonblank_lines"] for m in metrics],
            "passing_worker_bytes": [m["source_bytes"] for m in metrics],
        })
    return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, default=HERE / "results", help="directory of recorded trials")
    print(json.dumps(summarize(parser.parse_args().out), indent=2))
