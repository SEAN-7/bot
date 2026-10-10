#!/bin/sh
# Checks install.sh syntax and that release builds are reproducible and verifiable.
set -eu
cd "$(dirname "$0")/.."
backup=$(mktemp -d)
cp install.sh plugins/index.json "$backup/"
restore() { cp "$backup/install.sh" install.sh; cp "$backup/index.json" plugins/index.json; rm -rf "$backup"; }
trap restore EXIT

sh -n install.sh
grep -q '^# BEGIN CHECKSUMS$' install.sh

sh scripts/release.sh 0.0.0-test >/dev/null
cp dist/SHA256SUMS "$backup/first"
grep -q '"version": "0.0.0-test"' plugins/index.json
grep -q '^VERSION=0.0.0-test$' install.sh
sh scripts/release.sh 0.0.0-test >/dev/null
cmp "$backup/first" dist/SHA256SUMS || { echo "builds are not reproducible" >&2; exit 1; }

sh scripts/release.sh 0.0.0-test --verify >/dev/null

sed 's/^\(SHA_agent_linux_amd64=\).*/\10000/' install.sh >"$backup/corrupt" && cp "$backup/corrupt" install.sh
if sh scripts/release.sh 0.0.0-test --verify >/dev/null 2>&1; then
	echo "verify accepted a corrupted checksum" >&2
	exit 1
fi
echo "release_test: ok"
