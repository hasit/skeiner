"""GitHub release provenance fixtures, independent Git oracle, and loopback replay."""
import argparse
import hashlib
import json
import os
import ssl
import subprocess
import tempfile
import threading
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.error import HTTPError
from urllib.parse import urlsplit
from urllib.request import Request, urlopen

FIXTURES = Path(__file__).resolve().parent / "fixtures"
CASES = {
    "git": {"owner": "git", "repo": "git", "tag": "v2.47.0"},
    "go": {"owner": "golang", "repo": "go", "tag": "go1.22.0"},
    "cli": {"owner": "cli", "repo": "cli", "tag": "v2.59.0"},
    "curl": {"owner": "curl", "repo": "curl", "tag": "curl-8_11_0"},
}
OPERATIONS = [
    "getRepository", "getBranch", "getReleaseByTag", "getLatestRelease", "getRepositoryCommit",
    "getTagReference", "listMatchingTagReferences", "getAnnotatedTag", "getGitCommit", "getGitTree",
]
TASK = (
    "Audit the provenance of the supplied public release tag. Resolve its full Git commit SHA, "
    "the commit subject (first line of the commit message), and whether the commit contains "
    "an embedded signature. Also report every annotated tag object's full SHA and whether it "
    "contains an embedded signature, ordered from the outermost tag inward. A lightweight tag "
    "has an empty chain. A signature's presence counts independently of trust or validity. "
    "Return the JSON object {commit_sha, commit_subject, commit_signed, tag_chain:[{sha,signed}]} "
    "using boolean signature flags. Use the API for the evidence."
)
API = "https://api.github.com"


def base(case):
    inp = CASES[case]
    return f"/repos/{inp['owner']}/{inp['repo']}"


def expected(case):
    return json.loads((FIXTURES / "oracle.json").read_text())[case]


def expected_reads(case):
    inp, answer = CASES[case], expected(case)
    root = base(case)
    return [f"{root}/git/ref/tags/{inp['tag']}",
            *(f"{root}/git/tags/{obj['sha']}" for obj in answer["tag_chain"]),
            f"{root}/git/commits/{answer['commit_sha']}"]


def grounded(case, reads):
    """Allow repository commits by tag/SHA and matching refs as equivalent evidence."""
    paths = {r["path"] for r in reads if r["ok"]}
    ref, *tags, commit = expected_reads(case)
    prefix = base(case) + "/commits/"
    return (bool(paths & {ref, ref.replace("/git/ref/", "/git/matching-refs/")})
            and all(path in paths for path in tags)
            and bool(paths & {commit, prefix + CASES[case]["tag"], prefix + expected(case)["commit_sha"]}))


def pick(obj, *names):
    return {name: obj[name] for name in names if name in obj}


def signature(value):
    if value is None:
        return None
    if not value:
        raise ValueError("unexpected blank signature")
    lines = value.strip().splitlines()
    if lines[0].startswith("-----BEGIN ") and lines[-1].startswith("-----END "):
        return lines[0] + "\n[omitted]\n" + lines[-1]
    return "[signature omitted]"


def projected_message(value):
    first, _, body = value.partition("\n")
    return first + ("\n\n[message body omitted]" if body.strip() else "")


def git_object(obj):
    fields = pick(obj, "sha", "url", "tag", "object", "tree", "parents")
    fields["message"] = projected_message(obj["message"])
    info = obj["verification"]
    fields["verification"] = {**pick(info, "verified", "reason"), "signature": signature(info["signature"])}
    return fields


def project(path, status, value):
    if status != 200:
        return pick(value, "message", "status")
    tail = "/".join(path.split("/")[4:])
    if tail.startswith("git/commits/") or tail.startswith("git/tags/"):
        return git_object(value)
    if tail.startswith("commits/"):
        return {**pick(value, "sha", "url"), "commit": git_object(value["commit"])}
    if tail.startswith("releases/"):
        return pick(value, "id", "tag_name", "target_commitish", "draft", "prerelease", "name", "url")
    if tail.startswith("git/trees/"):
        return pick(value, "sha", "url", "truncated")
    if tail.startswith("branches/"):
        return {**pick(value, "name", "protected"), "commit": pick(value["commit"], "sha", "url")}
    if not tail:
        return pick(value, "full_name", "default_branch", "description", "archived", "url")
    return value  # Exact Git refs and matching refs contain only object identifiers and URLs.


def read_github(path, token=None):
    headers = {"User-Agent": "skeiner-discovery-benchmark", "Accept": "application/vnd.github+json",
               "X-GitHub-Api-Version": "2022-11-28"}
    if token:
        headers["Authorization"] = "Bearer " + token
    trust = os.environ.get("SSL_CERT_FILE") or ssl.get_default_verify_paths().cafile
    if not trust and Path("/etc/ssl/cert.pem").is_file():
        trust = "/etc/ssl/cert.pem"
    try:
        with urlopen(Request(API + path, headers=headers), timeout=25,
                     context=ssl.create_default_context(cafile=trust)) as response:
            raw, status = response.read(), response.status
    except HTTPError as exc:
        if exc.code != 404:
            raise
        raw, status = exc.read(), exc.code
    return {"status": status, "body": project(path, status, json.loads(raw))}


def capture_api(case, token):
    root, name = base(case), CASES[case]["tag"]
    records = {}

    def read(path):
        url = root + path
        records[url] = read_github(url, token)
        return records[url]["body"]

    repo = read("")
    read("/branches/" + name)
    read("/branches/" + repo["default_branch"])
    read("/releases/latest")
    read("/releases/tags/" + name)
    read("/git/matching-refs/tags/" + name)
    obj = read("/git/ref/tags/" + name)["object"]
    while obj["type"] == "tag":
        obj = read("/git/tags/" + obj["sha"])["object"]
    if obj["type"] != "commit":
        raise ValueError("release does not identify a commit")
    commit = read("/git/commits/" + obj["sha"])
    read("/git/trees/" + commit["tree"]["sha"])
    read("/commits/" + name)
    read("/commits/" + obj["sha"])
    return records


def git_oracle(case):
    """Fetch through the Git protocol; inspect canonical objects without the REST API."""
    inp = CASES[case]
    remote = f"https://github.com/{inp['owner']}/{inp['repo']}.git"
    with tempfile.TemporaryDirectory() as tmp:
        def git(*args):
            return subprocess.check_output(["git", "-C", tmp, *args])

        git("init", "--bare", "-q")
        git("fetch", "--quiet", "--filter=tree:0", "--no-tags", "--depth=1", remote, "refs/tags/" + inp["tag"])
        sha = git("rev-parse", "FETCH_HEAD").decode().strip()
        tags = []
        while True:
            kind = git("cat-file", "-t", sha).decode().strip()
            body = git("cat-file", kind, sha)
            if hashlib.sha1(f"{kind} {len(body)}\0".encode() + body).hexdigest() != sha:
                raise ValueError("Git object hash mismatch")
            headers, message = body.decode().split("\n\n", 1)
            if kind == "commit":
                return {"commit_sha": sha, "commit_subject": message.splitlines()[0],
                        "commit_signed": any(row.startswith(("gpgsig ", "gpgsig-sha256 "))
                                             for row in headers.splitlines()), "tag_chain": tags}
            if kind != "tag" or not headers.startswith("object "):
                raise ValueError("unexpected non-tag object in release chain")
            tags.append({"sha": sha, "signed": any(
                line.startswith(("-----BEGIN PGP SIGNATURE-----", "-----BEGIN SSH SIGNATURE-----",
                                 "-----BEGIN SIGNED MESSAGE-----")) for line in message.splitlines())})
            sha = headers.splitlines()[0].split()[1]


def fixture(case):
    raw = (FIXTURES / (case + ".json")).read_bytes()
    if hashlib.sha256(raw).hexdigest() != json.loads((FIXTURES / "sha256.json").read_text())[case]:
        raise ValueError(f"{case} fixture checksum mismatch")
    return json.loads(raw)


def answer_from_api(case, records):
    """Resolve the captured REST objects and check that each response has its requested SHA."""
    def read(path):
        record = records[base(case) + path]
        if record['status'] != 200:
            raise ValueError(f"{case}: archived request failed for {path}")
        return record['body']

    obj = read('/git/ref/tags/' + CASES[case]['tag'])['object']
    tags = []
    seen = set()
    while obj['type'] == 'tag':
        tag = read('/git/tags/' + obj['sha'])
        if tag['sha'] != obj['sha'] or tag['sha'] in seen:
            raise ValueError(f"{case}: invalid annotated tag chain")
        seen.add(tag['sha'])
        tags.append({'sha': tag['sha'], 'signed': bool(tag['verification']['signature'])})
        obj = tag['object']
    if obj['type'] != 'commit':
        raise ValueError(f"{case}: tag does not resolve to a commit")
    commit = read('/git/commits/' + obj['sha'])
    if commit['sha'] != obj['sha']:
        raise ValueError(f"{case}: commit SHA differs from the requested object")
    return {'commit_sha': commit['sha'], 'commit_subject': commit['message'].splitlines()[0],
            'commit_signed': bool(commit['verification']['signature']), 'tag_chain': tags}


@contextmanager
def endpoint(reads=None):
    records = {}
    for case in CASES:
        records.update(fixture(case))

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            path = urlsplit(self.path).path
            result = records.get(path, {"status": 404, "body": {"message": "No archived response for this path"}})
            if reads is not None:
                reads.append({"path": path, "ok": result["status"] == 200})
            body = json.dumps(result["body"], ensure_ascii=False, separators=(",", ":")).encode()
            self.send_response(result["status"])
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    with ThreadingHTTPServer(("127.0.0.1", 0), Handler) as server:
        thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.02}, daemon=True)
        thread.start()
        try:
            yield f"http://127.0.0.1:{server.server_port}"
        finally:
            server.shutdown()
            thread.join()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("capture", "verify", "verify-git"))
    args = parser.parse_args()
    if args.action == 'verify':
        for case in CASES:
            if answer_from_api(case, fixture(case)) != expected(case):
                raise ValueError(f"{case}: archived REST response differs from the Git oracle")
        print(f"Verified {len(CASES)} release snapshots against the recorded native Git oracle.")
        return
    with ThreadPoolExecutor(max_workers=4) as pool:
        answers = dict(zip(CASES, pool.map(git_oracle, CASES)))
        if args.action == "capture":
            token = os.environ.get("GITHUB_TOKEN") or os.environ.get("GH_TOKEN")
            data = dict(zip(CASES, pool.map(lambda case: capture_api(case, token), CASES)))
            for case in CASES:
                if answer_from_api(case, data[case]) != answers[case]:
                    raise ValueError(f"{case}: live REST and Git object answers disagree")
            hashes = {}
            for case, record in data.items():
                raw = (json.dumps(record, ensure_ascii=False, indent=2) + "\n").encode()
                (FIXTURES / (case + ".json")).write_bytes(raw)
                hashes[case] = hashlib.sha256(raw).hexdigest()
            for name, table in [("sha256", hashes), ("oracle", answers)]:
                (FIXTURES / (name + ".json")).write_text(json.dumps(table, indent=2) + "\n")
        for case, answer in answers.items():
            if expected(case) != answer:
                raise ValueError(f"{case}: oracle differs from current native Git objects")
            print(case, json.dumps(answer))


if __name__ == "__main__":
    main()
