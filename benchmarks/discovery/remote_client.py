"""Measure the release audit from a separate HTTPS client and verify both measurement records."""
import argparse
import hashlib
import http.client
import json
import math
import os
import platform
import re
import ssl
from datetime import datetime, timezone
from pathlib import Path
from statistics import median
from time import monotonic, perf_counter_ns, sleep
from urllib.parse import urlsplit

from dataset import CASES, grounded
from study import ARMS, HERE, TRIALS, correct

ROUNDS = 4
EXPECTED_PAIRS = ROUNDS * len(TRIALS) * len(CASES)


def schedule():
    cells = [(trial, case) for trial in TRIALS for case in CASES]
    for iteration in range(1, ROUNDS + 1):
        for trial, case in (cells if iteration % 2 else reversed(cells)):
            i = list(CASES).index(case)
            order = ARMS if (trial + i + iteration) % 2 == 0 else ARMS[::-1]
            for arm in order:
                yield iteration, trial, case, arm


def utc():
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds")


def compact(obj):
    return json.dumps(obj, separators=(",", ":"), ensure_ascii=False, allow_nan=False).encode()


def save(path, obj):
    pending = path.with_suffix(path.suffix + ".tmp")
    pending.write_text(json.dumps(obj, indent=2, ensure_ascii=False, allow_nan=False) + "\n")
    pending.replace(path)


def connect(endpoint):
    origin = urlsplit(endpoint)
    if (origin.scheme != "https" or not origin.hostname or origin.username is not None
            or origin.password is not None or origin.path not in ("", "/") or origin.query or origin.fragment):
        raise ValueError("expected a single HTTPS endpoint origin")
    trust = os.getenv("SSL_CERT_FILE") or ssl.get_default_verify_paths().cafile
    if not trust and Path("/etc/ssl/cert.pem").exists():
        trust = "/etc/ssl/cert.pem"
    return http.client.HTTPSConnection(origin.hostname, origin.port, timeout=45,
                                       context=ssl.create_default_context(cafile=trust))


def request(conn, method, path, headers, body=None):
    reused = conn.sock is not None
    start = perf_counter_ns()
    conn.request(method, path, body=body, headers=headers)
    response = conn.getresponse()
    data = response.read(65537)
    elapsed_ms = round((perf_counter_ns() - start) / 1_000_000, 3)
    if len(data) > 65536:
        raise ValueError("oversized benchmark response")
    return response, data, elapsed_ms, reused


def ready(conn, headers, record):
    # A new public tunnel can take a few seconds to reach DNS and the edge.
    deadline = monotonic() + 60
    attempts = 0
    while True:
        attempts += 1
        try:
            response, data, elapsed, _ = request(conn, "GET", "/ready", headers)
            record["last_setup_status"] = response.status
            if response.status == 200 and json.loads(data).get("ready") is True:
                return elapsed, attempts
            if response.status not in (502, 503, 504, 530):
                raise ValueError(f"readiness returned HTTP {response.status}")
        except ssl.SSLCertVerificationError:
            raise
        except (OSError, http.client.HTTPException) as exc:
            record["last_setup_transport_error"] = type(exc).__name__
        if monotonic() >= deadline:
            raise TimeoutError("readiness deadline expired")
        conn.close()
        sleep(2)


def measure(endpoint, token, output):
    if not token:
        raise ValueError("missing bearer token")
    if output.exists():
        raise ValueError("choose a fresh output file to preserve earlier attempts")
    headers = {"Authorization": "Bearer " + token, "Content-Type": "application/json",
               "Accept-Encoding": "identity", "Cache-Control": "no-store"}
    conn = connect(endpoint)
    record = {"started_at": utc(), "platform": platform.system() + "/" + platform.machine(),
              "rounds": ROUNDS, "complete": False, "runs": []}
    save(output, record)
    try:
        stage = "readiness"
        record["setup_request_ms"], record["setup_attempts"] = ready(conn, headers, record)
        for sequence, (iteration, trial, case, arm) in enumerate(schedule(), 1):
            stage = f"audit {sequence} ({arm}, {trial}, {case})"
            body = compact({"arm": arm, "trial": trial, "case": case})
            row = {"round": iteration, "trial": trial, "case": case, "arm": arm, "started_at": utc()}
            response, data, elapsed, reused = request(conn, "POST", "/audit", headers, body)
            row.update(elapsed_ms=elapsed, status=response.status, reused_connection=reused,
                       request_body_bytes=len(body), response_body_bytes=len(data),
                       response_sha256=hashlib.sha256(data).hexdigest())
            record["runs"].append(row)
            if response.status not in (200, 502) or not response.getheader("X-Benchmark-Sequence"):
                raise RuntimeError(f"audit returned HTTP {response.status}")
            worker_header = response.getheader("X-Benchmark-Worker-Ms")
            timing = ([worker_header] if worker_header is not None else
                      re.findall(r"(?:^|,)\s*worker;dur=(\d+(?:\.\d+)?)\s*(?=,|$)",
                                 response.getheader("Server-Timing", "")))
            if len(timing) != 1 or int(response.getheader("X-Benchmark-Sequence", "0")) != sequence:
                raise ValueError("missing server timer or unexpected audit sequence")
            evidence = response.getheader("X-Grounded")
            if evidence not in ("0", "1"):
                raise ValueError("missing server evidence check")
            answer = json.loads(data)
            row.update(sequence=sequence, worker_ms=float(timing[0]), answer=answer,
                       correct=correct(case, answer), grounded=evidence == "1",
                       read_count=int(response.getheader("X-Reads", "-1")))
            save(output, record)
        stage = "finish"
        response, _, _, _ = request(conn, "POST", "/finish", headers, b"")
        if response.status != 200:
            raise RuntimeError(f"finish returned HTTP {response.status}")
        record.update(complete=True, finished_at=utc())
    except Exception as exc:
        # An HTTP proxy or TLS error can quote the ephemeral origin. Keep it out of receipts.
        record["failure"] = {"stage": stage, "kind": type(exc).__name__}
        raise RuntimeError(f"{stage} failed ({type(exc).__name__}); incomplete receipt saved") from None
    finally:
        conn.close()
        save(output, record)


def deltas(pairs, field):
    return [pair["skeiner"][field] - pair["client"][field] for pair in pairs]


def comparison(pairs, field):
    diff = deltas(pairs, field)
    return {"pairs": len(diff), "skeiner_faster": sum(d < 0 for d in diff),
            "client_faster": sum(d > 0 for d in diff), "ties": sum(d == 0 for d in diff),
            "median_skeiner_minus_client_ms": round(median(diff), 3)}


def report(client, host):
    if not client.get("complete") or client["rounds"] != ROUNDS:
        raise ValueError("client measurement incomplete")
    if len(client["runs"]) != 2 * EXPECTED_PAIRS or len(host["runs"]) != len(client["runs"]):
        raise ValueError("incomplete client or host enrollment")
    hashes = {}
    for arm in ARMS:
        for trial in TRIALS:
            name = f"{arm}-{trial}"
            source_hash = hashlib.sha256((HERE / "results" / (name + ".js")).read_bytes()).hexdigest()
            if source_hash != json.loads((HERE / "results" / (name + ".json")).read_text())["program_sha256"]:
                raise ValueError(f"archived source differs from its author receipt: {name}")
            hashes[name] = source_hash
    if host["programs"] != hashes:
        raise ValueError("remote host program hashes disagree with the original sources")
    pairs = {}
    for sequence, (entry, row, remote) in enumerate(zip(schedule(), client["runs"], host["runs"]), 1):
        iteration, trial, case, arm = entry
        if (tuple(row[k] for k in ("round", "trial", "case", "arm")) != entry
                or row["sequence"] != sequence or remote["sequence"] != sequence
                or (remote["trial"], remote["case"], remote["arm"]) != (trial, case, arm)
                or remote["program_sha256"] != hashes[f"{arm}-{trial}"]):
            raise ValueError(f"pairing or source differs at request {sequence}")
        matches = correct(case, row["answer"])
        evidence = grounded(case, remote["reads"])
        canonical = json.dumps(remote["answer"], ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()
        if (row["status"] != (200 if remote["ok"] else 502) or row["answer"] != remote["answer"]
                or row["response_sha256"] != hashlib.sha256(canonical).hexdigest()
                or row["response_body_bytes"] != len(canonical)
                or row["correct"] != matches or remote["correct"] != matches
                or row["grounded"] != evidence or remote["grounded"] != evidence
                or row["read_count"] != len(remote["reads"])
                or not math.isfinite(row["elapsed_ms"]) or row["elapsed_ms"] < 0
                or not math.isfinite(row["worker_ms"]) or row["worker_ms"] < 0
                or abs(row["worker_ms"] - remote["worker_ms"]) > 0.001
                or row["worker_ms"] > row["elapsed_ms"] + 0.01):
            raise ValueError(f"host/client evidence or timer disagreement at request {sequence}")
        pairs.setdefault((iteration, trial, case), {})[arm] = row
    arms = {}
    for arm in ARMS:
        rows = [p[arm] for p in pairs.values()]
        arms[arm] = {
            "samples": len(rows), "correct_grounded": sum(r["correct"] and r["grounded"] for r in rows),
            "upstream_reads": sum(r["read_count"] for r in rows),
            "median_client_elapsed_ms": round(median(r["elapsed_ms"] for r in rows), 3),
            "median_worker_ms": round(median(r["worker_ms"] for r in rows), 3),
            "median_elapsed_outside_worker_ms": round(median(r["elapsed_ms"] - r["worker_ms"] for r in rows), 3),
            "audits_on_reused_connection": sum(r["reused_connection"] for r in rows),
        }
    return {"arms": arms, "remote_client": comparison(pairs.values(), "elapsed_ms"),
            "worker": comparison(pairs.values(), "worker_ms"),
            "remote_client_by_round": [comparison([v for k, v in pairs.items() if k[0] == i], "elapsed_ms")
                                       for i in range(1, ROUNDS + 1)]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    cmd = parser.add_subparsers(dest="command", required=True)
    run = cmd.add_parser("measure")
    run.add_argument("--endpoint-file", type=Path, help="file containing the HTTPS origin (or REMOTE_BENCH_URL)")
    run.add_argument("--token-file", type=Path, help="private file containing the bearer (or REMOTE_BENCH_TOKEN)")
    run.add_argument("--out", type=Path, required=True)
    check = cmd.add_parser("report")
    check.add_argument("client", type=Path)
    check.add_argument("--host", type=Path, required=True)
    args = parser.parse_args()
    if args.command == "report":
        print(json.dumps(report(json.loads(args.client.read_text()), json.loads(args.host.read_text())), indent=2))
    else:
        endpoint = args.endpoint_file.read_text().strip() if args.endpoint_file else os.getenv("REMOTE_BENCH_URL", "")
        token = args.token_file.read_text().strip() if args.token_file else os.getenv("REMOTE_BENCH_TOKEN", "")
        measure(endpoint, token, args.out)


if __name__ == "__main__":
    main()
