"""Measure incremental response-schema discovery work on the nine frozen primary setups."""
import argparse
import json
import os
import subprocess
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import acceptance
import output_discovery
from report import cost
from spec import DEFAULT_SPEC, verify
from study import ARMS, HERE, REFS, TASK, measure, pretty, replay, save, setup_check, sha

BRIEF = """You are taking over an existing working custom worker. Its frozen launch and source are
below. They already pass the original task. The new requirement is to publish both successful
response shapes through ordinary MCP discovery before any upstream request. A client must
be able to tell that the list returns an array of vault objects and the detail returns one
vault object. It must be able to discover all nine documented property names/types and all
four enum values of the vault type. On restart with an additional optional boolean vault
property in the spec, discovery for both operations must reflect that change automatically.

For named tools you may embed the response JSON Schema in the ordinary tool description, use
_meta.responseSchema, or use native MCP outputSchema (a single-property object wrapper for
an array is accepted). Publish enough information to resolve any local schema references
using the MCP metadata alone. Preserve input discovery, dispatch, return values and the
original host/policy behavior. Submit the complete revised worker and configuration for
the original and new acceptance checks. Start from the supplied worker and make only the
changes you need."""
TRIALS = range(1, 4)


def load_json(path):
    return json.loads(path.read_text())


def frozen(folder):
    row = load_json(folder / "receipt.json")
    setup = load_json(folder / "launch.json")
    if (folder / "worker.js").exists():
        setup["worker"] = (folder / "worker.js").read_text()
    if not row["passed"] or setup != row["attempts"][-1]["submission"]:
        raise ValueError(f"Expected a verified primary setup: {folder.name}")
    return setup


def probe(originals, binary, spec):
    results = {}
    for arm in ARMS:
        for trial in TRIALS:
            key = f"{arm}-{trial}"
            setup = frozen(originals / key)
            results[key] = setup_check(arm, setup, binary, spec, output_discovery.check)
            grade = results[key]
            print(key, f"{grade['checks_passed']}/{grade['checks_total']}", flush=True)
    return results


def eligible(probes):
    return [i for i in TRIALS if not probes[f"custom-{i}"]["passed"]]


def full_check(launch, spec):
    primary = acceptance.check(launch, spec)
    outputs = output_discovery.check(launch, spec)
    return {"passed": primary["passed"] and outputs["passed"],
            "checks_passed": primary["checks_passed"] + outputs["checks_passed"],
            "checks_total": primary["checks_total"] + outputs["checks_total"],
            "primary": primary, "outputs": outputs}


def prompt_for(setup):
    return TASK + "\n" + REFS["custom"] + "\n" + BRIEF + "\nExisting launch:\n" + pretty(
        {k: v for k, v in setup.items() if k != "worker"}) + "\nExisting worker.js:\n" + setup["worker"]


def summarize(originals, out, trials):
    rows = []
    for i in trials:
        key = f"custom-{i}"
        prior = load_json(originals / key / "receipt.json")
        row = load_json(out / key / "receipt.json")
        if row["prompt_sha256"] != sha(prompt_for(frozen(originals / key))):
            raise ValueError(f"Working files differ from the revision author's input: {key}")
        turns = row["turns"]
        detail = {"trial": i, "passed": row["passed"], "stop": row["stop"],
                  "elapsed_seconds": row["elapsed_seconds"], "turns": len(turns),
                  "submissions": len(row["attempts"]),
                  "input_tokens": sum(t["usage"]["input_tokens"] for t in turns),
                  "output_tokens": sum(t["usage"]["output_tokens"] for t in turns),
                  "modeled_usd": round(cost(turns), 6), "before": prior["final_metrics"]}
        if row["attempts"]:
            final = row["final_metrics"]
            detail.update(after=final, delta={k: v - prior["final_metrics"][k] for k, v in final.items()})
        rows.append(detail)
    return rows


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("action", choices=["probe", "measure", "replay", "report"])
    p.add_argument("--originals", type=Path, default=HERE / "results")
    p.add_argument("--out", type=Path, default=HERE / "output-results")
    p.add_argument("--binary", type=Path, default=HERE.parents[1] / "bin/setup/skeiner")
    p.add_argument("--spec", type=Path, default=DEFAULT_SPEC)
    p.add_argument("--write-replay", type=Path)
    args = p.parse_args()
    verify(args.spec)
    receipt = args.out / "baseline.json"
    if args.action == "probe":
        if args.out.exists():
            p.error("Use a new output directory for a baseline probe")
        binary = args.binary.resolve(strict=True)
        rows = probe(args.originals, binary, args.spec)
        args.out.mkdir(parents=True)
        save(receipt, rows)
        return 0
    baseline = load_json(receipt)
    trials = eligible(baseline)
    if args.action == "report":
        print(pretty(summarize(args.originals, args.out, trials)), end="")
        return 0
    binary = args.binary.resolve(strict=True)
    if args.action == "replay":
        if args.write_replay and args.write_replay.exists():
            p.error("Choose a new replay output file")
        if probe(args.originals, binary, args.spec) != baseline:
            p.exit(1, "Frozen primary setups now differ from the archived discovery probe\n")
        if {p.parent.name for p in args.out.glob("*/receipt.json")} != {f"custom-{i}" for i in trials}:
            p.exit(1, "Missing or unexpected revision receipt\n")
        return 0 if replay(args.out, binary, args.spec, args.write_replay, checker=full_check) else 1
    if (args.write_replay or any(args.out.glob("custom-*")) or "OPENAI_API_KEY" not in os.environ
            or subprocess.check_output([str(binary), "version"], text=True).strip() != "0.1.0-alpha.2"):
        p.error("Revision measurement requires the release, a fresh baseline and OPENAI_API_KEY")
    with ThreadPoolExecutor(max_workers=3) as pool:
        jobs = [pool.submit(measure, "custom", i, args.out, binary, args.spec,
                            prompt=prompt_for(frozen(args.originals / f"custom-{i}")), checker=full_check) for i in trials]
        for job in jobs:
            job.result()
    save(args.out / "metrics.json", summarize(args.originals, args.out, trials))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
