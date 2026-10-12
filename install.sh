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
VERSION=0.3.1
SHA_agent_linux_amd64=f4a76e4f900747dd0d9b9ffc5ac66af2dacb9099a1380ebfd7c1630b64e3e576
SHA_agent_linux_arm64=49c42b461bc393706524153c2d37a4e3060bc5874368290e5d5d758d7553fb07
# END CHECKSUMS

ROLLBACK=no
MIGRATED=no
MARKED=no # this run wrote $MIGRATING
# Marks an upgrade from 0.3 whose migration has not finished, and holds the
# path of its backup. install.sh writes it before replacing the binary and
# removes it once `agent _migrate` succeeds; the daemon refuses to start
# while it exists (layout.MigratingFile).
MIGRATING=/var/lib/agent/migrating
# The pool user `agent _migrate` is renaming the 0.3 main agent's user to,
# while it works (layout.MigratingUserFile). It owns this file.
MIGRATING_USER=/var/lib/agent/migrating-user

# finish cleans up, and restores the previous binary and service if an
# upgrade failed after it was stopped. Once the migration may have changed
# anything, the previous release must not run: the agent stays stopped on
# this one until a rerun completes.
finish() {
	status=$?
	if [ "$status" != 0 ] && [ "$ROLLBACK" = yes ] && [ -x /usr/local/bin/agent.prev ]; then
		# Nothing was migrated: the previous release starts without the marker.
		if [ "$MARKED" = yes ]; then rm -f "$MIGRATING"; fi
		mv /usr/local/bin/agent.prev /usr/local/bin/agent
		systemctl start agent 2>/dev/null || true
		printf 'install: failed; restored the previous version\n' >&2
	elif [ "$status" != 0 ] && [ "$MIGRATED" = yes ]; then
		printf 'install: failed (status %s) once the migration had begun; the agent stays stopped on v%s. Fix the cause and rerun install.sh.\n' "$status" "$VERSION" >&2
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
	for tool in curl sha256sum tar getent useradd usermod groupmod gpasswd pkill pgrep timeout systemctl loginctl crontab; do
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

	say "Installing agent harness v$VERSION ($ARCH)"
	tmp=$(mktemp -d)
	trap finish EXIT
	download "agent-linux-$ARCH" "$tmp/agent"
	eval "want=\$SHA_agent_linux_$ARCH"
	got=$(sha256sum "$tmp/agent" | cut -d' ' -f1)
	[ "$got" = "$want" ] || die "checksum mismatch for agent-linux-$ARCH: got $got, want $want"

	# A 0.3 installation runs its main agent as the user agent, which 0.4
	# does not have: that user, or a migration an earlier run left
	# unfinished, means this upgrade migrates. Nothing has changed yet, so
	# every check up to stopping the service leaves the machine as it was.
	legacy=no
	if id agent >/dev/null 2>&1; then legacy=yes; fi
	migrate=no
	if [ "$legacy" = yes ] || [ -e "$MIGRATING" ] || [ -e "$MIGRATING_USER" ]; then migrate=yes; fi
	upgrade=$migrate
	if [ -e /etc/agent/agents/main ]; then upgrade=yes; fi

	if [ -e "$MIGRATING" ]; then
		case $(cat "$MIGRATING") in
		*/agent-subagents-*) die "$MIGRATING marks an unfinished migration to v0.3.0; finish it with the v0.3.1 install.sh, then run this one" ;;
		esac
	elif [ "$legacy" = yes ]; then
		# The migration starts from v0.3.0 or later.
		case $(cat /var/lib/agent/version 2>/dev/null || true) in
		0.[0-2].*) die "upgrade to v0.3.1 first; this release migrates installations from v0.3.0 or later" ;;
		esac
		for dir in /home/agent/agents/*/inbox; do
			if [ -d "$dir" ] && [ ! -L "${dir%/inbox}/home" ]; then
				die "upgrade to v0.3.1 first; $dir belongs to a subagent from before v0.3.0"
			fi
		done
	fi

	# The migration stops the agent user's services, and with them its
	# transient systemd-run jobs, which do not come back. Declining changes
	# nothing.
	if [ "$legacy" = yes ]; then
		uid=$(id -u agent)
		jobs=
		for unit in "/run/user/$uid/systemd/transient/"*.service; do
			[ -f "$unit" ] || continue
			name=${unit##*/}
			state=$(systemctl --user -M agent@ is-active "$name" 2>/dev/null) || true
			case $state in
			inactive | failed) ;;
			'') jobs="$jobs  $name (state unknown)
" ;;
			*) jobs="$jobs  $name ($state)
" ;;
			esac
		done
		say "The upgrade moves the agent to a pool user. Its user services stop and restart during the move; whatever they serve is down meanwhile."
		if [ -n "$jobs" ]; then
			say "Running transient jobs (systemd-run) of user agent:"
			printf '%s' "$jobs"
			if (: </dev/tty) 2>/dev/null; then
				case $(ask "These jobs will be stopped and not restarted. Continue? [y/N] ") in
				y | Y | yes | Yes | YES) ;;
				*) say "Nothing changed."; exit 0 ;;
				esac
			else
				say "warning: no terminal to confirm; these jobs will be stopped and not restarted."
			fi
		fi
	fi

	# The release installed now is the one to restore if this run fails,
	# unless an earlier run began migrating (its marker, or a recorded target
	# with user agent gone): the binary may be this release already, and the
	# previous one must not run again, whatever fails now.
	if [ -e "$MIGRATING" ] || { [ -e "$MIGRATING_USER" ] && [ "$legacy" = no ]; }; then
		MIGRATED=yes
	elif [ -x /usr/local/bin/agent ]; then
		cp -p /usr/local/bin/agent /usr/local/bin/agent.prev
		ROLLBACK=yes
	fi
	active=no
	if systemctl is-active --quiet agent 2>/dev/null; then
		active=yes
		say "Stopping the agent; active runs finish first (up to 15 minutes)..."
	fi
	# Whenever the unit exists, even inactive or between restarts: a unit
	# restarting on the new binary would race the migration below.
	if [ "$active" = yes ] || [ -f /etc/systemd/system/agent.service ]; then
		systemctl stop agent
	fi

	# Before the new binary is in place, the marker keeps it from starting
	# on the 0.3 layout until the migration is done. The first run backs up
	# the untouched state, root-only (it holds credentials and password
	# hashes); the marker keeps the backup's path for every rerun. The home
	# is not copied: the migration moves it but changes none of its files.
	if [ "$migrate" = yes ] && [ ! -e "$MIGRATING" ]; then
		mkdir -p /var/backups /var/lib/agent
		backup=/var/backups/agent-0.4-$(date -u +%Y%m%dT%H%M%SZ).tgz
		say "Backing up /etc/agent, /var/lib/agent and user agent's records to $backup..."
		mkdir "$tmp/accounts"
		for db in passwd group shadow gshadow; do
			getent "$db" agent >"$tmp/accounts/$db" || true
		done
		set --
		for path in etc/agent var/lib/agent var/spool/cron/crontabs/agent var/lib/systemd/linger/agent; do
			if [ -e "/$path" ]; then set -- "$@" "$path"; fi
		done
		(umask 077; tar -czf "$backup" -C / "$@" -C "$tmp" accounts) || {
			rm -f "$backup"
			die "could not write the backup $backup"
		}
		chmod 600 "$backup"
		printf '%s\n' "$backup" >"$MIGRATING"
		MARKED=yes
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

	# Migrate a 0.3 installation (agent _migrate): its main agent's user
	# becomes the first pool user that does not exist yet, keeping its uid,
	# files and services. Status 1: nothing was renamed, so the previous
	# release can run again. Any other failure: it stopped part way, so the
	# agent stays stopped on this release and a rerun finishes it. It runs
	# from its installed path, which it starts helpers from as other users.
	if [ "$migrate" = yes ]; then
		backup=$(cat "$MIGRATING")
		code=0
		/usr/local/bin/agent _migrate || code=$?
		case $code in
		0)
			rm -f "$MIGRATING"
			# From here a failure keeps this release: the previous one cannot
			# run the migrated tree.
			ROLLBACK=no
			MIGRATED=yes
			;;
		1)
			# Nothing was renamed, by this run or an earlier one.
			[ "$MIGRATED" = yes ] || rm -f "$MIGRATING"
			die "could not migrate the agent, and nothing was renamed; the backup from before the upgrade is $backup"
			;;
		*)
			ROLLBACK=no
			MIGRATED=yes
			die "the migration stopped part way; the backup from before it is $backup"
			;;
		esac
	fi

	# Pool users agent-1 to agent-<max_agents>, created only now: the
	# migration renames the 0.3 main agent's user to the first one missing.
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
	# A daemon refusing to start during an unfinished migration may have hit
	# systemd's start limit.
	systemctl reset-failed agent 2>/dev/null || true
	systemctl enable --now agent
	ROLLBACK=no
	# The installed version, for the next upgrade. Release notes no longer
	# depend on it: they ride along with each agent's next wake, and a new
	# agent starts current.
	printf '%s\n' "$VERSION" >/var/lib/agent/version

	if [ "$upgrade" = yes ]; then
		say "Upgraded to v$VERSION and started."
	else
		/usr/local/bin/agent new main --provider "$provider" --model "$model" ||
			die "the harness is running, but creating the main agent failed (see above); once the cause is fixed, run: sudo agent new main --provider $provider --model $model"
		say "Installed v$VERSION. Next:"
		say "  sudo agent send main \"Hello. Read your README, then tell me how you plan to keep notes.\""
		say "  sudo agent log main"
	fi
}

main "$@"
