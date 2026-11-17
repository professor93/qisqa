# Running qisqa

This page shows how to run qisqa as a service that is started again when it
stops, how to check that it answers, and what it needs on a small server.

## Running as a service

qisqa is one process that runs in the foreground and stops cleanly on
`SIGTERM`: it finishes the requests in progress and closes the data folder.
It also stops by itself, with status 1, when a sync fails (see
[Data folder](data-folder.md)), and then needs to be started again.

Every example below starts qisqa again when it stops by itself, runs it as
a user of its own, `qisqa`, not as root, in the folder `/opt/qisqa`, which
holds the binary, `config.yaml` and the data folder `data`, and gives it 15
seconds to stop.

### systemd

On Debian, Ubuntu, Fedora, RHEL and most other Linux systems. Make the user
and the folder, with the binary and your `config.yaml` in the current
folder:

```sh
sudo useradd --system --home-dir /opt/qisqa --shell /usr/sbin/nologin qisqa
sudo install -d -m 755 /opt/qisqa
sudo install -m 755 qisqa /opt/qisqa/
sudo install -m 644 config.yaml /opt/qisqa/
sudo install -d -o qisqa -g qisqa -m 700 /opt/qisqa/data
```

Put this into `/etc/systemd/system/qisqa.service`:

```ini
[Unit]
Description=qisqa URL shortener
After=network.target

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
when it ends with status 1. The service log goes to the journal.

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
install -d -m 755 /opt/qisqa
install -m 755 qisqa /opt/qisqa/
install -m 644 config.yaml /opt/qisqa/
install -d -o qisqa -g qisqa -m 700 /opt/qisqa/data
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

On macOS. The service needs the user `qisqa`, and the folder `/opt/qisqa`
laid out as above. Put this into
`/Library/LaunchDaemons/example.short.qisqa.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>example.short.qisqa</string>
	<key>UserName</key>
	<string>qisqa</string>
	<key>WorkingDirectory</key>
	<string>/opt/qisqa</string>
	<key>ProgramArguments</key>
	<array>
		<string>/opt/qisqa/qisqa</string>
		<string>-config</string>
		<string>/opt/qisqa/config.yaml</string>
	</array>
	<key>KeepAlive</key>
	<true/>
	<key>ExitTimeOut</key>
	<integer>15</integer>
	<key>StandardOutPath</key>
	<string>/var/log/qisqa.log</string>
	<key>StandardErrorPath</key>
	<string>/var/log/qisqa.log</string>
</dict>
</plist>
```

Start it, see its state, and stop it:

```sh
sudo launchctl bootstrap system /Library/LaunchDaemons/example.short.qisqa.plist
sudo launchctl print system/example.short.qisqa
sudo launchctl bootout system/example.short.qisqa
```

`KeepAlive` starts qisqa again when it stops by itself, at most once every
10 seconds. `bootout` sends `SIGTERM`, and `SIGKILL` after `ExitTimeOut`,
15 seconds. The service log goes to `/var/log/qisqa.log`.

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

Every example runs qisqa in `/opt/qisqa`, so the example's
`data_dir: ./data` is `/opt/qisqa/data`. Run admin commands as the same
user and name that folder, as in `sudo -u qisqa /opt/qisqa/qisqa -data /opt/qisqa/data account list`:
the data folder and its socket are open to that user alone, and a relative
`data_dir` would be taken from the folder the command is run in.

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
