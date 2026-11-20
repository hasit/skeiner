"""Synthetic museum loans, curated expected answers, and a loopback API."""
import json
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, HTTPServer
from threading import Thread
from urllib.parse import parse_qs, urlsplit

CASES = {
    "original": ({"branch": "MOSS", "asOf": "2026-03-15"}, [
        ("L-04", "O-47", "2026-03-02"), ("L-05", "O-11", "2026-03-13"),
        ("L-07", "O-16", "2026-02-28")]),
    "updated": ({"branch": "MOSS", "asOf": "2026-03-20"}, [
        ("L-03", "O-82", "2026-03-15"), ("L-05", "O-11", "2026-03-13"),
        ("L-06", "O-33", "2026-03-18"), ("L-09", "O-37", "2026-03-16")]),
    "other-branch": ({"branch": "PIER", "asOf": "2026-03-12"}, [
        ("P-12", "X-18", "2026-03-08"), ("P-77", "X-91", "2026-03-10")]),
}

OPERATIONS = {"listLoans": "loans", "listRenewals": "renewals"}
TASK = (
    "Which museum loans at the requested branch are overdue as of the given date? "
    "Include open loans only. Each effective due date is the latest of the loan's "
    "contractual due date and every approved renewal date for that loan. Ignore "
    "withdrawn renewals. Due on the as-of date is on time. Check every page. Return "
    "only a JSON array of objects with loan, object, due, sorted by loan ID ascending."
)

def expected(case):
    return [dict(zip(("loan", "object", "due"), row)) for row in CASES[case][1]]


def records(case, branch):
    if branch == "MOSS":
        loans = [
            ("L-05", "O-11", "open", "2026-03-10"), ("L-01", "O-64", "open", "2026-03-01"),
            ("L-02", "O-58", "returned", "2026-03-01"), ("L-07", "O-16", "open", "2026-02-28"),
            ("L-03", "O-82", "open", "2026-03-15"), ("L-04", "O-47", "open", "2026-03-02"),
            ("L-08", "O-20", "returned", "2026-02-13"), ("L-06", "O-33", "open", "2026-03-18"),
        ]
        renewals = [
            ("L-05", "approved", "2026-03-13"), ("L-01", "approved", "2026-03-24"),
            ("L-05", "approved", "2026-03-11"), ("L-04", "withdrawn", "2026-05-19"),
        ]
        if case == "updated":
            loans[5] = ("L-04", "O-47", "returned", "2026-03-02")
            loans.insert(0, ("L-09", "O-37", "open", "2026-03-16"))
            renewals.append(("L-07", "approved", "2026-03-30"))
    else:
        loans = [
            ("P-77", "X-91", "open", "2026-02-01"), ("P-50", "X-05", "open", "2026-03-12"),
            ("P-41", "X-40", "open", "2026-02-26"), ("P-19", "X-13", "returned", "2026-02-17"),
            ("P-12", "X-18", "open", "2026-03-08"), ("P-38", "X-29", "open", "2026-03-23"),
        ]
        renewals = [
            ("P-77", "approved", "2026-03-10"), ("P-41", "approved", "2026-03-16"),
            ("P-77", "withdrawn", "2026-04-01"), ("P-12", "approved", "2026-03-04"),
        ]
    return {
        "loans": [dict(zip(("loanId", "objectId", "status", "dueDate"), row)) for row in loans],
        "renewals": [dict(zip(("loanId", "decision", "through"), row)) for row in renewals],
    }


@contextmanager
def endpoint(case):
    reads = []

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            url = urlsplit(self.path)
            collection = url.path.removeprefix("/")
            params = parse_qs(url.query, keep_blank_values=True)
            branch = params.get("branch", [""])[0]
            cursor = params.get("cursor", [""])[0]
            reads.append([collection, branch, cursor])
            if collection not in OPERATIONS.values() or branch not in ("MOSS", "PIER"):
                self.send_error(400)
                return
            rows = records(case, branch)[collection]
            starts = {"": 0, **{f"page-{i // 3 + 1}": i for i in range(3, len(rows), 3)}}
            if cursor not in starts:
                self.send_error(400)
                return
            start = starts[cursor]
            next_cursor = f"page-{start // 3 + 2}" if start + 3 < len(rows) else None
            data = json.dumps({collection: rows[start:start + 3], "nextCursor": next_cursor}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def log_message(self, *args):
            pass

    with HTTPServer(("127.0.0.1", 0), Handler) as server:
        thread = Thread(target=server.serve_forever, kwargs={"poll_interval": .02}, daemon=True)
        thread.start()
        try:
            yield f"http://127.0.0.1:{server.server_port}", reads
        finally:
            server.shutdown()
            thread.join()
