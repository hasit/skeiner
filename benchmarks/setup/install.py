"""Time clean local installs separately from authoring an integration."""
import argparse
import hashlib
import json
import os
import platform
import shutil
import subprocess
import tempfile
from datetime import datetime, timezone
from pathlib import Path
from time import perf_counter

HERE = Path(__file__).resolve().parent
RELEASE = "v0.1.0-alpha.2"
ARCHIVES = {
    "darwin-amd64": "82799387a76a8ea05696763fe68ea4ceef4b83310b1f312d0562ac8d596f27a7",
    "darwin-arm64": "1bb0b4527019bdf55d5dad4380a39f6be0b43e5b90a8082728060a1e137118e0",
    "linux-amd64": "3b280c7b846953c40095ee3345fb6edc1f89621fee696850da0d3bf73509ac4c",
    "linux-arm64": "7b346d569cab889f92a5bf718413b6f365483df5e052e8a321b7b37e574d076f",
}


def install(arm, trial, work, target):
    prefix = work / "install"
    prefix.mkdir()
    env = {"PATH": os.environ["PATH"], "NO_UPDATE_NOTIFIER": "1"}
    if arm == "skeiner":
        asset = f"skeiner-{target}.tar.gz"
        url = f"https://github.com/hasit/skeiner/releases/download/{RELEASE}/{asset}"
        archive = work / asset
        start = perf_counter()
        subprocess.run(["curl", "--disable", "--fail", "--silent", "--show-error", "--location",
                        "--proto", "=https", "--proto-redir", "=https", "--max-time", "120",
                        "-o", str(archive), url], env=env, check=True)
        if hashlib.sha256(archive.read_bytes()).hexdigest() != ARCHIVES[target]:
            raise ValueError("Skeiner archive checksum differs from the release")
        subprocess.run(["tar", "-xzf", str(archive), "-C", str(prefix)], env=env, check=True)
        if subprocess.check_output([str(prefix / "skeiner"), "version"], env=env, text=True).strip() != RELEASE[1:]:
            raise ValueError("unexpected Skeiner version")
    else:
        source = HERE / "js_toolkit"
        if arm == "custom":
            source = source / "custom"
        for name in ("package.json", "package-lock.json"):
            shutil.copyfile(source / name, prefix / name)
        start = perf_counter()
        subprocess.run(["npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund", "--loglevel=error",
                        "--userconfig", str(work / "empty.npmrc"), "--cache", str(work / "cache")],
                       env=env, cwd=prefix, stdout=subprocess.DEVNULL, check=True, timeout=180)
        # Check that the declared entry points load using the installed runtime.
        if arm == "package":
            subprocess.run([str(prefix / "node_modules/.bin/openapi-mcp-server"), "--version"], cwd=prefix,
                           env=env, stdout=subprocess.DEVNULL, check=True)
        else:
            subprocess.run(["node", "--input-type=module", "-e",
                            "await import('@modelcontextprotocol/sdk/server/index.js'); "
                            "await import('yaml'); await import('zod');"], cwd=prefix, env=env, check=True)
    elapsed = round(perf_counter() - start, 3)
    return {"arm": arm, "trial": trial, "seconds": elapsed,
            "installed_file_bytes": sum(p.stat().st_size for p in prefix.rglob("*")
                                        if not p.is_symlink() and p.is_file())}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, required=True, help="new JSON file")
    args = parser.parse_args()
    machine = platform.machine().lower()
    arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(machine, machine)
    target = platform.system().lower() + "-" + arch
    if target not in ARCHIVES or args.out.exists():
        parser.error("choose a new output file and a supported macOS or Linux host")
    result = {"observed_at": datetime.now(timezone.utc).isoformat(), "target": target,
              "release": RELEASE, "node": subprocess.check_output(["node", "--version"], text=True).strip(),
              "npm": subprocess.check_output(["npm", "--version"], text=True).strip(), "runs": []}
    arms = ["skeiner", "package", "custom"]
    for offset in range(3):
        for arm in arms[offset:] + arms[:offset]:
            with tempfile.TemporaryDirectory() as tmp:
                row = install(arm, offset + 1, Path(tmp), target)
            result["runs"].append(row)
            print(arm, row["seconds"], flush=True)
    args.out.write_text(json.dumps(result, indent=2) + "\n")


if __name__ == "__main__":
    main()
