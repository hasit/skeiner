"""Summarize the enrolled conversations at GPT-4.1 mini standard list prices."""
import argparse
import json
from pathlib import Path
from statistics import median

from dataset import CASES

HERE = Path(__file__).resolve().parent
MODEL = "gpt-4.1-mini-2025-04-14"


def usd(row):
    if row["model"] != MODEL:
        raise ValueError(f"no price configured for {row['model']}")
    cached = row["cached_input_tokens"]
    uncached = row["input_tokens"] - cached
    return (0.40 * uncached + 0.10 * cached + 1.60 * row["output_tokens"]) / 1_000_000


def summarize(directory, arm, replays):
    first_costs = []
    total_costs = []
    elapsed = []
    answers = []
    verified = []
    turns = 0
    for trial in range(1, 4):
        cases = CASES if arm == "direct" else ["phoenix"]
        receipts = [json.loads((directory / f"{arm}-{trial}-{case}.json").read_text()) for case in cases]
        first_costs.append(usd(receipts[0]))
        total_costs.append(sum(usd(row) for row in receipts))
        seconds = sum(row["elapsed_seconds"] for row in receipts)
        turns += sum(row["model_turns"] for row in receipts)
        answers.append(receipts[0]["correct"])
        if arm == "direct":
            verified.extend(row["correct"] for row in receipts)
        else:
            runs = replays[f"{arm}-{trial}"]
            if [row["case"] for row in runs] != list(CASES):
                raise ValueError(f"expected a replay of every case for {arm} trial {trial}")
            verified.extend(row["correct"] for row in runs)
            seconds += sum(row["elapsed_seconds"] for row in runs if row["elapsed_seconds"] is not None)
        elapsed.append(seconds)
    return {"exact_first_model_answers": sum(answers), "correct_city_answers": sum(verified),
            "city_answers": len(verified), "model_turns": turns,
            "mean_first_usd": round(sum(first_costs) / 3, 7),
            "mean_three_city_usd": round(sum(total_costs) / 3, 7),
            "three_city_usd_by_trial": [round(cost, 7) for cost in total_costs],
            "median_three_city_seconds": round(median(elapsed), 3)}


def summarize_ten(directory, extension, primary):
    direct = json.loads((extension / "direct.json").read_text())
    replays = json.loads((extension / "replays.json").read_text())
    result = {}
    for arm in ("direct", "skeiner", "reducer"):
        totals = []
        new_answers = []
        for trial in range(1, 4):
            if arm == "direct":
                first = [json.loads((directory / f"direct-{trial}-{case}.json").read_text()) for case in CASES]
                new = [row for row in direct if row["trial"] == trial]
                cost = sum(usd(row) for row in first + new)
            else:
                filename = f"{arm}-{trial}-phoenix.json"
                first = json.loads((directory / filename).read_text())
                enrolled = json.loads((HERE / "results" / filename).read_text())
                if first["program_sha256"] != enrolled["program_sha256"]:
                    raise ValueError("the seven-city extension uses the original enrolled programs")
                new = replays[f"{arm}-{trial}"]
                cost = usd(first)
            if len(new) != 7:
                raise ValueError(f"expected seven new city results for {arm} trial {trial}")
            totals.append(cost)
            new_answers.extend(row["correct"] for row in new)
        result[arm] = {"correct_new_answers": sum(new_answers), "new_answers": len(new_answers),
                       "correct_ten_city_answers": primary[arm]["correct_city_answers"] + sum(new_answers),
                       "ten_city_answers": primary[arm]["city_answers"] + len(new_answers),
                       "mean_ten_city_usd": round(sum(totals) / len(totals), 7),
                       "ten_city_usd_by_trial": [round(cost, 7) for cost in totals]}
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dir", type=Path, default=HERE / "results")
    parser.add_argument("--extension", type=Path, help="include the seven further cities")
    args = parser.parse_args()
    replays = json.loads((args.dir / "replays.json").read_text())
    primary = {arm: summarize(args.dir, arm, replays) for arm in ("direct", "skeiner", "reducer")}
    report = {"three_cities": primary}
    if args.extension:
        report["ten_cities"] = summarize_ten(args.dir, args.extension, primary)
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
