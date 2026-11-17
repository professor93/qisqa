# Running qisqa

This page shows how to run qisqa as a service that is started again when it
stops, how to check that it answers, and what it needs on a small server.

## Running as a service

qisqa is one process that runs in the foreground and stops cleanly on
`SIGTERM`: it finishes the requests in progress and closes the data folder.
It also stops by itself, with status 1, when a sync fails (see
[Data folder](data-folder.md)), and then needs to be started again. A block
for [Supervisor](https://github.com/Supervisor/supervisor), whose
`autorestart=true` does that:

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

With `directory=/opt/qisqa`, the example's `data_dir: ./data` is
`/opt/qisqa/data`. Run admin commands as the same user and name that
folder, as in `sudo -u qisqa /opt/qisqa/qisqa -data /opt/qisqa/data account list`:
the data folder and its socket are open to that user alone, and a relative
`data_dir` would be taken from the folder the command is run in.

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
