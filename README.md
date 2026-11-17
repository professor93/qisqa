# qisqa

qisqa is a small, fast URL shortener: one binary that keeps its links in its
own data folder. It turns long URLs into short links through a
token-protected HTTP API and redirects everyone who opens them. Links
expire, can stop working shortly after they are first opened, can ask for a
password, and can live on several short domains at once. The short domains
and the accounts that use the API are managed with commands of the same
binary. The name is the Uzbek word for "short".

- The API creates, edits and deletes links, and in text mode replaces the
  URLs inside a text with short links. qisqa serves the whole reference
  itself, at `/api/llms.txt` and `/api/openapi.yaml`.
- A secured link opens through a small page, so that messengers and other
  programs that only fetch a page to preview it neither use up the link nor
  learn where it leads.
- qisqa keeps only a salted hash of a link's password, and five wrong
  passwords in a row lock the link for five minutes.
- Every short domain can have its own fallback URL, a list of the hosts its
  links may lead to, and aliases, fixed names such as
  `short.example/android`. Blocking a host stops every link to it and to
  its subdomains, whoever made it.
- A code is the link's number scrambled with the data folder's `key`:
  neighbouring numbers give unrelated codes, and no code contains a word
  from the built-in list of profanity. An IP that asks for too many codes
  that do not exist is blocked for a while.
- By default a change is answered once it is synced to disk; with
  `sync_every` it is answered at once and synced within an interval.
- With the log files on, qisqa writes an access, an admin and an error log,
  one file per UTC day, and compresses and removes old files by itself.

## Quick start

Download qisqa for your system from the
[releases page](https://github.com/professor93/qisqa/releases) (see
[Releases](#releases)), or build it in a copy of this repository with Go
1.27 or newer:

```sh
go build -o qisqa .
```

Write a config file with the address to listen on and the data folder, the
folder where qisqa keeps everything it stores. Every other key has a
default; [`config.example.yaml`](config.example.yaml) explains them all.

```sh
cat > config.yaml <<'EOF'
listen: "127.0.0.1:8080"
data_dir: ./data
EOF
```

Start qisqa. It creates the data folder and runs in the foreground until you
stop it with Ctrl-C:

```sh
./qisqa -config config.yaml
```

```
time=2026-11-15T11:55:58.147+05:00 level=INFO msg="qisqa started" version=dev listen=127.0.0.1:8080 config=config.yaml data_dir=./data admin=data/qisqa.sock
```

A new data folder has no short domain and no account, and without an
account the API is closed. In a second terminal, in the same folder, add a
domain and an account that may use it. These are admin commands: the same
binary hands them to the running server (see
[Accounts and domains](docs/accounts-and-domains.md)).

```sh
./qisqa -config config.yaml domain add short.example
./qisqa -config config.yaml account add me --domain short.example
```

```
added domain 1  short.example
added account 1  me
token: 43ee711d4502dfa72f13ba0cfa495576c0d60b6431671a06c89566b3ad282590
Keep the token: it is not shown again.
```

Put the token you got into `TOKEN` and create a link:

```sh
TOKEN=paste-your-token-here
curl -X POST http://localhost:8080/api/links \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url": "https://example.com/a/rather/long/address"}'
```

The answer, here spread over lines:

```json
{
  "code": "S2OmB",
  "domain": "short.example",
  "short_url": "https://short.example/S2OmB",
  "url": "https://example.com/a/rather/long/address",
  "expires_at": "2026-11-25T06:56:08Z",
  "secured": false,
  "password": false,
  "created": true
}
```

Open it, with the code from your answer. qisqa answers only for its own
domains, so on your own machine say which domain you mean:

```sh
curl -i -H "Host: short.example" http://localhost:8080/S2OmB
```

```
HTTP/1.1 302 Found
Cache-Control: no-store
Content-Type: text/html; charset=utf-8
Location: https://example.com/a/rather/long/address
X-Robots-Tag: noindex, nofollow
```

Before real use, add your own domains and put a proxy that handles HTTPS in
front of qisqa (see [Behind nginx](docs/nginx.md)).

## Documentation

The rest is in the folder [`docs`](docs/README.md), in this order:

- [Configuration](docs/configuration.md): every key of the config file, its
  default, and whether a change to it applies without a restart.
- [Data folder](docs/data-folder.md): what qisqa keeps there, when a change
  is on disk, backups, restoring a copy with `-skip`, and what happens when
  the disk fails.
- [Accounts and domains](docs/accounts-and-domains.md): the admin commands
  for domains, aliases, accounts and blocked hosts.
- [API](docs/api.md): creating, editing and deleting links, shortening the
  URLs inside a text, the errors, and the other addresses qisqa serves.
- [Links](docs/links.md): how codes work and how long to make them, secured
  links, password links and text mode.
- [Logs](docs/logs.md): the service log, and the columns of the access,
  admin and error logs.
- [Running qisqa](docs/running.md): running it as a service under systemd,
  Supervisor, OpenRC or launchd, the health check, and small servers.
- [Behind nginx](docs/nginx.md): nginx in front of qisqa, for HTTPS.
- [Docker](docs/docker.md): the image and the compose file.
- [Speed](docs/speed.md): what qisqa did on a laptop and in a small
  container, and the store's own benchmarks.
- [Development](docs/development.md): the tests, the code, and how a
  release is made.

## Speed

On an Apple M1 Pro laptop, in a container without limits, qisqa answered
304,013 redirects a second of one cached link, and 164,626 of random links
among 10,000 when most of them read a chunk file. In a container limited to
0.25 CPU and 250 MB it answered 5,428 a second of the cached link, and used
at most 16.4 MiB of memory. [Speed](docs/speed.md) has the whole table, how
it was measured, and the store's own benchmarks.

## Releases

Every release comes with ready-to-run binaries for Linux and macOS, each for
`amd64` and `arm64`. An archive holds the binary, this README and the
folder `docs`, the license and `config.example.yaml`; `checksums.txt` lists
the SHA-256 of every archive.

Download the archive for your system from the
[releases page](https://github.com/professor93/qisqa/releases) and unpack it:

```sh
tar -xzf qisqa_0.1.0_linux_amd64.tar.gz
./qisqa -version
```

With Go installed, this builds the latest release instead:

```sh
go install github.com/professor93/qisqa@latest
```

## License

MIT. See [LICENSE](LICENSE).
