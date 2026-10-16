#!/bin/sh
# Checks install.sh syntax and that release builds are reproducible and verifiable.
set -eu
cd "$(dirname "$0")/.."
backup=$(mktemp -d)
cp install.sh "$backup/"
restore() { cp "$backup/install.sh" install.sh; rm -rf "$backup"; }
trap restore EXIT

sh -n install.sh
grep -q '^# BEGIN CHECKSUMS$' install.sh
# A truncated download must not run a partial script: everything is in main.
tail -n 1 install.sh | grep -q '^main "\$@"$' || { echo "install.sh must end by calling main" >&2; exit 1; }

sh scripts/release.sh 0.0.0-test >/dev/null
cp dist/SHA256SUMS "$backup/first"
grep -q '^VERSION=0.0.0-test$' install.sh
grep -q '^SHA_plugin_openai_compatible_linux_arm64=' install.sh
sh scripts/release.sh 0.0.0-test >/dev/null
cmp "$backup/first" dist/SHA256SUMS || { echo "builds are not reproducible" >&2; exit 1; }

sh scripts/release.sh 0.0.0-test --verify >/dev/null

sed 's/^\(SHA_agent_linux_amd64=\).*/\10000/' install.sh >"$backup/corrupt" && cp "$backup/corrupt" install.sh
if sh scripts/release.sh 0.0.0-test --verify >/dev/null 2>&1; then
	echo "verify accepted a corrupted checksum" >&2
	exit 1
fi
echo "release_test: ok"
