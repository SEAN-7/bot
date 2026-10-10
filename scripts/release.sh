#!/bin/sh
# Build reproducible release binaries into dist/.
#   scripts/release.sh VERSION           write plugins/index.json and install.sh checksums
#   scripts/release.sh VERSION --verify  fail if the committed checksums differ
set -eu
VERSION=$1
MODE=${2:-write}
cd "$(dirname "$0")/.."

PLUGINS="anthropic openai-compatible replay"
ARCHES="amd64 arm64"
LDFLAGS="-buildid= -X github.com/SEAN-7/bot/internal/version.Version=$VERSION"

sum() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}
build() { # package arch output
	CGO_ENABLED=0 GOOS=linux GOARCH=$2 go build -trimpath -buildvcs=false -ldflags "$LDFLAGS" -o "$3" "$1"
}
fail() { echo "release: $*" >&2; exit 1; }

rm -rf dist
mkdir dist
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

for p in $PLUGINS; do
	for a in $ARCHES; do build "./plugins/$p" "$a" "dist/plugin-$p-linux-$a"; done
done

{
	printf '{\n  "version": "%s",\n  "plugins": {\n' "$VERSION"
	n=0
	for p in $PLUGINS; do
		n=$((n + 1))
		printf '    "%s": {"linux-amd64": "%s", "linux-arm64": "%s"}' "$p" "$(sum "dist/plugin-$p-linux-amd64")" "$(sum "dist/plugin-$p-linux-arm64")"
		[ "$n" -lt 3 ] && printf ','
		printf '\n'
	done
	printf '  }\n}\n'
} >"$tmp/index.json"

if [ "$MODE" = --verify ]; then
	cmp -s "$tmp/index.json" plugins/index.json || fail "plugins/index.json does not match the built plugins"
else
	cp "$tmp/index.json" plugins/index.json
fi

for a in $ARCHES; do build ./cmd/agent "$a" "dist/agent-linux-$a"; done

{
	echo "# BEGIN CHECKSUMS"
	echo "VERSION=$VERSION"
	for a in $ARCHES; do echo "SHA_agent_linux_$a=$(sum "dist/agent-linux-$a")"; done
	echo "# END CHECKSUMS"
} >"$tmp/block"

sed -n '/^# BEGIN CHECKSUMS$/,/^# END CHECKSUMS$/p' install.sh >"$tmp/current"
if [ "$MODE" = --verify ]; then
	cmp -s "$tmp/block" "$tmp/current" || fail "install.sh checksums do not match the built agent"
else
	awk -v block="$tmp/block" '
		/^# BEGIN CHECKSUMS$/ { while ((getline line < block) > 0) print line; skip = 1; next }
		/^# END CHECKSUMS$/ { skip = 0; next }
		!skip { print }
	' install.sh >"$tmp/install.sh"
	cat "$tmp/install.sh" >install.sh
fi

cp install.sh dist/install.sh
(cd dist && for f in *; do echo "$(sum "$f")  $f"; done) >"$tmp/SHA256SUMS"
mv "$tmp/SHA256SUMS" dist/SHA256SUMS
echo "release $VERSION: $(ls dist | wc -l | tr -d ' ') files in dist/"
