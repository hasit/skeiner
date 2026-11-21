"""Run the first enrolled Skeiner program and the direct client on today's archive."""
import argparse
import hashlib
import json
import os
import subprocess
import tempfile
import time
from datetime import datetime, timezone
from pathlib import Path

from dataset import CASES, expected, fetch, native

HERE = Path(__file__).resolve().parent
ARCHIVE = "https://archive-api.open-meteo.com"


def fingerprint(series):
    payload = json.dumps(series, separators=(",", ":"), sort_keys=True, allow_nan=False)
    return hashlib.sha256(payload.encode()).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=HERE.parents[1] / "skeiner")
    args = parser.parse_args()
    program = HERE / "results/skeiner-1.js"
    receipt = json.loads((HERE / "results/skeiner-1-phoenix.json").read_text())
    if hashlib.sha256(program.read_bytes()).hexdigest() != receipt["program_sha256"]:
        raise ValueError("live replay source differs from the first enrolled program")
    report = {"observed_at": datetime.now(timezone.utc).isoformat(), "program": program.name,
              "program_sha256": receipt["program_sha256"], "cases": {}}
    with tempfile.TemporaryDirectory() as temporary:
        input_file = Path(temporary) / "input.json"
        for case, inputs in CASES.items():
            start = time.perf_counter()
            raw = fetch(case, live=True)
            direct = native(raw, inputs["threshold"])
            direct_seconds = round(time.perf_counter() - start, 3)
            series_hash = fingerprint(raw["daily"])
            frozen = fetch(case)
            if raw["daily"] != frozen["daily"]:
                raise ValueError(f"upstream daily data changed for {case}, recheck the oracle")
            input_file.write_text(json.dumps(inputs))
            command = [str(args.binary.resolve()), "run", "--spec", str(HERE / "openapi.yaml"),
                       "--base-url", ARCHIVE, "--allow", "getDailyWeather", "--token-env", "",
                       "--input-file", str(input_file), "--program", str(program)]
            if receipt.get("saved_mode"):
                command.extend(["--mode", receipt["saved_mode"]])
            start = time.perf_counter()
            run = subprocess.run(command, env={"PATH": os.defpath}, capture_output=True,
                                 text=True, timeout=45, check=True)
            elapsed = round(time.perf_counter() - start, 3)
            answer = json.loads(run.stdout)
            reference = expected(case)
            report["cases"][case] = {"daily_sha256": series_hash, "days": len(raw["daily"]["time"]),
                                     "native": direct, "native_seconds": direct_seconds,
                                     "native_correct": direct == reference, "skeiner": answer,
                                     "skeiner_seconds": elapsed, "skeiner_correct": answer == reference}
    print(json.dumps(report, indent=2, allow_nan=False))
    return all(row["native_correct"] and row["skeiner_correct"] for row in report["cases"].values())


if __name__ == "__main__":
    raise SystemExit(0 if main() else 1)
