"""Execute an author's client-side JavaScript in a network-scoped Deno process."""
import json
import os
import select
import shutil
import subprocess
import tempfile
import time
from contextlib import contextmanager
from pathlib import Path
from urllib.parse import urlsplit


HERE = Path(__file__).resolve().parent
LOCAL_DENO = HERE.parents[1] / "bin/deno"
CLIENT_EXECUTE = {"type": "function", "function": {
    "name": "execute",
    "description": (
        "Run an async client-side JavaScript function body and return its JSON result. "
        "Discover HTTP paths and parameters with search. Use await get(path, query) for "
        "a GET returning parsed JSON. Substitute path parameters yourself. Query is an "
        "optional object. get also accepts a provider URL and rebases it to the current "
        "API origin. The body receives input, baseUrl, and standard fetch. Intermediate "
        "responses stay in JavaScript. Use an explicit return."
    ),
    "parameters": {"type": "object", "properties": {
        "code": {"type": "string", "description": "Async JavaScript function body."},
        "input": {"description": "Optional JSON value available as input."}
    }, "required": ["code"], "additionalProperties": False}
}}


def deno_path():
    executable = LOCAL_DENO if LOCAL_DENO.is_file() else shutil.which("deno")
    if not executable:
        raise FileNotFoundError("Install Deno 2.9.8 on PATH or at bin/deno in the repository")
    return str(executable)


def command(base):
    origin = urlsplit(base)
    if origin.scheme != "http" or origin.hostname != "127.0.0.1" or not origin.port:
        raise ValueError("The JavaScript benchmark requires the loopback API server")
    return [
        deno_path(), "run", "--quiet", "--no-config", "--no-lock", "--no-npm",
        "--no-remote", "--no-prompt", "--v8-flags=--max-old-space-size=64",
        f"--allow-net={origin.hostname}:{origin.port}", str(HERE / "client_runner.js"),
    ]


def invalid_args(args):
    code = args.get("code")
    if not isinstance(code, str) or len(code.encode()) > 32768:
        return "JavaScript code must be a string of at most 32 KiB"
    if len(json.dumps(args.get("input"), ensure_ascii=False).encode()) > 32768:
        return "Program input exceeds 32 KiB"


def execute(base, args):
    """Run against a loopback replay or loopback live proxy, returning MCP-style text and success."""
    error = invalid_args(args)
    if error:
        return error, False
    with tempfile.TemporaryDirectory() as folder:
        try:
            result = subprocess.run(command(base), input=json.dumps({**args, "baseUrl": base}),
                                    cwd=folder, env={"DENO_DIR": folder, "DENO_NO_UPDATE_CHECK": "1"},
                                    text=True, capture_output=True, timeout=30, check=False)
        except subprocess.TimeoutExpired:
            return "Client execution exceeded 30 seconds", False
    if result.returncode:
        return result.stderr.strip()[:2048] or "Client execution failed", False
    answer = result.stdout.strip()
    if not answer or len(answer.encode()) > 16384:
        return "Client execution must return a JSON value of at most 16 KiB", False
    try:
        json.loads(answer)
    except ValueError:
        return "Client execution did not return a JSON value", False
    return answer, True


def read_frame(stream):
    deadline = time.monotonic() + 30
    frame = bytearray()
    while not frame.endswith(b"\n"):
        if not select.select([stream], [], [], max(0, deadline - time.monotonic()))[0]:
            raise TimeoutError("Deno session timed out")
        chunk = os.read(stream.fileno(), 65536)
        if not chunk or len(frame) + len(chunk) > 131072:
            raise RuntimeError("Missing or oversized response from Deno session")
        frame.extend(chunk)
    return json.loads(frame)


@contextmanager
def warm_session(base):
    """Time archived programs with one ready JS process and native HTTP connection pool."""
    with tempfile.TemporaryDirectory() as folder, subprocess.Popen(
        command(base) + ["--session"], cwd=folder, env={"DENO_DIR": folder, "DENO_NO_UPDATE_CHECK": "1"},
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, bufsize=0,
    ) as child:
        try:
            if not read_frame(child.stdout).get("ready"):
                raise RuntimeError("Deno session did not initialize")

            def call(args):
                error = invalid_args(args)
                if error:
                    return error, False
                child.stdin.write((json.dumps({**args, "baseUrl": base}) + "\n").encode())
                child.stdin.flush()
                reply = read_frame(child.stdout)
                if "error" in reply:
                    return reply["error"], False
                return reply["result"], True

            yield call
        finally:
            child.kill()
