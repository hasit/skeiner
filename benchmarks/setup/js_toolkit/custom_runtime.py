"""Launch the packaged JavaScript gateway or a hand-built MCP worker."""

import shutil
from pathlib import Path
from urllib.parse import urlsplit


HERE = Path(__file__).resolve().parent
LOCAL_DENO = HERE.parents[2] / "bin/deno"
CUSTOM_ENV = {"SPEC", "BASE_URL", "TOKEN"}


def package_command():
    node = shutil.which("node")
    entry = HERE / "node_modules/@ivotoby/openapi-mcp-server/bin/mcp-server.js"
    if not node or not entry.is_file():
        raise FileNotFoundError("Install Node and run npm ci in benchmarks/setup/js_toolkit")
    return [node, str(entry)]


def custom_command(workdir, spec, base, env, worker_path):
    deno = str(LOCAL_DENO) if LOCAL_DENO.is_file() else shutil.which("deno")
    if not deno or not (HERE / "custom/node_modules/yaml/package.json").is_file():
        raise FileNotFoundError("Install Deno and run npm ci in benchmarks/setup/js_toolkit/custom")
    spec_path = Path(spec).absolute()
    workdir, spec, worker = (Path(p).resolve(strict=True) for p in (workdir, spec, worker_path))
    if not worker.is_relative_to(workdir) or not worker.is_file() or not spec.is_file():
        raise ValueError("Expected a worker in the trial directory and a local OpenAPI file")
    if not set(env) <= CUSTOM_ENV:
        raise ValueError("The worker accepts only SPEC, BASE_URL, and TOKEN environment mappings")
    origin = urlsplit(base)
    if origin.scheme != "http" or origin.hostname != "127.0.0.1" or not origin.port:
        raise ValueError("The custom worker requires a loopback fixture origin")
    read_paths = list(dict.fromkeys((str(spec_path), str(spec), str(worker))))
    if any("," in path for path in read_paths):
        raise ValueError("Deno permission paths must not contain commas")

    command = [
        deno, "run", "--quiet", "--no-config", "--no-lock", "--no-remote",
        "--node-modules-dir=manual", "--cached-only", "--deny-import", "--no-prompt",
        "--v8-flags=--max-old-space-size=128", f"--allow-net=127.0.0.1:{origin.port}",
        "--allow-read=" + ",".join(read_paths), "--allow-env=SPEC,BASE_URL,TOKEN",
        "--ignore-env=LOG_STREAM,LOG_TOKENS",
        str(HERE / "custom/host.mjs"), str(worker),
    ]
    return command, {**env, "DENO_DIR": str(workdir / ".deno"), "DENO_NO_UPDATE_CHECK": "1"}
