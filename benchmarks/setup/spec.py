"""Install and verify the unchanged 1Password Connect OpenAPI document."""
import argparse
import hashlib
import os
import ssl
import tempfile
from pathlib import Path
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parents[2]
DEFAULT_SPEC = ROOT / "bin" / "setup" / "connect.yaml"
REVISION = "420a61d55b8744b3732afa65f340ab5629526190"
URL = f"https://raw.githubusercontent.com/1Password/connect/{REVISION}/docs/openapi/spec.yaml"
SHA256 = "e597c52210719e5bf9b27556d6f3ead28434a35a8e8c827f71eec19899cc873d"


def verify(path):
    if hashlib.sha256(path.read_bytes()).hexdigest() != SHA256:
        raise ValueError(f"OpenAPI document checksum mismatch: {path}")


def install(path):
    if path.exists():
        verify(path)
        return
    trust = os.environ.get("SSL_CERT_FILE") or ssl.get_default_verify_paths().cafile
    if not trust and Path("/etc/ssl/cert.pem").exists():
        trust = "/etc/ssl/cert.pem"
    with urlopen(URL, context=ssl.create_default_context(cafile=trust), timeout=30) as response:
        content = response.read(1024 * 1024)
    if hashlib.sha256(content).hexdigest() != SHA256:
        raise ValueError("downloaded OpenAPI document differs from the pinned version")
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as output:
        output.write(content)
    Path(output.name).replace(path)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, default=DEFAULT_SPEC)
    dest = parser.parse_args().out
    install(dest)
    print(f"Verified {dest.stat().st_size} bytes at {dest}")
