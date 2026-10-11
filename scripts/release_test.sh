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
# The token must never appear in a command's arguments, where any local user
# (including the agent) could read it with ps.
if grep -n 'GITHUB_TOKEN' install.sh | grep -v -e "printf 'header" -e '^[0-9]*:#' -e 'GITHUB_TOKEN="\$GITHUB_TOKEN" /usr/local/bin/agent' -e 'GITHUB_TOKEN:-' | grep .; then
	echo "install.sh passes GITHUB_TOKEN on a command line" >&2
	exit 1
fi
# A truncated download must not run a partial script: everything is in main.
tail -n 1 install.sh | grep -q '^main "\$@"$' || { echo "install.sh must end by calling main" >&2; exit 1; }

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
