#!/bin/sh
# Installs qisqa from its releases on GitHub:
#
#   curl -fsSL https://raw.githubusercontent.com/professor93/qisqa/main/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/professor93/qisqa/main/install.sh | sh -s -- --user
#
# On Linux, as root or with sudo, qisqa goes into /opt/qisqa for the system
# user qisqa, with a systemd or OpenRC service; otherwise, and always on
# macOS, into ~/qisqa for the current user. docs/running.md says more under
# "The installer". The script asks no question; every step is one line.
#
# It is plain POSIX sh: it runs under dash, busybox ash and macOS sh.
# QISQA_RELEASES_URL replaces the releases address, for tests only.
#
# The whole script is one group, { ... }, which the shell reads to its end
# before it runs any of it: a download cut short runs nothing.
{
set -eu

# useradd, rc-service and the like are in the sbin folders, which are not in
# every user's PATH, also when sudo would run them.
PATH=$PATH:/usr/local/sbin:/usr/sbin:/sbin
export PATH

releases=${QISQA_RELEASES_URL:-https://github.com/professor93/qisqa/releases}
pages=https://github.com/professor93/qisqa/blob/main/docs

say() { printf '%s\n' "$*"; }
die() {
	printf 'install.sh: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'USAGE'
Usage: install.sh [--user] [--version VERSION] [--dir PATH]

Installs qisqa from its releases on GitHub.

  --user             install for the current user only, with no service
  --version VERSION  the release to install, such as 0.1.0 (default: the latest)
  --dir PATH         the folder to install into (default: /opt/qisqa, or
                     ~/qisqa for the current user)
  --help             show this text

On Linux, as root or with sudo, qisqa is installed into /opt/qisqa for the
system user qisqa, with a systemd or OpenRC service. Otherwise, and always
on macOS, it is installed for the current user, and you start it yourself.
Run again, it upgrades: a newer release replaces the binary, the config and
the data stay, and the service is started again; the same release changes
nothing. Through a pipe, give the options after "sh -s --".
USAGE
}

# as_root runs a command as root: directly, or through sudo.
as_root() {
	if [ -n "$sudo" ]; then
		sudo "$@"
	else
		"$@"
	fi
}

have() { command -v "$1" >/dev/null 2>&1; }

cleanup() {
	if [ -n "${tmp:-}" ] && [ -d "$tmp" ]; then
		rm -rf "$tmp"
	fi
}

# The systemd unit of docs/running.md; TestInstallerServiceFilesMatchTheDocs
# keeps the two the same.
systemd_unit() {
	sed "s|/opt/qisqa|$dir|g" <<'UNIT'
[Unit]
Description=qisqa URL shortener
After=network.target
StartLimitIntervalSec=0

[Service]
ExecStart=/opt/qisqa/qisqa -config /opt/qisqa/config.yaml
WorkingDirectory=/opt/qisqa
User=qisqa
Restart=always
RestartSec=1
TimeoutStopSec=15
LimitNOFILE=65536
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
UNIT
}

# The OpenRC script of docs/running.md, kept the same by the same test.
openrc_script() {
	sed "s|/opt/qisqa|$dir|g" <<'INIT'
#!/sbin/openrc-run

description="qisqa URL shortener"
supervisor=supervise-daemon
command=/opt/qisqa/qisqa
command_args="-config /opt/qisqa/config.yaml"
command_user=qisqa:qisqa
directory=/opt/qisqa
error_log=/var/log/qisqa.log
respawn_delay=1
respawn_max=0
retry=TERM/15/KILL/5

depend() {
	after net
}

start_pre() {
	checkpath --file --owner "$command_user" --mode 0600 "$error_log"
}
INIT
}

config_file() {
	cat <<'CONFIG'
# Every key is explained in docs/configuration.md and config.example.yaml.
listen: "127.0.0.1:8080"
data_dir: ./data
CONFIG
}

parse_options() {
	user_only=
	version=latest
	dir=
	while [ $# -gt 0 ]; do
		case $1 in
		--user) user_only=yes ;;
		--version)
			[ $# -ge 2 ] || die "--version needs a version, such as --version 0.1.0"
			version=$2
			shift
			;;
		--version=*) version=${1#--version=} ;;
		--dir)
			[ $# -ge 2 ] || die "--dir needs a folder, such as --dir /opt/qisqa"
			dir=$2
			shift
			;;
		--dir=*) dir=${1#--dir=} ;;
		--help | -h)
			usage
			exit 0
			;;
		*) die "unknown option $1; see install.sh --help" ;;
		esac
		shift
	done
	version=${version#v}
	case $version in
	latest) ;;
	'' | [!0-9]* | *[!0-9A-Za-z.+-]*) die "--version: \"$version\" is not a version such as 0.1.0" ;;
	esac
}

find_system() {
	case $(uname -s) in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) die "there is no qisqa for $(uname -s); see $releases" ;;
	esac
	case $(uname -m) in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) die "there is no qisqa for the processor $(uname -m); see $releases" ;;
	esac
	say "System: $os $arch."
}

# find_rights chooses a system install, as root, or a user install.
find_rights() {
	sudo=
	if [ -n "$user_only" ]; then
		kind=user why="--user was given"
	elif [ "$os" = darwin ]; then
		kind=user why="on macOS qisqa is installed for the current user"
	elif [ "$(id -u)" = 0 ]; then
		kind=system why="the script runs as root"
	elif have sudo && sudo -n true </dev/null 2>/dev/null; then
		kind=system why="sudo may be used" sudo=sudo
	elif have sudo && (: </dev/tty) 2>/dev/null; then
		kind=system why="sudo can ask for a password on this terminal" sudo=sudo
		say "Asking sudo for root, for a system install (or run with --user)."
		sudo -v || die "sudo refused; run with --user to install into ~/qisqa"
	else
		kind=user why="the script does not run as root and cannot use sudo"
	fi

	if [ "$kind" = system ]; then
		dir=${dir:-/opt/qisqa}
	else
		dir=${dir:-${HOME:?HOME is not set}/qisqa}
	fi
	case $dir in
	/*) ;;
	*) dir=$(pwd)/$dir ;;
	esac
	while [ "${dir%/}" != "$dir" ] && [ "$dir" != / ]; do dir=${dir%/}; done
	[ "$dir" != / ] || die "--dir: / cannot be the folder of qisqa"
	if [ "$kind" = system ]; then
		# The path goes into the service file.
		case $dir in
		*[!A-Za-z0-9._/-]*) die "--dir: for a system install the path may hold only letters, digits and . _ - /" ;;
		esac
		# The unit's ProtectSystem=full makes /usr, /boot, /efi and /etc
		# read-only for qisqa, and its ProtectHome=true hides /home, /root and
		# /run/user from it; the rest of /run is emptied at every boot.
		if [ -d /run/systemd/system ]; then
			case $dir/ in
			/usr/* | /boot/* | /efi/* | /etc/*) die "--dir: the systemd unit (ProtectSystem=full) makes $dir read-only for qisqa; choose another folder, such as /srv/qisqa" ;;
			/home/* | /root/* | /run/user/*) die "--dir: the systemd unit (ProtectHome=true) hides $dir from qisqa; choose another folder, or run with --user for one in your home" ;;
			/run/*) die "--dir: $dir is emptied at every boot; choose another folder, such as /srv/qisqa" ;;
			esac
		fi
		say "Install: a system install into $dir, with root rights, since $why."
	else
		say "Install: a user install into $dir, since $why."
	fi
}

# check_folder refuses a folder that is there, holds other files and no
# qisqa: the script would put its own among them, and over those of the same
# name. A config.yaml, data and logs made before the first run are qisqa's
# own, and are kept; a .qisqa.new is what a run that stopped at the binary
# leaves.
check_folder() {
	if as_root test -d "$dir" && ! as_root test -f "$dir/qisqa" &&
		as_root ls -A "$dir" </dev/null | grep -qvE '^(\.qisqa\.new|config\.yaml|data|logs)$'; then
		die "$dir is there and holds other files but no qisqa; the script does not install into it"
	fi
}

# find_tools finds a downloader, tar and a SHA-256 tool, and in a system
# install installs the ones that are missing.
find_tools() {
	missing=
	if have curl; then
		get=curl
	elif have wget; then
		get=wget
	else
		get='' missing="curl"
	fi
	have tar || missing="$missing tar"
	if have sha256sum; then
		sum=sha256sum
	elif have shasum; then
		sum=shasum
	else
		sum='' missing="$missing sha256sum"
	fi
	if [ -z "$missing" ]; then
		say "Tools: $get, tar and $sum are there."
		return
	fi
	missing=${missing# }
	[ "$kind" = system ] || die "missing: $missing; install it, or run the script as root to have it installed"

	packages=
	for tool in $missing; do
		case $tool in
		curl) packages="$packages curl ca-certificates" ;;
		tar) packages="$packages tar" ;;
		sha256sum) packages="$packages coreutils" ;;
		esac
	done
	# Word splitting is wanted: one argument per package.
	# shellcheck disable=SC2086
	set -- $packages
	if have apt-get; then
		say "Tools: installing $* with apt-get."
		as_root env DEBIAN_FRONTEND=noninteractive apt-get update -qq </dev/null >/dev/null ||
			die "apt-get update failed"
		as_root env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends "$@" </dev/null >/dev/null ||
			die "apt-get could not install $*"
	elif have dnf; then
		say "Tools: installing $* with dnf."
		as_root dnf install -y -q "$@" </dev/null >/dev/null || die "dnf could not install $*"
	elif have yum; then
		say "Tools: installing $* with yum."
		as_root yum install -y -q "$@" </dev/null >/dev/null || die "yum could not install $*"
	elif have apk; then
		say "Tools: installing $* with apk."
		as_root apk add --no-cache -q "$@" </dev/null >/dev/null || die "apk could not install $*"
	elif have zypper; then
		say "Tools: installing $* with zypper."
		as_root zypper --non-interactive --quiet install "$@" </dev/null >/dev/null || die "zypper could not install $*"
	elif have pacman; then
		say "Tools: installing $* with pacman."
		as_root pacman -S --needed --noconfirm "$@" </dev/null >/dev/null || die "pacman could not install $*"
	else
		die "missing: $missing, and no package manager was found to install it"
	fi
	if have curl; then get=curl; elif have wget; then get=wget; fi
	if have sha256sum; then sum=sha256sum; elif have shasum; then sum=shasum; fi
	if [ -z "$get" ] || ! have tar || [ -z "$sum" ]; then
		die "still missing after the install: $missing"
	fi
	say "Tools: $get, tar and $sum are there."
}

# fetch downloads an https:// address into a file. With curl a redirect must
# lead to https:// too; wget, used only where curl is missing, has no such
# option, so with it only the first address is checked.
fetch() {
	case $1 in
	https://*) ;;
	*) die "refusing to download $1: only https:// addresses are used" ;;
	esac
	if [ "$get" = curl ]; then
		curl --proto '=https' --proto-redir '=https' -fsSL --retry 2 -o "$2" "$1" </dev/null
	else
		wget -q -O "$2" "$1" </dev/null
	fi
}

sha256() {
	if [ "$sum" = sha256sum ]; then
		sha256sum "$1"
	else
		shasum -a 256 "$1"
	fi | cut -d ' ' -f 1
}

# find_release fetches the release's checksums.txt and finds in it the
# archive for this system: its name, its SHA-256 and, from the name, the
# version of the release.
find_release() {
	case $releases in
	https://*) ;;
	*) die "QISQA_RELEASES_URL must be an https:// address" ;;
	esac
	if [ "$version" = latest ]; then
		base=$releases/latest/download
	else
		base=$releases/download/v$version
	fi
	say "Download: $base/checksums.txt."
	if [ "$version" = latest ]; then
		asked="the latest release"
	else
		asked="a release $version"
	fi
	fetch "$base/checksums.txt" "$tmp/checksums.txt" ||
		die "could not download $base/checksums.txt; is there $asked? See $releases"
	line=$(grep -E "^[0-9a-f]{64}  qisqa_[0-9A-Za-z.+-]+_${os}_${arch}\.tar\.gz\$" "$tmp/checksums.txt" || true)
	[ -n "$line" ] || die "the release has no archive for $os $arch; see $releases"
	[ "$(printf '%s\n' "$line" | wc -l)" -eq 1 ] || die "checksums.txt names more than one archive for $os $arch"
	want=${line%% *}
	archive=${line##* }
	release=$(release_of "$archive")
}

# release_of prints the version in the name of an archive for this system,
# qisqa_VERSION_OS_ARCH.tar.gz.
release_of() {
	name=${1#qisqa_}
	printf '%s\n' "${name%"_${os}_${arch}.tar.gz"}"
}

# find_installed sets current to the version that the qisqa in the folder
# says it is, or to nothing: when there is none, when it does not run, and
# in a system install when someone other than root could have changed it,
# since root would run it.
find_installed() {
	current=
	as_root test -f "$dir/qisqa" || return 0
	if [ "$kind" = system ]; then
		[ "$(as_root stat -L -c %u "$dir/qisqa")" = 0 ] || return 0
		case $(as_root stat -L -c %a "$dir/qisqa") in
		*[2367]? | *[2367]) return 0 ;;
		esac
	fi
	current=$(as_root "$dir/qisqa" -version </dev/null 2>/dev/null | sed -n 's/^qisqa //p') || current=
}

# choose_change sets change: install on a first install, none when the
# release is the version that is there, replace for any other version.
choose_change() {
	if [ -z "$current" ]; then
		change=install
	elif [ "$current" = "$release" ]; then
		change=none
		say "Version: qisqa $current is in $dir, and it is the release asked for: the binary and the documents stay."
	else
		change=replace
		say "Version: qisqa $current is in $dir; qisqa $release takes its place."
	fi
}

# download fetches the archive that find_release found, checks its SHA-256
# and unpacks it into $tmp/release.
download() {
	say "Download: $base/$archive."
	fetch "$base/$archive" "$tmp/$archive" || die "could not download $base/$archive"
	got=$(sha256 "$tmp/$archive")
	[ "$got" = "$want" ] ||
		die "the SHA-256 of $archive is $got, but checksums.txt says $want; nothing was installed"
	say "Checked: the SHA-256 of $archive is the one in checksums.txt."

	mkdir "$tmp/release"
	tar -xzf "$tmp/$archive" -C "$tmp/release" || die "could not unpack $archive"
	if [ ! -f "$tmp/release/qisqa" ] || [ -L "$tmp/release/qisqa" ]; then
		die "$archive holds no qisqa binary"
	fi
}

# binary puts the new binary into the folder under another name, runs it
# there (the temporary folder may not allow it), and only then renames it
# over the old one: a running binary cannot be written to, and one that does
# not run replaces nothing. A binary of another version that it replaces is
# kept beside it as qisqa.old, to go back to.
binary() {
	if [ "$change" = none ]; then
		installed="qisqa $current"
		say "Binary: keeping $dir/qisqa, $installed."
		return 0
	fi
	put -m 755 "$tmp/release/qisqa" "$dir/.qisqa.new"
	installed=$(as_root "$dir/.qisqa.new" -version </dev/null) ||
		die "the qisqa of $archive does not run on this system; nothing was replaced"
	if [ "$change" = replace ]; then
		as_root cp -pf "$dir/qisqa" "$dir/qisqa.old"
		as_root mv -f "$dir/.qisqa.new" "$dir/qisqa"
		say "Binary: $dir/qisqa, $installed; the one it replaced is $dir/qisqa.old."
	else
		as_root mv -f "$dir/.qisqa.new" "$dir/qisqa"
		say "Binary: $dir/qisqa, $installed."
	fi
}

# fail_start ends the script when qisqa did not come up, with the message
# given. After an upgrade it first puts the binary that was replaced back
# and starts the service again: a release that does not start here costs a
# few seconds, not the service.
fail_start() {
	if [ "$change" = replace ] && as_root test -f "$dir/qisqa.old"; then
		as_root mv -f "$dir/qisqa.old" "$dir/qisqa"
		case $service in
		systemd) as_root systemctl restart qisqa </dev/null || true ;;
		openrc) as_root rc-service qisqa restart </dev/null >/dev/null 2>&1 || true ;;
		esac
		die "$1; qisqa $current was put back${service:+ and started again}"
	fi
	die "$1"
}

# put installs a file or, with -d, a folder into the install folder: as
# root and owned by root in a system install, as the user otherwise.
put() {
	if [ "$kind" = system ]; then
		as_root install -o 0 -g 0 "$@"
	else
		install "$@"
	fi
}

# documents copies README.md, LICENSE, config.example.yaml and docs/ from the
# archive into the folder, those that the archive has; a link is no document.
documents() {
	[ "$change" != none ] || return 0
	for name in README.md LICENSE config.example.yaml; do
		if [ -f "$tmp/release/$name" ] && [ ! -L "$tmp/release/$name" ]; then
			put -m 644 "$tmp/release/$name" "$dir/$name"
		fi
	done
	if [ -d "$tmp/release/docs" ] && [ ! -L "$tmp/release/docs" ]; then
		put -d -m 755 "$dir/docs"
		for page in "$tmp/release/docs"/*.md; do
			if [ -f "$page" ] && [ ! -L "$page" ]; then
				put -m 644 "$page" "$dir/docs/${page##*/}"
			fi
		done
	fi
	say "Documents: those of the archive are in $dir."
}

# check_owner refuses, in a system install, a folder that is there and that
# someone other than root may write to. Before anything is made or run:
# qisqa's own user, or anyone but root, must not be able to put anything
# into a folder that root then writes through and runs a binary from.
check_owner() {
	if [ -d "$dir" ]; then
		[ "$(stat -L -c %u "$dir")" = 0 ] || die "$dir is there but not owned by root; the script does not install into it"
		mode=$(stat -L -c %a "$dir")
		case $mode in
		*[2367]? | *[2367]) die "$dir is there and writable by more than root (mode $mode); the script does not install into it" ;;
		esac
	fi
}

install_system() {
	if id qisqa >/dev/null 2>&1; then
		say "User: qisqa is there."
	else
		for nologin in /usr/sbin/nologin /sbin/nologin /bin/false; do
			[ -x "$nologin" ] && break
		done
		if have useradd; then
			if grep -q '^qisqa:' /etc/group; then group_option="--gid qisqa"; else group_option=--user-group; fi
			# shellcheck disable=SC2086
			as_root useradd --system $group_option --no-create-home --home-dir "$dir" --shell "$nologin" qisqa
		elif have adduser; then
			grep -q '^qisqa:' /etc/group || as_root addgroup -S qisqa
			as_root adduser -S -D -H -h "$dir" -s "$nologin" -G qisqa qisqa
		else
			die "cannot make the user qisqa: neither useradd nor adduser is there"
		fi
		say "User: made the system user qisqa, without a login shell."
	fi
	group=$(id -gn qisqa)

	if [ -d "$dir" ]; then
		say "Folder: $dir is there."
	else
		as_root install -d -o 0 -g 0 -m 755 "$dir"
		say "Folder: made $dir, owned by root, mode 755."
	fi

	binary

	wrote_config=
	if as_root test -e "$dir/config.yaml" || as_root test -L "$dir/config.yaml"; then
		say "Config: keeping $dir/config.yaml."
	else
		wrote_config=yes
		config_file >"$tmp/config.yaml"
		# set -C: should the test above have been wrong (a sudo that failed),
		# the shell does not write over the config that is there. Its $1 is
		# the inner shell's.
		# shellcheck disable=SC2016
		as_root sh -c 'set -C; cat >"$1"' sh "$dir/config.yaml" <"$tmp/config.yaml" ||
			die "could not write $dir/config.yaml, or it is there; nothing was written over it"
		as_root chown 0:0 "$dir/config.yaml"
		as_root chmod 644 "$dir/config.yaml"
		say "Config: wrote $dir/config.yaml, which listens on 127.0.0.1:8080."
	fi
	if as_root test -e "$dir/data" || as_root test -L "$dir/data"; then
		say "Data: keeping the data folder $dir/data."
	else
		as_root install -d -o qisqa -g "$group" -m 700 "$dir/data"
		say "Data: made the data folder $dir/data, for the user qisqa alone."
	fi
	if as_root test -e "$dir/logs" || as_root test -L "$dir/logs"; then
		say "Logs: keeping the log folder $dir/logs."
	else
		as_root install -d -o qisqa -g "$group" -m 700 "$dir/logs"
		say "Logs: made the log folder $dir/logs, for the user qisqa alone."
	fi

	# The service is started again only when something of it changed: the
	# binary, the config, its own file, or when it does not run.
	service='' restart=''
	if [ "$change" != none ] || [ -n "$wrote_config" ]; then
		restart=yes
	fi
	if [ -d /run/systemd/system ]; then
		service=systemd output="journalctl -u qisqa"
		systemd_unit >"$tmp/qisqa.service"
		if [ "$(as_root cat /etc/systemd/system/qisqa.service 2>/dev/null)" != "$(cat "$tmp/qisqa.service")" ]; then
			as_root install -o 0 -g 0 -m 644 "$tmp/qisqa.service" /etc/systemd/system/qisqa.service
			as_root systemctl daemon-reload </dev/null || die "systemctl daemon-reload failed"
			restart=yes
		fi
		as_root systemctl enable --quiet qisqa </dev/null || die "systemctl enable qisqa failed"
		as_root systemctl is-active --quiet qisqa </dev/null || restart=yes
		if [ -n "$restart" ]; then
			as_root systemctl restart qisqa </dev/null || fail_start "qisqa did not start; its output: $output"
			say "Service: /etc/systemd/system/qisqa.service, enabled and (re)started."
		fi
	elif have rc-service; then
		service=openrc output=/var/log/qisqa.log
		openrc_script >"$tmp/qisqa.init"
		if [ "$(as_root cat /etc/init.d/qisqa 2>/dev/null)" != "$(cat "$tmp/qisqa.init")" ]; then
			as_root install -o 0 -g 0 -m 755 "$tmp/qisqa.init" /etc/init.d/qisqa
			restart=yes
		fi
		as_root rc-update add qisqa default </dev/null >/dev/null || die "rc-update add qisqa default failed"
		as_root rc-service qisqa status </dev/null >/dev/null 2>&1 || restart=yes
		if [ -n "$restart" ]; then
			# OpenRC does not say which process answers: with the service
			# stopped, nothing may answer on qisqa's address, or a qisqa
			# started by hand would pass for the service.
			as_root rc-service qisqa stop </dev/null >/dev/null 2>&1 || true
			health_url
			if answers; then
				fail_start "something else already answers on $url; stop it first (a qisqa started by hand?), then run the script again"
			fi
			as_root rc-service qisqa start </dev/null >/dev/null || fail_start "qisqa did not start; its output: $output"
			say "Service: /etc/init.d/qisqa, in the default runlevel and (re)started."
		fi
	else
		say "Service: neither systemd nor OpenRC runs here, so there is no service; start qisqa by hand."
	fi
	if [ -n "$service" ]; then
		if [ -n "$restart" ]; then
			wait_for_health
		else
			say "Service: qisqa runs and nothing of it changed; it was not started again."
		fi
	fi
	# Last, so that a release that did not start leaves the old documents
	# beside the old binary.
	documents
}

# health_url sets url to /healthz on the address that the config gives.
health_url() {
	address=$(as_root sed -n 's/^listen:[[:space:]]*//p' "$dir/config.yaml" |
		sed 's/[[:space:]]#.*//' | tr -d "\"' " | head -n 1)
	address=${address:-:8080}
	case $address in
	:*) address=127.0.0.1$address ;;
	0.0.0.0:*) address=127.0.0.1:${address#0.0.0.0:} ;;
	"[::]:"*) address="[::1]:${address#"[::]:"}" ;;
	esac
	url=http://$address/healthz
}

# wait_for_health waits up to 10 seconds for qisqa's answer on /healthz.
wait_for_health() {
	health_url
	tries=0 pid='' since=0
	while :; do
		if answers; then
			[ "$service" = systemd ] || break
			# A server that holds the port already answers too, while qisqa
			# fails to start and systemd starts it again every second: the
			# service must also be active, with the same running process
			# through two seconds of answers. MainPID is 0 between two starts.
			now=$(as_root systemctl show -p MainPID qisqa </dev/null)
			case $now in
			MainPID=[1-9]*) ;;
			*) now='' ;;
			esac
			if [ -z "$now" ] || [ "$now" != "$pid" ]; then
				pid=$now since=$tries
			elif [ $((tries - since)) -ge 2 ] &&
				as_root systemctl is-active --quiet qisqa </dev/null; then
				break
			fi
		else
			pid=''
		fi
		tries=$((tries + 1))
		[ "$tries" -lt 10 ] || fail_start "qisqa does not answer on $url after 10 seconds; its output: $output"
		sleep 1
	done
	say "Health: $url answers."
}

# answers tells whether $url gives qisqa's answer, {"status":"ok"}.
answers() {
	if [ "$get" = curl ]; then
		answer=$(curl -fsS --max-time 1 "$url" </dev/null 2>/dev/null) || return 1
	else
		answer=$(wget -q -T 1 -O - "$url" </dev/null 2>/dev/null) || return 1
	fi
	[ "$answer" = '{"status":"ok"}' ]
}

install_user() {
	if [ -d "$dir" ]; then
		say "Folder: $dir is there."
	else
		mkdir -p "$dir"
		chmod 700 "$dir"
		say "Folder: made $dir, mode 700."
	fi
	binary
	documents
	if [ -e "$dir/config.yaml" ] || [ -L "$dir/config.yaml" ]; then
		say "Config: keeping $dir/config.yaml."
	else
		(umask 077 && config_file >"$dir/config.yaml")
		say "Config: wrote $dir/config.yaml, which listens on 127.0.0.1:8080."
	fi
}

# quote prints a word as the shell reads it back.
quote() {
	case $1 in
	*[!A-Za-z0-9._/:=@%+-]*) printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")" ;;
	*) printf '%s' "$1" ;;
	esac
}

finish() {
	if [ "$kind" = system ]; then
		admin="$dir/qisqa -data $dir/data"
		if as_root test -f "$dir/docs/nginx.md"; then nginx=$dir/docs/nginx.md; else nginx=$pages/nginx.md; fi
	else
		if [ -f "$dir/docs/nginx.md" ]; then nginx=$dir/docs/nginx.md; else nginx=$pages/nginx.md; fi
	fi
	say ""
	case $change in
	none) say "qisqa $current was in $dir already; nothing was replaced." ;;
	replace) say "Installed $installed in $dir; it replaced qisqa $current, kept as $dir/qisqa.old." ;;
	*) say "Installed $installed in $dir." ;;
	esac
	say "The config is $dir/config.yaml."
	if [ "$change" != install ]; then
		# Not a first install: the domain and the account are there already.
		if [ "$kind" = user ] || [ -z "$service" ]; then
			[ "$change" = none ] || say "A qisqa that is running still runs the old binary: stop it and start it again."
		else
			say "qisqa runs as the $service service qisqa; its output: $output."
		fi
		return 0
	fi
	if [ "$kind" = user ]; then
		# From the folder, as in the Quick start: the admin socket's path,
		# data/qisqa.sock, stays short whatever the folder.
		say "Start qisqa, and keep it running:"
		say "  cd $(quote "$dir") && ./qisqa -config config.yaml"
		say "Then, in a second terminal, add a domain and an account:"
		say "  cd $(quote "$dir")"
		say "  ./qisqa -config config.yaml domain add short.example"
		say "  ./qisqa -config config.yaml account add me --domain short.example"
	else
		if [ -z "$service" ]; then
			say "Start qisqa, and keep it running:"
			if have sudo; then
				say "  sudo -u qisqa sh -c 'cd $dir && exec ./qisqa -config config.yaml'"
			else
				say "  su -s /bin/sh qisqa -c 'cd $dir && exec ./qisqa -config config.yaml'"
			fi
		else
			say "qisqa runs as the $service service qisqa; its output: $output."
		fi
		say "Add a domain and an account:"
		if have sudo; then
			say "  sudo -u qisqa $admin domain add short.example"
			say "  sudo -u qisqa $admin account add me --domain short.example"
		else
			say "  su -s /bin/sh qisqa -c '$admin domain add short.example'"
			say "  su -s /bin/sh qisqa -c '$admin account add me --domain short.example'"
		fi
	fi
	say "For HTTPS, put a proxy in front of qisqa: $nginx"
}

main() {
	parse_options "$@"
	find_system
	find_rights
	check_folder
	if [ "$kind" = system ]; then
		check_owner
	fi
	find_tools
	tmp=
	trap cleanup EXIT
	trap 'exit 1' HUP INT TERM
	tmp=$(mktemp -d) || die "could not make a temporary folder"
	find_release
	find_installed
	choose_change
	if [ "$change" != none ]; then
		download
	fi
	if [ "$kind" = system ]; then
		install_system
	else
		install_user
	fi
	finish
}

main "$@"
}
