"""Run every enrolled program against live GitHub with both runtimes already started."""
import argparse
import http.client
import json
import os
import platform
import ssl
import sys
import threading
from contextlib import ExitStack, contextmanager
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from queue import Empty, LifoQueue
from time import perf_counter
from urllib.parse import urlsplit

from client import warm_session
from dataset import CASES, fixture, grounded
from study import ARMS, HERE, ROOT, TRIALS, answer_json, correct, sha, toolset

sys.path.insert(0, str(ROOT / "benchmarks/github"))
from official_mcp import github_token


@contextmanager
def github_gateway():
    """Forward only enrolled public paths. Neither JavaScript runtime sees the token."""
    paths = set().union(*(fixture(case) for case in CASES))
    headers = {"User-Agent": "skeiner-discovery-study", "Accept": "application/vnd.github+json",
               "X-GitHub-Api-Version": "2022-11-28", "Authorization": "Bearer " + github_token()}
    trust = os.getenv("SSL_CERT_FILE") or ssl.get_default_verify_paths().cafile
    if not trust and Path("/etc/ssl/cert.pem").exists():
        trust = "/etc/ssl/cert.pem"
    context = ssl.create_default_context(cafile=trust)
    connections = LifoQueue()
    reads = []

    def upstream(target):
        try:
            conn = connections.get_nowait()
        except Empty:
            conn = http.client.HTTPSConnection("api.github.com", timeout=20, context=context)
        try:
            conn.request("GET", target, headers=headers)
            response = conn.getresponse()
            body = response.read()
        except Exception:
            conn.close()
            raise
        connections.put(conn)
        return response.status, body

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            path = urlsplit(self.path).path
            if path in paths:
                status, data = upstream(self.path)
            else:
                status, data = 404, b'{"message":"outside the enrolled paths"}'
            reads.append({"path": path, "status": status, "ok": status == 200, "body_bytes": len(data)})
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def log_message(self, *args):
            pass

    # Make the initial connection before either measured program runs.
    status, _ = upstream("/repos/git/git")
    if status != 200:
        raise RuntimeError(f"GitHub gateway warmup returned HTTP {status}")
    with ThreadingHTTPServer(("127.0.0.1", 0), Handler) as server:
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            yield f"http://127.0.0.1:{server.server_port}", reads
        finally:
            server.shutdown()
            thread.join()
            while not connections.empty():
                connections.get_nowait().close()


def measure(binary, rounds):
    rows = []
    with github_gateway() as (base, reads), ExitStack() as stack:
        calls = {}
        programs = {}
        for trial in TRIALS:
            _, mcp_call = stack.enter_context(toolset("skeiner", binary, base))
            calls["skeiner", trial] = lambda args, call=mcp_call: call("execute", args)
            calls["client", trial] = stack.enter_context(warm_session(base))
            for arm in ARMS:
                name = f"{arm}-{trial}"
                receipt = json.loads((HERE / "results" / (name + ".json")).read_text())
                if receipt.get("program_sha256"):
                    code = (HERE / "results" / (name + ".js")).read_bytes().decode()
                    if sha(code) != receipt["program_sha256"]:
                        raise ValueError(f"source differs from enrollment: {name}")
                    programs[arm, trial] = {"code": code}
                    if receipt.get("saved_mode"):
                        programs[arm, trial]["mode"] = receipt["saved_mode"]
                if not calls[arm, trial]({"code": "return true;", "input": {}})[1]:
                    raise RuntimeError(f"warmup failed for {arm}")
        for iteration in range(1, rounds + 1):
            for trial in TRIALS:
                for i, (case, inputs) in enumerate(CASES.items()):
                    order = ARMS if (trial + i + iteration) % 2 == 0 else ARMS[::-1]
                    for arm in order:
                        key = (arm, trial)
                        row = {"round": iteration, "trial": trial, "case": case, "arm": arm}
                        reads.clear()
                        if key in programs:
                            start = perf_counter()
                            result, ok = calls[key]({**programs[key], "input": inputs})
                            elapsed = round((perf_counter() - start) * 1000, 2)
                            answer = answer_json(result) if ok else None
                        else:
                            elapsed, answer, ok = None, None, False
                        if any(r["status"] in (401, 403, 429) or r["status"] >= 500 for r in reads):
                            raise RuntimeError(f"GitHub unavailable for {row}: {reads}")
                        row.update(elapsed_ms=elapsed, answer=answer, correct=correct(case, answer),
                                   grounded=grounded(case, reads), ok=ok, reads=list(reads))
                        rows.append(row)
    return rows


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=ROOT / "skeiner")
    parser.add_argument("--rounds", type=int, choices=range(1, 6), default=3)
    args = parser.parse_args()
    print(json.dumps({"started_at": datetime.now(timezone.utc).isoformat(),
                      "platform": platform.system() + "/" + platform.machine(),
                      "rounds": args.rounds, "runs": measure(args.binary.resolve(), args.rounds)}, indent=2))


if __name__ == "__main__":
    main()
