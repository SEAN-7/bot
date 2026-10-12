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
VERSION=0.2.1
SHA_agent_linux_amd64=6e00ece70c15f00e9717574bfa74890e19a0dedecdc59a0a5b3d95f2ccf32604
SHA_agent_linux_arm64=cacdfe78caaba50f0bed3a3ab41cad18c10aa478542978f952ebcde692846fdc
# END CHECKSUMS

ROLLBACK=no
MIGRATED=no

# finish cleans up, and restores the previous binary and service if an
# upgrade failed after it was stopped. Once subagents have been migrated,
# even in part, the previous release must not run: the agent stays stopped
# on this one until a rerun completes.
finish() {
	status=$?
	if [ "$status" != 0 ] && [ "$ROLLBACK" = yes ] && [ -x /usr/local/bin/agent.prev ]; then
		mv /usr/local/bin/agent.prev /usr/local/bin/agent
		systemctl start agent 2>/dev/null || true
		printf 'install: failed; restored the previous version\n' >&2
	elif [ "$status" != 0 ] && [ "$MIGRATED" = yes ]; then
		printf 'install: failed (status %s) after migrating subagents; the agent stays stopped on v%s. Fix the cause and rerun install.sh.\n' "$status" "$VERSION" >&2
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
	for tool in curl sha256sum useradd gpasswd pkill pgrep systemctl loginctl crontab; do
		command -v "$tool" >/dev/null 2>&1 || die "missing required tool: $tool"
	done
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
	# An earlier run migrated subagents part way: the previous release must
	# never run on them again, whatever fails now.
	if [ -e /var/lib/agent/migrating ]; then
		ROLLBACK=no
		MIGRATED=yes
	fi
	if [ "$upgrade" = yes ]; then
		say "Stopping the agent; active runs finish first (up to 15 minutes)..."
	fi
	# Whenever the unit exists, even inactive or between restarts: a unit
	# restarting on the new binary would race the migration below.
	if [ "$upgrade" = yes ] || [ -f /etc/systemd/system/agent.service ]; then
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

	# Subagent users agent-1 to agent-<max_agents - 1>. Their homes start
	# empty (no skeleton files), as a subagent's home does.
	max_agents=$(sed -n 's/.*"max_agents": *\([0-9]*\).*/\1/p' /etc/agent/settings.json | head -n 1)
	max_agents=${max_agents:-16}
	i=1
	while [ "$i" -lt "$max_agents" ]; do
		id "agent-$i" >/dev/null 2>&1 ||
			useradd --no-create-home --home-dir "/home/agent-$i" --user-group --shell /bin/bash "agent-$i"
		mkdir -p "/home/agent-$i"
		chown "agent-$i:agent-$i" "/home/agent-$i"
		chmod 2750 "/home/agent-$i"
		i=$((i + 1))
	done
	chmod 750 /home/agent

	# Releases before 0.3.0 ran every subagent as agent, in a directory under
	# /home/agent/agents holding its inbox/. Move them to pool users, after a
	# root-only backup. If nothing was moved, a failure restores the previous
	# version. Once files have moved, the previous version must not run them:
	# the agent stays stopped on this one and a rerun finishes the migration.
	# The marker records the first run's backup, of the untouched tree, and
	# stays until the migration completes.
	legacy=no
	for dir in /home/agent/agents/*/inbox; do
		# An admitted control directory has a home link; a stray inbox/ there
		# is not a legacy subagent.
		if [ -d "$dir" ] && [ ! -L "${dir%/inbox}/home" ]; then legacy=yes; fi
	done
	if [ "$legacy" = yes ]; then
		marker=/var/lib/agent/migrating
		if [ ! -e "$marker" ]; then
			mkdir -p /var/backups
			backup=/var/backups/agent-subagents-$(date -u +%Y%m%dT%H%M%SZ).tgz
			say "Backing up /home/agent/agents to $backup..."
			(umask 077; tar -C /home/agent -czf "$backup" agents)
			chmod 600 "$backup"
			printf '%s\n' "$backup" >"$marker"
		fi
		backup=$(cat "$marker")
		migrate=0
		/usr/local/bin/agent _migrate || migrate=$?
		case $migrate in
		0)
			rm -f "$marker"
			# From here a failure keeps this release: the old one cannot run
			# the migrated subagents.
			ROLLBACK=no
			MIGRATED=yes
			;;
		1)
			# Nothing was moved, by this run or an earlier one.
			[ "$MIGRATED" = yes ] || rm -f "$marker"
			die "could not move the subagents to their own users; the backup of /home/agent/agents is $backup"
			;;
		*)
			ROLLBACK=no
			MIGRATED=yes
			die "the migration stopped part way; the backup of /home/agent/agents from before it is $backup"
			;;
		esac
	elif [ -e /var/lib/agent/migrating ]; then
		rm -f /var/lib/agent/migrating # no legacy subagent is left
	fi

	configured=no
	if [ -f /home/agent/config.json ]; then
		configured=yes
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
		# A fresh seed README is current: record the version so no release notes are sent.
		printf '%s\n' "$VERSION" >/var/lib/agent/version
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

	if [ "$configured" = yes ]; then
		say "Upgraded to v$VERSION and started."
	else
		say "Installed v$VERSION. Next:"
		say "  sudo agent send \"Hello. Introduce yourself and tell me what you can do.\""
		say "  agent log"
	fi
}

main "$@"
