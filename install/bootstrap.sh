#!/bin/sh
set -eu
if [ "$#" -eq 0 ]; then
  printf '%s\n' 'Usage: sudo ./bot-linux-ARCH install|update [options]' >&2
  exit 2
fi
case "$1" in install|update) ;; *) printf '%s\n' 'Expected install or update.' >&2; exit 2 ;; esac
if [ "$(id -u)" -ne 0 ]; then printf '%s\n' 'Run installation or update with sudo.' >&2; exit 1; fi
temporary_base=${BOT_INSTALL_TMPDIR:-/opt}
mkdir -p -- "$temporary_base"
temporary=$(mktemp -d "$temporary_base/bot-bundle.XXXXXXXX")
trap 'rm -rf -- "$temporary"' EXIT HUP INT TERM
printf '%s\n' 'Verifying and unpacking bot...'
payload_line=$(awk '/^__BOT_PAYLOAD_BELOW__$/ {print NR + 1; exit}' "$0")
tail -n +"$payload_line" "$0" > "$temporary/payload.tar.gz"
printf '%s  %s\n' '@PAYLOAD_SHA256@' "$temporary/payload.tar.gz" | sha256sum -c - >/dev/null
mkdir "$temporary/bundle"
tar -xzf "$temporary/payload.tar.gz" -C "$temporary/bundle"
action=$1
shift
"$temporary/bundle/runtime/node/bin/node" "$temporary/bundle/install/manage.mjs" "$action" --bundle "$temporary/bundle" "$@"
exit $?
__BOT_PAYLOAD_BELOW__
