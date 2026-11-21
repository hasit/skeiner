"""Synthetic 1Password Connect vault metadata on a private loopback listener."""
import json
import secrets
import threading
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit


class Vaults:
    def __init__(self):
        self.token = secrets.token_urlsafe(32)
        self.selected = {
            "id": secrets.token_hex(13),
            "name": f"Field Team {secrets.token_hex(4)}",
            "contentVersion": 1 + secrets.randbelow(99),
        }
        self.detail = dict(self.selected, attributeVersion=1 + secrets.randbelow(99))
        self.other = {"id": secrets.token_hex(13), "name": "Other Team", "contentVersion": 2}
        self.filter = f'name eq "{self.selected["name"]}"'
        self.trace = []

    def answer(self, method, target, auth):
        url = urlsplit(target)
        query = parse_qs(url.query, keep_blank_values=True)
        authorized = auth == "Bearer " + self.token
        self.trace.append((method, url.path, query, authorized))
        if not authorized:
            return 401, {"status": 401}
        if method == "GET" and url.path == "/v1/vaults":
            if "filter" not in query:
                return 200, [self.other, self.selected]
            return 200, [self.selected] if query["filter"] == [self.filter] else []
        if method == "GET" and url.path == "/v1/vaults/" + self.selected["id"]:
            return 200, self.detail
        return 403, {"status": 403}


@contextmanager
def origin():
    data = Vaults()

    class Handler(BaseHTTPRequestHandler):
        def respond(self):
            status, result = data.answer(self.command, self.path, self.headers.get("Authorization"))
            body = json.dumps(result).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        do_GET = do_DELETE = do_POST = do_PUT = do_PATCH = respond

        def log_message(self, *args):
            pass

    with ThreadingHTTPServer(("127.0.0.1", 0), Handler) as server:
        thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.03}, daemon=True)
        thread.start()
        try:
            yield data, f"http://127.0.0.1:{server.server_port}/v1"
        finally:
            server.shutdown()
            thread.join()
