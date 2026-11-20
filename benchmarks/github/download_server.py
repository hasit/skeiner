"""Download and verify GitHub's native v2.2.0 MCP server for Linux or macOS."""

import hashlib
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tarfile
import tempfile

from official_mcp import DEFAULT_BINARY, VERSION


# Official release manifest: github/github-mcp-server/releases/tag/v2.2.0
CHECKSUMS = {
    ("Darwin", "arm64"): "5e46b05b87989d23267f0a69ee60a3acd1de51a72512ee5fcc2786ca60c54a6a",
    ("Darwin", "x86_64"): "a42e667b99915cf368b92aa419fd2f753549ea7133cb3572a3e7f3e2d4a8675b",
    ("Linux", "arm64"): "d21e4fca8603b59d99380f1c332aaea36d73033d52b389867adf2124ee150983",
    ("Linux", "x86_64"): "157c5664b273e5070126d414f88cbab765630acde588f75dafdf9998c2235c51",
}


def main():
    system = platform.system()
    machine = platform.machine().lower()
    arch = "arm64" if machine == "aarch64" else machine
    digest = CHECKSUMS.get((system, arch))
    if digest is None:
        raise SystemExit(f"Choose a native GitHub MCP server for {system}/{arch} from the v{VERSION} release")
    name = f"github-mcp-server_{system}_{arch}.tar.gz"
    url = f"https://github.com/github/github-mcp-server/releases/download/v{VERSION}/{name}"
    DEFAULT_BINARY.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=DEFAULT_BINARY.parent) as temp:
        archive = Path(temp) / name
        subprocess.run(["curl", "--disable", "--fail", "--silent", "--show-error", "--location",
                        "--proto", "=https", "--proto-redir", "=https", "--max-time", "120",
                        "--retry", "2", "--output", str(archive), url], check=True)
        if hashlib.sha256(archive.read_bytes()).hexdigest() != digest:
            raise SystemExit(f"SHA-256 verification failed for {name}")
        binary = Path(temp) / DEFAULT_BINARY.name
        with tarfile.open(archive, "r:gz") as tar:
            entry = tar.getmember(DEFAULT_BINARY.name)
            if not entry.isfile():
                raise SystemExit("Expected an executable file in the release archive")
            with tar.extractfile(entry) as source, binary.open("wb") as out:
                shutil.copyfileobj(source, out)
        binary.chmod(0o755)
        os.replace(binary, DEFAULT_BINARY)
    print(DEFAULT_BINARY)


if __name__ == "__main__":
    main()
