# Running qisqa

This page shows how to install qisqa with its installer or by hand, how to
run it as a service that is started again when it stops, how to check that
it answers, and what it needs on a small server.

## The installer

`install.sh`, at the root of the repository, installs the latest release:

```sh
curl -fsSL https://raw.githubusercontent.com/professor93/qisqa/main/install.sh | sh
```

To read it before you run it:

```sh
curl -fsSLO https://raw.githubusercontent.com/professor93/qisqa/main/install.sh
sh install.sh
```

It asks no question and says each step in one line. It downloads the
release's `checksums.txt` and the archive for this system and processor
from GitHub, checks the archive's SHA-256, and stops before it installs
anything when the two differ. Then:

- **On Linux, as root, or as a user who may use `sudo`** (without a
  password, or with a terminal for `sudo` to ask on), it makes a system
  install. A missing `curl` (or `wget`), `tar` or `sha256sum` is installed
  with the package manager. It makes the system user `qisqa`, and puts the
  binary, the documents of the archive, a `config.yaml` that listens on
  `127.0.0.1:8080`, the data folder and the log folder into `/opt/qisqa`,
  laid out as in the [systemd](#systemd) example. Where systemd runs, it
  writes the unit of that example, enables the service and starts it;
  where OpenRC runs, the script of the [OpenRC](#openrc) example,
  likewise. It then waits up to 10 seconds for `/healthz`, and fails if
  qisqa does not answer. Where neither runs, it makes no service and says
  how to start qisqa by hand.
- **On macOS, with `--user`, or without root**, it installs for the
  current user into `~/qisqa`, mode 700: the binary, the documents and the
  same `config.yaml`, and no service. A missing tool is then an error.

Its last lines give the version, where the config is, and the commands
that add a domain and an account, in the form for that install. It writes
`config.yaml` only where there is none: a second run installs the binary
and the documents again, keeps `config.yaml` and the data folder as they
are, and restarts the service. It installs into a folder that is there only
when root owns it and nobody else may write to it.

| Option | Meaning |
|---|---|
| `--user` | Install for the current user only. |
| `--version 0.1.0` | The release to install. Default: the latest. |
| `--dir PATH` | The folder to install into. Default: `/opt/qisqa`, or `~/qisqa` for the current user. |
| `--help` | Show the options. |

Through the pipe, the options come after `sh -s --`:

```sh
curl -fsSL https://raw.githubusercontent.com/professor93/qisqa/main/install.sh | sh -s -- --user
```

To remove what it installed, stop the service and remove it, with systemd:

```sh
sudo systemctl disable --now qisqa
sudo rm /etc/systemd/system/qisqa.service
sudo systemctl daemon-reload
```

or with OpenRC, as root:

```sh
rc-service qisqa stop
rc-update del qisqa default
rm /etc/init.d/qisqa /var/log/qisqa.log
```

Then remove the folder and the user. The folder holds the data folder:
every link goes with it, so back it up first if you may want it again.

```sh
sudo rm -r /opt/qisqa
sudo userdel qisqa
```

On Alpine, `deluser qisqa` in place of `userdel qisqa`. A user install has
no service: stop qisqa and remove its folder, `rm -r ~/qisqa`.

## Running as a service

qisqa is one process that runs in the foreground and stops cleanly on
`SIGTERM`: it finishes the requests in progress and closes the data folder.
It also stops by itself, with status 1, when a sync fails (see
[Data folder](data-folder.md)), and then needs to be started again.

Every example below starts qisqa again when it stops by itself and gives
it 15 seconds to stop. Those for Linux run it as a user of its own,
`qisqa`, not as root, in the folder `/opt/qisqa`, which holds the binary,
`config.yaml`, the data folder `data` and the log folder `logs`. The
folder, the binary and `config.yaml` belong to root, and everyone may read
them; `data` and `logs` belong to the user `qisqa` alone, which can read
its binary and `config.yaml` but not change them. The launchd example runs
qisqa as your own user, in `~/qisqa`, where the installer puts it on macOS.

### systemd

Most Linux systems use systemd; this example was run on Debian and Ubuntu.
Make the user and the folder, with the binary and your `config.yaml` in the
current folder:

```sh
sudo useradd --system --home-dir /opt/qisqa --shell /usr/sbin/nologin qisqa
sudo install -d -o root -g root -m 755 /opt/qisqa
sudo install -m 755 qisqa /opt/qisqa/
sudo install -o root -g root -m 644 config.yaml /opt/qisqa/
sudo install -d -o qisqa -g qisqa -m 700 /opt/qisqa/data
sudo install -d -o qisqa -g qisqa -m 700 /opt/qisqa/logs
```

Put this into `/etc/systemd/system/qisqa.service`:

```ini
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
```

Start it, now and at every boot, then see its state and its output:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now qisqa
systemctl status qisqa
journalctl -u qisqa
```

`Restart=always` starts qisqa again a second after it stops by itself, also
when it ends with status 1. `StartLimitIntervalSec=0` lets it do so however
often that happens: without it, systemd gives up after 5 starts in 10
seconds. The service log goes to the journal.

### Supervisor

A block for [Supervisor](https://github.com/Supervisor/supervisor), whose
`autorestart=true` starts qisqa again:

```ini
[program:qisqa]
command=/opt/qisqa/qisqa -config /opt/qisqa/config.yaml
directory=/opt/qisqa
user=qisqa
autostart=true
autorestart=true
stopsignal=TERM
stopwaitsecs=15
stderr_logfile=/var/log/qisqa.log
```

The user and the folder are made as for [systemd](#systemd).

### OpenRC

On Alpine, Gentoo and other systems with OpenRC. On Alpine, make the user
and the folder as root, with the binary and your `config.yaml` in the
current folder:

```sh
addgroup -S qisqa
adduser -S -D -H -h /opt/qisqa -s /sbin/nologin -G qisqa qisqa
install -d -o root -g root -m 755 /opt/qisqa
install -m 755 qisqa /opt/qisqa/
install -o root -g root -m 644 config.yaml /opt/qisqa/
install -d -o qisqa -g qisqa -m 700 /opt/qisqa/data
install -d -o qisqa -g qisqa -m 700 /opt/qisqa/logs
```

Put this into `/etc/init.d/qisqa`, and make it executable with
`chmod 755 /etc/init.d/qisqa`:

```sh
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
```

Start it, now and at every boot, then see its state:

```sh
rc-update add qisqa default
rc-service qisqa start
rc-service qisqa status
```

`supervise-daemon` starts qisqa again a second after it stops by itself.
`respawn_max=0` lets it do so however often that happens: without it,
`supervise-daemon` gives up after a few times (on Alpine, `/etc/rc.conf`
says 5 times in 30 minutes). `retry` sends `SIGTERM` on a stop and
`SIGKILL` 15 seconds later. The service log goes to `/var/log/qisqa.log`;
`supervise-daemon` opens it as the user `qisqa`, so `start_pre` creates it
for that user first.

### launchd

On macOS, a per-user agent runs qisqa as your own user, from `~/qisqa`,
where the installer puts it. Put this into
`~/Library/LaunchAgents/example.short.qisqa.plist`, with the path of your
home folder, as `echo $HOME` prints it, in place of `/Users/you`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>example.short.qisqa</string>
	<key>WorkingDirectory</key>
	<string>/Users/you/qisqa</string>
	<key>ProgramArguments</key>
	<array>
		<string>/Users/you/qisqa/qisqa</string>
		<string>-config</string>
		<string>/Users/you/qisqa/config.yaml</string>
	</array>
	<key>KeepAlive</key>
	<true/>
	<key>ExitTimeOut</key>
	<integer>15</integer>
	<key>StandardOutPath</key>
	<string>/Users/you/qisqa/qisqa.log</string>
	<key>StandardErrorPath</key>
	<string>/Users/you/qisqa/qisqa.log</string>
</dict>
</plist>
```

Start it, see its state, and stop it:

```sh
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/example.short.qisqa.plist
launchctl print gui/$(id -u)/example.short.qisqa
launchctl bootout gui/$(id -u)/example.short.qisqa
```

`KeepAlive` starts qisqa again when it stops by itself, at most once every
10 seconds. `bootout` sends `SIGTERM`, and `SIGKILL` after `ExitTimeOut`,
15 seconds. The service log goes to `~/qisqa/qisqa.log`. The agent belongs
to your login session (the `gui` domain); a daemon for the whole machine is
a LaunchDaemon with a user of its own, which this page does not show.

### Docker

[Docker](docker.md) has the image and the compose file, which starts qisqa
again whenever it stops by itself.

## Health check

`GET /healthz` needs no token and works on any host name:

```sh
curl http://localhost:8080/healthz
```

```json
{"status":"ok"}
```

It answers `200` whenever the process can answer at all. Restart the
process when `/healthz` does not answer.

## Admin commands

The examples for Linux run qisqa in `/opt/qisqa`, so the example's
`data_dir: ./data` is `/opt/qisqa/data`. Run admin commands as the same
user and name that folder, as in `sudo -u qisqa /opt/qisqa/qisqa -data /opt/qisqa/data account list`:
the data folder and its socket are open to that user alone, and a relative
`data_dir` would be taken from the folder the command is run in. With the
launchd example, run them in `~/qisqa`, as in
`./qisqa -config config.yaml account list`.

## Small servers

qisqa is one process with nothing else to run beside it, and it does not
load the links into memory: a redirect reads the link's 16-byte slot, its
place in the chunk file, and the record with its URL. What it does keep in
memory has limits:

- the link cache, at most `cache.max_entries` links;
- at most 512 open chunk files, each with its table of hosts;
- `meta.json`, whole;
- the wrong-password counts of at most 10,000 links, and the addresses the
  limit on unknown codes is counting.

A password link costs one PBKDF2 hash when it is created and one for every
password tried, each about 10 ms of one core of an Apple M1 Pro laptop (10
cores). With 2 CPUs or more, password checks never take every CPU: they run
on one fewer at most (see [Password links](links.md#password-links)). On disk a
chunk file starts at about 1 MB, the slots of its 65,536 links, and grows by
the URL of each link.

In a container with a memory limit, set the environment variable
`GOMEMLIMIT` a little under that limit, in units such as `MiB`. Go then
works to stay below it, and collects garbage more often as memory use comes
near it.

[Speed](speed.md) shows what qisqa did in a container limited to a quarter
of a CPU and 250 MB.
