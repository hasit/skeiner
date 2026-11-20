"""Compare saved Skeiner programs with GitHub's official MCP server on live commit data."""
import argparse
import csv
import io
import json
import subprocess
import sys
from contextlib import contextmanager
from datetime import datetime, timezone
from pathlib import Path
from time import perf_counter

from official_mcp import DEFAULT_BINARY, VERSION, OfficialGitHub, github_token

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
sys.path.insert(0, str(ROOT))
from examples.rates.quickstart import request

ANCHOR = "777489f9e09c8d0dd6b12f9d90de6376330577a2"  # Git v2.47.0
FILE = "Documentation/git-merge.txt"
MIX_PROGRAM = (HERE / "release-mix.js").read_text()
LOOKUP_PROGRAM = """const rows = await api.call("listCommits", {query: api.input});
return rows.map(row => ({sha: row.sha}));"""

# Expected answers were computed independently with git log on the tagged repository.
CASES = [
    ("release-60", 60, 60, {"localization": 11, "merges": 31, "other": 18}),
    ("release-200", 200, 50, {"localization": 11, "merges": 62, "other": 127}),
    ("one-file", 1, 1, [{"sha": "9decd56cc9a41773bffa517caaa07db3f78e8646"}]),
]


@contextmanager
def skeiner_session(binary, token):
    command = [str(binary.resolve()), "serve", "--spec", str(HERE / "openapi.yaml"),
               "--base-url", "https://api.github.com", "--allow", "listCommits",
               "--token-env", "GITHUB_TOKEN"]
    with subprocess.Popen(command, env={"GITHUB_TOKEN": token}, stdin=subprocess.PIPE,
                          stdout=subprocess.PIPE, text=True, encoding="utf-8") as child:
        init = request(child, 1, "initialize", {"protocolVersion": "2025-06-18",
                       "capabilities": {}, "clientInfo": {"name": "github-benchmark", "version": "1"}})
        request(child, None, "notifications/initialized", {})
        seq = 1

        def execute(args):
            nonlocal seq
            seq += 1
            reply = request(child, seq, "tools/call", {"name": "execute", "arguments": args})
            if reply.get("isError"):
                raise RuntimeError("Skeiner: " + reply["content"][0]["text"])
            content = reply["content"]
            if len(content) != 1 or content[0]["type"] != "text":
                raise ValueError("expected one MCP text result")
            return content[0]["text"]

        yield execute, init["serverInfo"]["version"]


def argument_size(args):
    return len(json.dumps(args, separators=(",", ":"), ensure_ascii=False).encode())


def measure(arm, call, case):
    name, limit, skeiner_page, expected = case
    lookup = name == "one-file"
    start = perf_counter()
    if arm == "skeiner":
        params = {"sha": ANCHOR, "limit": limit, "perPage": skeiner_page}
        if lookup:
            params = {"sha": ANCHOR, "path": FILE, "per_page": 1}
        args = {"mode": "async", "code": LOOKUP_PROGRAM if lookup else MIX_PROGRAM, "input": params}
        text = call(args)
        answer = json.loads(text)
        page_count = (limit + skeiner_page - 1) // skeiner_page
        call_count, request_bytes, reply_bytes = 1, argument_size(args), len(text.encode())
    else:
        page_size = min(limit, 100)
        page_count = (limit + page_size - 1) // page_size
        call_count, request_bytes, reply_bytes = page_count, 0, 0
        answer = [] if lookup else {"localization": 0, "merges": 0, "other": 0}
        for page in range(1, page_count + 1):
            args = {"owner": "git", "repo": "git", "sha": ANCHOR, "perPage": page_size,
                    "page": page, "fields": ["sha"] if lookup else ["commit"]}
            if lookup:
                args["path"] = FILE
            text = call(args)
            request_bytes += argument_size(args)
            reply_bytes += len(text.encode())
            is_csv = arm == "github-csv"
            rows = list(csv.DictReader(io.StringIO(text))) if is_csv else json.loads(text)
            if len(rows) != page_size:
                raise ValueError(f"{arm}: unexpected number of commits on page {page}")
            for row in rows:
                if lookup:
                    answer.append({"sha": row["sha"]})
                    continue
                message = row["commit.message"] if is_csv else row["commit"]["message"]
                if message.startswith("l10n:"):
                    answer["localization"] += 1
                elif message.startswith("Merge "):
                    answer["merges"] += 1
                else:
                    answer["other"] += 1
    elapsed_ms = round((perf_counter() - start) * 1000, 1)
    if answer != expected:
        raise ValueError(f"{arm} {name}: expected {expected}, got {answer}")
    return {"case": name, "arm": arm, "answer": answer, "commit_pages": page_count,
            "mcp_calls": call_count, "argument_bytes": request_bytes,
            "reply_bytes": reply_bytes, "elapsed_ms": elapsed_ms}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--skeiner", type=Path, default=ROOT / "skeiner")
    parser.add_argument("--github-mcp", type=Path, default=DEFAULT_BINARY)
    parser.add_argument("--repetitions", type=int, default=3)
    options = parser.parse_args()
    if options.repetitions < 1:
        parser.error("--repetitions must be positive")
    token = github_token()
    rows = []
    with OfficialGitHub(options.github_mcp, token) as stable, \
         OfficialGitHub(options.github_mcp, token, csv_output=True) as csv_server, \
         skeiner_session(options.skeiner, token) as (execute, version):
        arms = [("github-json", stable.call), ("github-csv", csv_server.call), ("skeiner", execute)]
        for trial in range(options.repetitions):
            for index, case in enumerate(CASES):
                offset = (trial + index) % len(arms)
                for arm, call in arms[offset:] + arms[:offset]:
                    rows.append({"trial": trial + 1, **measure(arm, call, case)})
    print(json.dumps({"recorded_at": datetime.now(timezone.utc).isoformat(),
                      "repository": "git/git", "revision": ANCHOR, "skeiner_version": version,
                      "github_mcp_version": VERSION, "measurements": rows}, indent=2))


if __name__ == "__main__":
    main()
