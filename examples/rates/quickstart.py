"""Use search and execute over MCP stdio. Requires only Python 3."""
import argparse
import json
import subprocess
import sys
import threading
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

EXAMPLE = Path(__file__).resolve().parent
LIVE = "https://api.frankfurter.dev/v2"


@contextmanager
def snapshot_server():
    records = json.loads((EXAMPLE / "snapshot.json").read_text())

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path not in records:
                self.send_error(404)
                return
            body = json.dumps(records[self.path]).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    with HTTPServer(("127.0.0.1", 0), Handler) as server:
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            yield f"http://127.0.0.1:{server.server_port}/v2"
        finally:
            server.shutdown()
            thread.join()


def request(child, identifier, method, params):
    message = {"jsonrpc": "2.0", "method": method, "params": params}
    if identifier is not None:
        message["id"] = identifier
    child.stdin.write(json.dumps(message) + "\n")
    child.stdin.flush()
    if identifier is None:
        return
    for line in child.stdout:
        response = json.loads(line)
        if response.get("id") == identifier:
            if "error" in response:
                raise RuntimeError(response["error"])
            return response["result"]
    raise RuntimeError(f"skeiner closed the connection during {method}")


def tool(child, identifier, name, arguments):
    reply = request(child, identifier, "tools/call", {"name": name, "arguments": arguments})
    payload = reply["content"][0]["text"]
    if reply.get("isError"):
        raise RuntimeError(payload)
    return json.loads(payload)


def compare(binary, base_url):
    command = [str(binary.resolve()), "serve", "--spec", str(EXAMPLE / "openapi.yaml"),
               "--base-url", base_url, "--token-env", "", "--allow", "getRate,getProviderRate"]
    with subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True) as child:
        request(child, 1, "initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                        "clientInfo": {"name": "rates-quickstart", "version": "1"}})
        request(child, None, "notifications/initialized", {})
        operations = tool(child, 2, "search", {"query": "exchange rate provider"})
        found = sorted(op["operationId"] for op in operations)
        print("Discovered " + ", ".join(found), file=sys.stderr)
        if found != ["getProviderRate", "getRate"]:
            raise RuntimeError("Both rate operations are needed for this comparison")
        result = tool(child, 3, "execute", {"mode": "async", "code": (EXAMPLE / "compare.js").read_text()})
        print(json.dumps(result))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=Path("./skeiner"))
    parser.add_argument("--live", action="store_true", help="fetch rates from the public API")
    args = parser.parse_args()
    if args.live:
        compare(args.binary, LIVE)
    else:
        with snapshot_server() as url:
            compare(args.binary, url)


if __name__ == "__main__":
    main()
