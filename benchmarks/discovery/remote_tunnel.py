"""On the disposable Actions host, publish the fixed benchmark listener for one session."""
import http.client
import os
import re
import subprocess
import sys
import time
from pathlib import Path


def reachable(hostname):
    connection = http.client.HTTPSConnection(hostname, timeout=3)
    try:
        connection.request("GET", "/ready")
        return connection.getresponse().status == 401
    except (OSError, http.client.HTTPException):
        return False
    finally:
        connection.close()


def launch(folder):
    origin = "http://127.0.0.1:8765"
    command = [str(folder / "cloudflared"), "tunnel", "--no-autoupdate", "--protocol", "http2", "--url", origin]
    log = folder / "remote-tunnel.log"
    pidfile = folder / "remote-tunnel.pid"
    for _ in range(2):
        with log.open("w") as output:
            process = subprocess.Popen(command, env={"PATH": os.defpath, "HOME": os.environ["HOME"]},
                                       stdin=subprocess.DEVNULL, stdout=output, stderr=subprocess.STDOUT,
                                       start_new_session=True)
        pidfile.write_text(str(process.pid))
        deadline = time.monotonic() + 60
        while process.poll() is None and time.monotonic() < deadline:
            hosts = re.findall(r"https://([a-z0-9-]+\.trycloudflare\.com)\b", log.read_text(errors="replace"))
            if hosts and hosts[-1] != "api.trycloudflare.com" and reachable(hosts[-1]):
                (folder / "endpoint.txt").write_text(f"https://{hosts[-1]}\n")
                return
            time.sleep(1)
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        pidfile.unlink(missing_ok=True)
    raise SystemExit("Quick Tunnel failed to reach the authenticated benchmark listener after two attempts.")


if __name__ == "__main__":
    if os.environ.get("GITHUB_ACTIONS") != "true" or os.environ.get("RUNNER_ENVIRONMENT") != "github-hosted":
        sys.exit("Run the remote-provenance workflow on a GitHub-hosted runner.")
    launch(Path(sys.argv[1]).resolve())
