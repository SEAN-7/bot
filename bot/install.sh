#!/bin/sh
# Install or upgrade the agent harness on a Linux VM.
#
#   curl -fsSL https://raw.githubusercontent.com/SEAN-7/bot/vX.Y.Z/bot/install.sh | sudo sh
set -eu

REPO=SEAN-7/bot
# BEGIN CHECKSUMS
VERSION=0.5.7
SHA_agent_linux_amd64=6cc79a030ceed0a574e53da72ec72e37d5fbb2f5b42743decbd3df02d7fcfe3b
SHA_agent_linux_arm64=09b3f746392f821858839707dd74c865e4f9495d8afda0a6a114185969cda0be
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
	[ "$VERSION" != dev ] || die "this install.sh is not from a release; use the one at a release tag"
	for tool in curl sha256sum useradd gpasswd pkill pgrep systemctl loginctl crontab; do
		command -v "$tool" >/dev/null 2>&1 || die "missing required tool: $tool"
	done

	# An upgrade keeps the configuration and creates no main agent. Every
	# completed install records its version (the human may have removed
	# main since); settings.json alone may be a fresh install that failed
	# before choosing a provider, which a rerun finishes as one.
	upgrade=no
	if [ -e /var/lib/agent/version ] || [ -e /etc/agent/agents/main ]; then upgrade=yes; fi

	# An upgrade comes from 0.4.1 or later, by the version it recorded. An
	# earlier release, or an installation that recorded none, upgrades with
	# the v0.4.1 install.sh first, which records it; this one stops before
	# changing anything.
	if [ "$upgrade" = yes ]; then
		installed=$(head -n 1 /var/lib/agent/version 2>/dev/null || true)
		printf '%s\n' "$installed" | awk -F. '/^[0-9]+\.[0-9]+\.[0-9]+$/ && ($1 > 0 || $2 > 4 || ($2 == 4 && $3 >= 1)) { ok = 1 } END { exit !ok }' ||
			die "this machine runs ${installed:+v$installed, }a release before 0.4.1; upgrade with the v0.4.1 install.sh first"
	fi

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

	# download NAME DEST: fetch an asset of this release.
	download() {
		curl -fsSL -o "$2" "https://github.com/$REPO/releases/download/v$VERSION/$1"
	}

	# plugin NAME: download provider plugin NAME from this release and install
	# it. agent provider install checks it against the checksums compiled into
	# the new binary and installs nothing on a mismatch.
	plugin() {
		download "plugin-$1-linux-$ARCH" "$tmp/plugin-$1" &&
			/usr/local/bin/agent provider install "$1" "$tmp/plugin-$1"
	}

	say "Installing agent harness v$VERSION ($ARCH)"
	# Downloads go to a directory only root can read (mktemp -d makes it
	# 0700), removed on exit.
	tmp=$(mktemp -d)
	trap finish EXIT
	download "agent-linux-$ARCH" "$tmp/agent" || die "could not download agent-linux-$ARCH"
	eval "want=\$SHA_agent_linux_$ARCH"
	got=$(sha256sum "$tmp/agent" | cut -d' ' -f1)
	[ "$got" = "$want" ] || die "checksum mismatch for agent-linux-$ARCH: got $got, want $want"

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
	[ -f /etc/agent/settings.json ] || printf '{"max_agents": 16}\n' >/etc/agent/settings.json
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
		for path in /etc/agent/plugins/*; do
			[ -f "$path" ] || continue
			name=$(basename "$path")
			plugin "$name" >/dev/null ||
				say "warning: could not update plugin $name; keeping the installed version"
		done
	else
		say "Which provider plugin?"
		say "  1) anthropic"
		say "  2) openai-compatible"
		case $(ask "Choice [1]: ") in
		2) provider=openai-compatible ;;
		*) provider=anthropic ;;
		esac
		plugin "$provider" || die "could not install plugin $provider"
		while :; do
			model=$(ask "Model ID (from the list above): ")
			/usr/local/bin/agent provider describe | grep -qF "\"id\": \"$model\"" && break
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
# A command that runs the machine out of memory is killed by the kernel and
# its agent sees the failure; the rest of the service keeps running.
OOMPolicy=continue

[Install]
WantedBy=multi-user.target
UNIT

	systemctl daemon-reload
	# A daemon that failed to start before this run may have hit systemd's
	# start limit.
	systemctl reset-failed agent 2>/dev/null || true
	systemctl enable --now agent
	ROLLBACK=no
	# The installed version, for the next upgrade. The agent is running by
	# now, so a failure here only warns.
	printf '%s\n' "$VERSION" >/var/lib/agent/version ||
		say "warning: could not record v$VERSION in /var/lib/agent/version; the agent is running v$VERSION"

	if [ "$upgrade" = yes ]; then
		say "Upgraded to v$VERSION and started."
	else
		if ! printf '{"provider":"%s","model":"%s"}\n' "$provider" "$model" | /usr/local/bin/agent new main -; then
			retry="printf '{\"provider\":\"$provider\",\"model\":\"$model\"}\\n' | sudo agent new main -"
			if [ -e /etc/agent/agents/main ]; then retry="sudo agent remove main, then $retry"; fi
			die "the harness is running, but creating the main agent failed (see above); once the cause is fixed, run: $retry"
		fi
		say "Installed v$VERSION. Next:"
		say "  sudo agent send main \"Hello. Read your README, then tell me how you plan to keep notes.\""
		say "  sudo agent log main"
	fi
}

main "$@"
