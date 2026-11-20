#!/bin/sh
# Build one release archive from the repository root.
set -eu
if [ "$#" -ne 2 ]; then
    echo "usage: scripts/package.sh {linux|darwin} {amd64|arm64}" >&2
    exit 2
fi
case "$1/$2" in
    linux/amd64|linux/arm64|darwin/amd64|darwin/arm64) ;;
    *) echo "unsupported target: $1/$2" >&2; exit 2 ;;
esac
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
CGO_ENABLED=0 GOOS="$1" GOARCH="$2" go build -mod=readonly -trimpath \
    -ldflags='-s -w' -o "$stage/skeiner" ./cmd/skeiner
cp LICENSE THIRD_PARTY_NOTICES.txt "$stage/"
mkdir -p "$stage/examples/rates" "$stage/examples/ci" "$stage/evals" dist
cp examples/rates/compare.js examples/rates/openapi.yaml examples/rates/quickstart.py \
    examples/rates/snapshot.json "$stage/examples/rates/"
cp examples/ci/openapi.yaml examples/ci/find-lines.js "$stage/examples/ci/"
cp evals/ci.yaml evals/ci-structured.yaml evals/log-scan.yaml "$stage/evals/"
cat > "$stage/README.txt" <<'EOF'
Skeiner runs JavaScript programs against an OpenAPI service.

From the extracted directory:

    ./skeiner version
    python3 examples/rates/quickstart.py
    ./skeiner eval --spec examples/ci/openapi.yaml --suite evals/ci.yaml
    ./skeiner eval --spec examples/ci/openapi.yaml --suite evals/log-scan.yaml --program examples/ci/find-lines.js

The examples use local snapshots. The binary includes its JavaScript runtime.
For setup, architecture, benchmarks and source, visit https://github.com/hasit/skeiner.
EOF
archive="dist/skeiner-$1-$2.tar.gz"
tar -czf "$archive" -C "$stage" skeiner README.txt LICENSE THIRD_PARTY_NOTICES.txt examples evals
echo "$archive"
