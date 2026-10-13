#!/bin/sh
# Install or upgrade the agent harness on a Linux VM.
#
#   printf 'header = "Authorization: Bearer %s"\n' "$GITHUB_TOKEN" |
#     curl -fsSL -K - https://raw.githubusercontent.com/SEAN-7/bot/vX.Y.Z/install.sh |
#     sudo --preserve-env=GITHUB_TOKEN sh
#
# The token is used only for downloads, is never written to disk and never
# appears in command arguments.
set -eu

REPO=SEAN-7/bot
# BEGIN CHECKSUMS
VERSION=0.4.0
SHA_agent_linux_amd64=c7912bf8ee616a9649dbf667662d84027f26778a57b1573d35cb65ffab4536fc
SHA_agent_linux_arm64=2d14b6e11f6003b0b8182b0b82c494f9692469ff65ddfdc71a04c73e760ac6fb
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
	if [ "$#" != 0 ]; then
		echo "unknown option: $1" >&2
		exit 2
	fi

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
	for tool in curl sha256sum useradd gpasswd pkill pgrep systemctl loginctl crontab; do
		command -v "$tool" >/dev/null 2>&1 || die "missing required tool: $tool"
	done

	# A release before 0.4.0 ran the main agent as the user agent, without
	# /etc/agent/agents/main, and named agents by path in pool.json; its
	# migration left marker files while unfinished. This release cannot
	# migrate it, so it stops before changing anything. A user agent on a
	# fresh or 0.4 machine is someone else's.
	old=no
	if id agent >/dev/null 2>&1 && [ ! -e /etc/agent/agents/main ]; then
		case $(cat /var/lib/agent/version 2>/dev/null || true) in
		0.[0-3].*) old=yes ;;
		esac
	fi
	if grep -q '"agent" *: *"/' /var/lib/agent/pool.json 2>/dev/null ||
		[ -e /var/lib/agent/migrating ] || [ -e /var/lib/agent/migrating-user ]; then
		old=yes
	fi
	[ "$old" = no ] || die "this machine runs a release before 0.4.0; upgrade it with the v0.4.0 install.sh first"

	# Agents share files with each other through setfacl, from the acl package.
	if ! command -v setfacl >/dev/null 2>&1; then
		if command -v apt-get >/dev/null 2>&1; then
			DEBIAN_FRONTEND=noninteractive apt-get install -y acl >/dev/null ||
				say "warning: could not install the acl package; agents cannot share files with setfacl until it is installed"
		else
			say "warning: setfacl is missing; install the acl package so agents can share files"
		fi
	fi

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

	say "Installing agent harness v$VERSION ($ARCH)"
	tmp=$(mktemp -d)
	trap finish EXIT
	download "agent-linux-$ARCH" "$tmp/agent"
	eval "want=\$SHA_agent_linux_$ARCH"
	got=$(sha256sum "$tmp/agent" | cut -d' ' -f1)
	[ "$got" = "$want" ] || die "checksum mismatch for agent-linux-$ARCH: got $got, want $want"

	# An upgrade keeps the configuration and creates no main agent. Every
	# completed install records its version (the human may have removed
	# main since); settings.json alone may be a fresh install that failed
	# before choosing a provider, which a rerun finishes as one.
	upgrade=no
	if [ -e /var/lib/agent/version ] || [ -e /etc/agent/agents/main ]; then upgrade=yes; fi

	# The release installed now is the one to restore if this run fails.
	if [ -x /usr/local/bin/agent ]; then
		cp -p /usr/local/bin/agent /usr/local/bin/agent.prev
		ROLLBACK=yes
	fi
	active=no
	if systemctl is-active --quiet agent 2>/dev/null; then
		active=yes
		say "Stopping the agent; active runs finish first (up to 15 minutes)..."
	fi
	# Whenever the unit exists, even inactive or between restarts, so it
	# does not restart on the new binary before the install is complete.
	if [ "$active" = yes ] || [ -f /etc/systemd/system/agent.service ]; then
		systemctl stop agent
	fi

	install -m 755 -o root -g root "$tmp/agent" /usr/local/bin/agent

	id agent-provider >/dev/null 2>&1 || useradd --system --no-create-home --home-dir / --shell /usr/sbin/nologin agent-provider

	# /etc/agent is traversable (not listable) so agent-provider can reach plugins/;
	# credentials/, agents/ and settings.json stay root-only.
	install -d -m 711 -o root -g root /etc/agent
	install -d -m 700 -o root -g root /etc/agent/credentials /etc/agent/agents
	install -d -m 750 -o root -g agent-provider /etc/agent/plugins
	install -d -m 755 -o root -g root /var/lib/agent /var/log/agent
	[ -f /etc/agent/settings.json ] || printf '{"max_agents": 16, "repository": "%s"}\n' "$REPO" >/etc/agent/settings.json
	chmod 600 /etc/agent/settings.json

	# Pool users agent-1 to agent-<max_agents>, before the daemon starts.
	# Their homes start empty (no skeleton files), as an agent's home does.
	max_agents=$(sed -n 's/.*"max_agents": *\([0-9]*\).*/\1/p' /etc/agent/settings.json | head -n 1)
	max_agents=${max_agents:-16}
	i=1
	while [ "$i" -le "$max_agents" ]; do
		id "agent-$i" >/dev/null 2>&1 ||
			useradd --no-create-home --home-dir "/home/agent-$i" --user-group --shell /bin/bash "agent-$i"
		mkdir -p "/home/agent-$i"
		chown "agent-$i:agent-$i" "/home/agent-$i"
		chmod 2750 "/home/agent-$i"
		i=$((i + 1))
	done

	if [ "$upgrade" = yes ]; then
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
	fi

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

	systemctl daemon-reload
	# A daemon that failed to start before this run may have hit systemd's
	# start limit.
	systemctl reset-failed agent 2>/dev/null || true
	systemctl enable --now agent
	ROLLBACK=no
	# The installed version, for the next upgrade. Release notes do not
	# depend on it: they ride along with each agent's next wake, and a new
	# agent starts current. The agent is running by now, so a failure here
	# only warns.
	printf '%s\n' "$VERSION" >/var/lib/agent/version ||
		say "warning: could not record v$VERSION in /var/lib/agent/version; the agent is running v$VERSION"

	if [ "$upgrade" = yes ]; then
		say "Upgraded to v$VERSION and started."
	else
		if ! /usr/local/bin/agent new main --provider "$provider" --model "$model"; then
			retry="sudo agent new main --provider $provider --model $model"
			if [ -e /etc/agent/agents/main ]; then retry="sudo agent remove main, then $retry"; fi
			die "the harness is running, but creating the main agent failed (see above); once the cause is fixed, run: $retry"
		fi
		say "Installed v$VERSION. Next:"
		say "  sudo agent send main \"Hello. Read your README, then tell me how you plan to keep notes.\""
		say "  sudo agent log main"
	fi
}

main "$@"
