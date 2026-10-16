#!/bin/sh
# Build reproducible release binaries into dist/.
#   scripts/release.sh VERSION           write the checksums in install.sh
#   scripts/release.sh VERSION --verify  fail if the committed checksums differ
set -eu
VERSION=$1
MODE=${2:-write}
cd "$(dirname "$0")/.."

# Build with exactly the toolchain go.mod names: the toolchain line is only a
# minimum, and a different patch release produces different binaries.
GOTOOLCHAIN=$(sed -n 's/^toolchain //p' go.mod)
export GOTOOLCHAIN

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
for a in $ARCHES; do build ./cmd/agent "$a" "dist/agent-linux-$a"; done

# The checksum block pins the agent binary and the release's own plugins;
# a plugin's name has its hyphens as underscores in the variable name.
{
	echo "# BEGIN CHECKSUMS"
	echo "VERSION=$VERSION"
	for a in $ARCHES; do echo "SHA_agent_linux_$a=$(sum "dist/agent-linux-$a")"; done
	for p in $PLUGINS; do
		for a in $ARCHES; do echo "SHA_plugin_$(printf %s "$p" | tr - _)_linux_$a=$(sum "dist/plugin-$p-linux-$a")"; done
	done
	echo "# END CHECKSUMS"
} >"$tmp/block"

sed -n '/^# BEGIN CHECKSUMS$/,/^# END CHECKSUMS$/p' install.sh >"$tmp/current"
if [ "$MODE" = --verify ]; then
	cmp -s "$tmp/block" "$tmp/current" || fail "install.sh checksums do not match the built binaries"
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
