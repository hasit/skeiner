"""Install the pinned Deno release used in the discovery benchmark."""
import hashlib
import os
import platform
import shutil
import subprocess
import tempfile
import zipfile
from pathlib import Path

from client import LOCAL_DENO

VERSION = "2.9.8"
# Asset SHA-256 values from github.com/denoland/deno/releases/tag/v2.9.8
CHECKSUMS = {
    "aarch64-apple-darwin": "9f0bf660393da2eaff044cdc7444cb08c73f235849b61d224d825a194ce7c98e",
    "x86_64-apple-darwin": "4b2f5edd2639b7470f034238b321d7002d8dfb4726d8558f52c17afac370eb2c",
    "aarch64-unknown-linux-gnu": "d49596c20b9788cf8f5d8ddf644103d4bde56ed239753f0bdb92ba9361d51905",
    "x86_64-unknown-linux-gnu": "e890975bc2549807bbcc0b584ffafda72cc7c93349dd99c80483dd784f6c4609",
}


def main():
    systems = {"Darwin": "apple-darwin", "Linux": "unknown-linux-gnu"}
    machine = platform.machine().lower()
    architecture = "aarch64" if machine == "arm64" else machine
    target = architecture + "-" + systems.get(platform.system(), platform.system())
    if target not in CHECKSUMS:
        raise SystemExit(f"No pinned Deno download for {target}")
    url = f"https://github.com/denoland/deno/releases/download/v{VERSION}/deno-{target}.zip"
    LOCAL_DENO.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=LOCAL_DENO.parent) as directory:
        archive = Path(directory) / "deno.zip"
        subprocess.run(["curl", "--disable", "--fail", "--show-error", "--silent", "--location",
                        "--proto", "=https", "--proto-redir", "=https", "--max-time", "120",
                        "--retry", "2", "--output", str(archive), url], check=True)
        if hashlib.sha256(archive.read_bytes()).hexdigest() != CHECKSUMS[target]:
            raise SystemExit(f"SHA-256 verification failed for {target}")
        binary = Path(directory) / "deno"
        with zipfile.ZipFile(archive) as package, package.open("deno") as src, binary.open("wb") as dst:
            shutil.copyfileobj(src, dst)
        binary.chmod(0o755)
        os.replace(binary, LOCAL_DENO)
    print(LOCAL_DENO)


if __name__ == "__main__":
    main()
