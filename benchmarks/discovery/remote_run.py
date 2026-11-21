"""Start the remote host on GitHub Actions and run the paired audit from this machine."""
import argparse
import hashlib
import json
import os
import re
import secrets
import subprocess
import sys
import tempfile
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent
WORKFLOW = "remote-provenance.yml"


def gh(*args):
    return subprocess.check_output(["gh", *args], cwd=HERE, text=True, timeout=60).strip()


def state(run):
    return json.loads(gh("run", "view", run, "--json", "status,conclusion,headSha,url"))


def wait_for_end(run, seconds):
    deadline = time.monotonic() + seconds
    while (status := state(run))["status"] != "completed":
        if time.monotonic() >= deadline:
            raise TimeoutError("remote host did not finish")
        time.sleep(4)
    return status


def endpoint(run, private):
    # The first artifact is available as soon as its upload step completes.
    deadline = time.monotonic() + 15 * 60
    while time.monotonic() < deadline:
        result = subprocess.run(["gh", "run", "download", run, "--name", "remote-endpoint",
                                 "--dir", str(private)], cwd=HERE, capture_output=True, timeout=60)
        if result.returncode == 0:
            url = (private / "endpoint.txt").read_text().strip()
            if not re.fullmatch(r"https://[a-z0-9-]+\.trycloudflare\.com", url):
                raise ValueError("unexpected benchmark endpoint in the workflow artifact")
            return private / "endpoint.txt"
        if state(run)["status"] == "completed":
            raise RuntimeError(f"run {run} ended before the remote host became available")
        time.sleep(5)
    raise TimeoutError("remote host did not publish its endpoint")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", required=True, type=Path, help="new or empty directory for public receipts")
    parser.add_argument("--ref", default="main", help="published branch or tag to dispatch")
    args = parser.parse_args()
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=True)
    if any(out.iterdir()):
        parser.error("choose an empty output directory")
    with tempfile.TemporaryDirectory() as folder:
        private = Path(folder)
        secret = secrets.token_hex(32)
        token = private / "bearer"
        with os.fdopen(os.open(token, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), "w") as handle:
            handle.write(secret)
        digest = hashlib.sha256(secret.encode()).hexdigest()
        url = gh("workflow", "run", WORKFLOW, "--ref", args.ref, "-f", "token_sha256=" + digest)
        match = re.search(r"/actions/runs/(\d+)", url)
        if not match:
            raise RuntimeError("gh did not return a run URL; check its version and Actions run list")
        run = match.group(1)
        print(f"Remote benchmark run {run}", flush=True)
        client = HERE / "remote_client.py"
        run_file = out / "run.json"
        run_file.write_text(json.dumps(state(run), indent=2) + "\n")
        try:
            origin = endpoint(run, private)
            print("Running 48 paired audits from this client", flush=True)
            subprocess.run([sys.executable, str(client), "measure", "--endpoint-file", str(origin),
                            "--token-file", str(token), "--out", str(out / "client.json")], check=True)
            status = wait_for_end(run, 12 * 60)
            run_file.write_text(json.dumps(status, indent=2) + "\n")
            gh("run", "download", run, "--name", "remote-server", "--dir", str(out))
            if status["conclusion"] != "success":
                raise RuntimeError(f"run {run} ended with {status['conclusion']}")
            subprocess.run([sys.executable, str(client), "report", str(out / "client.json"),
                            "--host", str(out / "server.json")], check=True)
        except BaseException:
            # Stop the temporary route early if the caller exits during sampling.
            try:
                if state(run)["status"] != "completed":
                    gh("run", "cancel", run)
                final = wait_for_end(run, 90)
                run_file.write_text(json.dumps(final, indent=2) + "\n")
                if not (out / "server.json").exists():
                    gh("run", "download", run, "--name", "remote-server", "--dir", str(out))
            except (TimeoutError, subprocess.SubprocessError):
                print(f"Check run {run} for any remaining host artifacts.", file=sys.stderr)
            raise


if __name__ == "__main__":
    main()
