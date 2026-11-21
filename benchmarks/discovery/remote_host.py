"""Serve the six archived release audits with both workers beside the same gateway."""
import argparse
import hashlib
import hmac
import json
import os
import platform
import re
import subprocess
import threading
from contextlib import ExitStack
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from time import monotonic, perf_counter

from client import warm_session
from dataset import CASES, grounded
from live import github_gateway
from study import ARMS, HERE, ROOT, TRIALS, answer_json, correct, sha, toolset


def timestamp():
    return datetime.now(timezone.utc).isoformat()


def encode(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False).encode()


def save(path, receipt):
    temp = path.with_suffix(path.suffix + ".tmp")
    temp.write_bytes(encode(receipt) + b"\n")
    temp.replace(path)


def workers(stack, binary, base):
    enrolled = {}
    for trial in TRIALS:
        _, mcp = stack.enter_context(toolset("skeiner", binary, base))
        deno = stack.enter_context(warm_session(base))
        for arm in ARMS:
            name = f"{arm}-{trial}"
            record = json.loads((HERE / "results" / f"{name}.json").read_text())
            body = (HERE / "results" / f"{name}.js").read_bytes().decode()
            digest = sha(body)
            if digest != record["program_sha256"]:
                raise ValueError(f"archive source mismatch: {name}")
            args = {"code": body}
            if record.get("saved_mode"):
                args["mode"] = record["saved_mode"]
            call = deno if arm == "client" else lambda value, m=mcp: m("execute", value)
            warm, ok = call({"code": "return true;", "input": {}})
            if not ok or answer_json(warm) is not True:
                raise RuntimeError(f"worker warmup failed: {name}")
            enrolled[arm, trial] = (call, args, digest)
    return enrolled


def serve(args, token_hash):
    with github_gateway() as (base, reads, _), ExitStack() as stack:
        enrolled = workers(stack, args.binary.resolve(), base)
        receipt = {
            "started_at": timestamp(), "platform": platform.system() + "/" + platform.machine(),
            "commit": subprocess.check_output(["git", "-C", str(ROOT), "rev-parse", "HEAD"], text=True).strip(),
            "programs": {f"{arm}-{trial}": w[2] for (arm, trial), w in enrolled.items()}, "runs": [],
        }
        save(args.receipt, receipt)
        deadline = monotonic() + args.seconds
        done = threading.Event()
        active = threading.Lock()

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"
            timeout = 10

            def log_message(self, *_):
                pass

            def reply(self, status, value, headers=None):
                body = encode(value)
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Cache-Control", "no-store")
                self.send_header("Content-Length", str(len(body)))
                if status >= 400:
                    self.send_header("Connection", "close")
                    self.close_connection = True
                for name, val in (headers or {}).items():
                    self.send_header(name, str(val))
                self.end_headers()
                self.wfile.write(body)

            def authenticated(self):
                scheme, _, token = self.headers.get("Authorization", "").partition(" ")
                if scheme == "Bearer" and token and hmac.compare_digest(
                        hashlib.sha256(token.encode()).hexdigest(), token_hash):
                    return True
                self.reply(401, {"error": "authorization required"})
                return False

            def do_GET(self):
                if self.authenticated():
                    if self.path == "/ready":
                        self.reply(200, {"ready": True})
                    else:
                        self.reply(404, {"error": "unknown route"})

            def do_POST(self):
                if not self.authenticated():
                    return
                if self.path != "/audit":
                    self.reply(404, {"error": "unknown route"})
                    return
                length = self.headers.get("Content-Length", "0")
                if self.headers.get("Transfer-Encoding") or not re.fullmatch(r"[0-9]{1,3}", length):
                    self.reply(400, {"error": "invalid content length"})
                    return
                try:
                    request = json.loads(self.rfile.read(int(length)))
                except (ValueError, UnicodeDecodeError):
                    self.reply(400, {"error": "invalid JSON"})
                    return
                if (not isinstance(request, dict) or set(request) != {"arm", "trial", "case"}
                        or request["arm"] not in ARMS or type(request["trial"]) is not int
                        or request["trial"] not in TRIALS or not isinstance(request["case"], str)
                        or request["case"] not in CASES):
                    self.reply(400, {"error": "expected an enrolled arm, trial and case"})
                    return
                # Serialize access to the reusable worker and shared per-audit upstream read log.
                with active:
                    if done.is_set() or monotonic() >= deadline:
                        self.reply(410, {"error": "experiment has ended"})
                        return
                    arm, trial, case = request["arm"], request["trial"], request["case"]
                    call, program, digest = enrolled[arm, trial]
                    seq = len(receipt["runs"]) + 1
                    reads.clear()
                    began = timestamp()
                    start = perf_counter()
                    try:
                        raw, ok = call({**program, "input": CASES[case]})
                    except (OSError, RuntimeError, ValueError):
                        raw, ok = "", False
                    worker_ms = round((perf_counter() - start) * 1000, 3)
                    answer = answer_json(raw) if ok else None
                    row = {**request, "sequence": seq, "timestamp": began, "program_sha256": digest,
                           "worker_ms": worker_ms, "ok": ok, "answer": answer, "correct": correct(case, answer),
                           "grounded": grounded(case, reads), "reads": list(reads)}
                    receipt["runs"].append(row)
                    try:
                        duration = f"{worker_ms:.3f}"
                        self.reply(200 if ok else 502, answer, {"Server-Timing": f"worker;dur={duration}",
                                   "X-Benchmark-Worker-Ms": duration,
                                   "X-Grounded": int(row["grounded"]), "X-Reads": len(reads),
                                   "X-Benchmark-Sequence": seq})
                        self.wfile.flush()
                    finally:
                        # Preserve the sample even if its response was lost in transit.
                        save(args.receipt, receipt)
                        if seq >= args.requests:
                            done.set()

        with ThreadingHTTPServer(("127.0.0.1", args.port), Handler) as server:
            server.timeout = 0.25
            args.ready_file.write_text(str(server.server_port) + "\n")
            while not done.is_set() and monotonic() < deadline:
                server.handle_request()
            done.set()
            # Let any admitted audit flush its response before shutting down the two workers.
            with active:
                receipt["ended_at"] = timestamp()
                save(args.receipt, receipt)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=ROOT / "skeiner")
    parser.add_argument("--port", type=int, default=8765)
    parser.add_argument("--requests", type=int, choices=range(1, 121), default=120, metavar="1..120")
    parser.add_argument("--seconds", type=int, default=600)
    parser.add_argument("--receipt", type=Path, required=True)
    parser.add_argument("--ready-file", type=Path, required=True)
    args = parser.parse_args()
    token_hash = os.getenv("BENCH_TOKEN_SHA256", "")
    if not re.fullmatch(r"[0-9a-f]{64}", token_hash) or not 1 <= args.seconds <= 900:
        parser.error("set BENCH_TOKEN_SHA256 to a lowercase SHA-256 digest and use a 1–900 second lifetime")
    serve(args, token_hash)


if __name__ == "__main__":
    main()
