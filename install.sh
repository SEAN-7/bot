#!/bin/sh
# Install or upgrade the agent harness on a Linux VM.
#
#   printf 'header = "Authorization: Bearer %s"\n' "$GITHUB_TOKEN" |
#     curl -fsSL -K - https://raw.githubusercontent.com/SEAN-7/bot/vX.Y.Z/install.sh |
#     sudo --preserve-env=GITHUB_TOKEN sh
#
# Options: --sudo grants the agent passwordless sudo (default: none; an
# existing grant is left alone). The token is used only for downloads, is
# never written to disk and never appears in command arguments.
set -eu

REPO=SEAN-7/bot
# BEGIN CHECKSUMS
VERSION=0.2.0
SHA_agent_linux_amd64=74c1570da547f3c202425e3c8f330e8303821906b2fe10f685eabbc939d4f315
SHA_agent_linux_arm64=4ff40ffc4a9a3ae5b91e8940ae67583817901844d3ee2ee6cc4dbf7533f0a8e5
# END CHECKSUMS

ROLLBACK=no

# finish cleans up, and restores the previous binary and service if an
# upgrade failed after it was stopped.
finish() {
	status=$?
	if [ "$status" != 0 ] && [ "$ROLLBACK" = yes ] && [ -x /usr/local/bin/agent.prev ]; then
		mv /usr/local/bin/agent.prev /usr/local/bin/agent
		systemctl start agent 2>/dev/null || true
		printf 'install: failed; restored the previous version\n' >&2
	fi
	rm -rf "${tmp:-}"
	exit "$status"
}

main() {
	GRANT_SUDO=no
	for arg in "$@"; do
		case $arg in
		--sudo) GRANT_SUDO=yes ;;
		*) echo "unknown option: $arg" >&2; exit 2 ;;
		esac
	done

	say() { printf '%s\n' "$*"; }
	die() { printf 'install: %s\n' "$*" >&2; exit 1; }
	ask() { # prompt; reads from the terminal even when this script is piped to sh
		printf '%s' "$1" >/dev/tty
		read -r answer </dev/tty
		printf '%s' "$answer"
	}

	[ "$(id -u)" = 0 ] || die "run as root (sudo)"
	[ -n "${GITHUB_TOKEN:-}" ] || die "set GITHUB_TOKEN to a token with read access to $REPO"
	[ "$VERSION" != dev ] || die "this install.sh is not from a release; use the one at a release tag"
	for tool in curl sha256sum useradd systemctl loginctl crontab; do
		command -v "$tool" >/dev/null 2>&1 || die "missing required tool: $tool"
	done

	case $(uname -m) in
	x86_64) ARCH=amd64 ;;
	aarch64 | arm64) ARCH=arm64 ;;
	*) die "unsupported architecture $(uname -m)" ;;
	esac

	api() { # the token reaches curl on stdin (printf is a shell builtin), never in argv
		printf 'header = "Authorization: Bearer %s"\n' "$GITHUB_TOKEN" |
			curl -fsSL -K - -H "X-GitHub-Api-Version: 2022-11-28" "$@"
	}

	# download NAME DEST: fetch a release asset through the GitHub API.
	download() {
		id=$(api -H "Accept: application/vnd.github+json" "https://api.github.com/repos/$REPO/releases/tags/v$VERSION" |
			tr -d '\n' | grep -o '"url": *"[^"]*/releases/assets/[0-9]*"[^{}]*"name": *"'"$1"'"' |
			grep -o 'assets/[0-9]*' | head -n 1 | cut -d/ -f2) || true
		[ -n "$id" ] || die "release v$VERSION has no asset $1"
		api -H "Accept: application/octet-stream" -o "$2" "https://api.github.com/repos/$REPO/releases/assets/$id"
	}

	as_agent() {
		if command -v runuser >/dev/null 2>&1; then runuser -u agent -- "$@"; else su -s /bin/sh agent -c "$*"; fi
	}

	say "Installing agent harness v$VERSION ($ARCH)"
	tmp=$(mktemp -d)
	trap finish EXIT
	download "agent-linux-$ARCH" "$tmp/agent"
	eval "want=\$SHA_agent_linux_$ARCH"
	got=$(sha256sum "$tmp/agent" | cut -d' ' -f1)
	[ "$got" = "$want" ] || die "checksum mismatch for agent-linux-$ARCH: got $got, want $want"

	upgrade=no
	if systemctl is-active --quiet agent 2>/dev/null; then
		upgrade=yes
	fi
	if [ -x /usr/local/bin/agent ]; then
		cp -p /usr/local/bin/agent /usr/local/bin/agent.prev
		ROLLBACK=yes
	fi
	if [ "$upgrade" = yes ]; then
		say "Stopping the agent; active runs finish first (up to 15 minutes)..."
		systemctl stop agent
	fi
	install -m 755 -o root -g root "$tmp/agent" /usr/local/bin/agent

	id agent >/dev/null 2>&1 || useradd --create-home --home-dir /home/agent --shell /bin/bash agent
	id agent-provider >/dev/null 2>&1 || useradd --system --no-create-home --home-dir / --shell /usr/sbin/nologin agent-provider

	# /etc/agent is traversable (not listable) so agent-provider can reach plugins/;
	# credentials/ and settings.json stay root-only.
	install -d -m 711 -o root -g root /etc/agent
	install -d -m 700 -o root -g root /etc/agent/credentials
	install -d -m 750 -o root -g agent-provider /etc/agent/plugins
	install -d -m 755 -o root -g root /var/lib/agent /var/log/agent
	install -d -m 700 -o root -g root /var/lib/agent/owner
	[ -f /etc/agent/settings.json ] || printf '{"max_agents": 16, "repository": "%s"}\n' "$REPO" >/etc/agent/settings.json
	chmod 600 /etc/agent/settings.json

	if [ -f /home/agent/config.json ]; then
		# Upgrade: keep the configuration and refresh installed plugins.
		for plugin in /etc/agent/plugins/*; do
			[ -f "$plugin" ] || continue
			GITHUB_TOKEN="$GITHUB_TOKEN" /usr/local/bin/agent provider install "$(basename "$plugin")" >/dev/null ||
				say "warning: could not update plugin $(basename "$plugin"); keeping the installed version"
		done
	else
		say "Which provider plugin?"
		say "  1) anthropic"
		say "  2) openai-compatible"
		case $(ask "Choice [1]: ") in
		2) provider=openai-compatible ;;
		*) provider=anthropic ;;
		esac
		GITHUB_TOKEN="$GITHUB_TOKEN" /usr/local/bin/agent provider install "$provider"
		while :; do
			model=$(ask "Model ID (from the list above): ")
			/usr/local/bin/agent provider describe | grep -qF "  $model (" && break
			say "Unknown model: $model"
		done
		printf '{"provider": "%s", "model": "%s", "options": {}}\n' "$provider" "$model" | as_agent /usr/local/bin/agent _seed /home/agent
	fi

	loginctl enable-linger agent

	cat >/etc/systemd/system/agent.service <<'UNIT'
[Unit]
Description=Agent harness
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/agent daemon
Restart=always
RestartSec=2
# A stop asks only the daemon to finish: active runs complete (up to 15
# minutes) before it exits, so upgrades and restarts lose no context.
KillMode=mixed
TimeoutStopSec=17min

[Install]
WantedBy=multi-user.target
UNIT

	# No harness heartbeat: the agent schedules its own wake-ups. Remove the
	# one earlier releases installed, keeping the agent's own entries.
	if crontab -u agent -l 2>/dev/null | grep -q 'Heartbeat: anything pending'; then
		crontab -u agent -l | grep -v 'Heartbeat: anything pending' | crontab -u agent -
	fi

	if [ "$GRANT_SUDO" = yes ]; then
		printf 'agent ALL=(ALL) NOPASSWD:ALL\n' >"$tmp/sudoers"
		visudo -cf "$tmp/sudoers" >/dev/null
		install -m 440 -o root -g root "$tmp/sudoers" /etc/sudoers.d/agent
		say "WARNING: the agent has passwordless sudo. Credential, journal and owner-message protections are now conventions only. Set a spend limit at your provider."
	fi

	systemctl daemon-reload
	systemctl enable --now agent
	ROLLBACK=no

	if [ "$upgrade" = yes ]; then
		say "Upgraded to v$VERSION and restarted."
	else
		say "Installed v$VERSION. Next:"
		say "  sudo agent send \"Hello. Introduce yourself and tell me what you can do.\""
		say "  agent log"
	fi
}

main "$@"
