# qisqa

qisqa is a small, fast URL shortener: one binary that keeps its links in its
own data folder. It turns long URLs into short links through a
token-protected HTTP API and redirects everyone who opens them. Links
expire, can stop working shortly after they are first opened, can ask for a
password, and can live on several short domains at once. The short domains
and the accounts that use the API are managed with commands of the same
binary. The name is the Uzbek word for "short".

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
[Accounts and domains](#accounts-and-domains)).

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
front of qisqa (see [Behind nginx](#behind-nginx)).

## Configuration

Everything is set in one YAML file. [`config.example.yaml`](config.example.yaml)
explains every key. The short domains and the accounts are not in it: they
are in the data folder, see [Accounts and domains](#accounts-and-domains).

qisqa checks the file every 5 seconds. Keys marked **yes** below apply
without a restart. If the file is broken, the error is logged and the running
configuration stays. Durations are written as `30s`, `15m`, `12h` or `10d`,
at most `36500d`. A relative path, as in `data_dir` and `log.dir`, is taken
from the folder qisqa was started in.

| Key | Default | Meaning | Live reload |
|---|---|---|---|
| `listen` | `:8080` | Address the HTTP server listens on. | no |
| `data_dir` | `./data` | The [data folder](#data-folder): the links, and the accounts and domains in `meta.json`. Created if missing. | no |
| `sync_every` | `0` | When a change is answered: `0` once it is synced to disk; an interval from `100ms` to `1m` at once, and synced within the interval. See [When a change is on disk](#when-a-change-is-on-disk). | no |
| `min_length` | `5` | Length of new codes, 3 to 10. | yes |
| `default_ttl` | `10d` | Lifetime of a link created without `expires_in`. | yes |
| `short_url_format` | `https` | Shape of `short_url` in API answers: `https`, `https_www`, `bare` or `www`. | yes |
| `fallback_url` | empty | Where unknown hosts and unknown or expired codes are sent. Empty: a 404 page. | yes |
| `cache.ttl` | `6h` | How long a link is served from memory before it is read from the data folder again. | no |
| `cache.max_entries` | `10000` | How many links are kept in memory. | no |
| `not_found_limit.per_minute` | `30` | How many codes that do not exist one IP may ask for in a minute. | yes |
| `not_found_limit.block_for` | `5m` | How long an IP over that limit is blocked. | yes |
| `not_found_limit.client_ip_header` | empty | Behind a reverse proxy: the header that carries the client IP, for example `X-Real-IP`. | yes |
| `log.enabled` | `false` | Write the access, admin and error logs. | no |
| `log.dir` | `./logs` | Folder of the logs. | no |
| `log.redirects` | `false` | Also log every redirect. | no |
| `log.plain_days` | `7` | Days a log file stays as text, at least 1. | no |
| `log.archive_days` | `30` | Days a log file is then kept compressed before it is removed. `0`: no archive. | no |
| `blacklist` | empty | Extra words that must never appear in a generated code. | yes |

## Data folder

qisqa keeps everything it stores in one folder, `data_dir`:

```
data/
  LOCK                    keeps a second qisqa off the folder
  key                     32 random bytes: the secret every code is made with
  meta.json               the accounts, domains, aliases, blocked and removed hosts
  skipped                 numbers passed over after restoring a copy (qisqa -skip)
  qisqa.sock              the admin socket, there only while qisqa runs
  d1/                     the links of domain 1
    state.json            how far the numbering of each code length has gone
    5-000000000000.chunk  the first 65,536 links with 5-character codes
    5-000000000001.chunk  the next 65,536
  d2/                     the links of domain 2
```

A *chunk file* holds 65,536 links of one code length on one domain. Its
name is the code length and the file's number; the lowest numbers are the
oldest files. A domain's number is the first column of `domain list`. One
folder serves one qisqa: while one runs, it holds a lock on `LOCK`, and a
second one started on the folder stops with `data folder ./data is in use
by another qisqa process`.

- **Keep the folder private.** qisqa creates the folder and every file in
  it for its own user alone. The chunk files hold every long URL, and with
  `key` anyone can work out the code of every link. A folder that is there
  already is looked at before anything is written into it, through a link
  if `data_dir` is one: qisqa does not start when every user of the machine
  may write to it, and warns when its group may:

  ```
  time=2026-11-16T05:01:23.477+05:00 level=ERROR msg="qisqa failed" error="every user of this machine may write to the folder ./data (mode 0777): run chmod o-w ./data"
  time=2026-11-16T05:01:23.509+05:00 level=WARN msg="the folder's group may write to it; run chmod g-w on it unless that is meant" folder=./data
  ```
- **`key` and `meta.json` belong to the links.** A code is made from its
  link's number with `key` (see [How codes work](#how-codes-work)): a new
  key would make every old code lead nowhere. `meta.json` holds the domains
  and accounts the links belong to: started afresh, it would hand the old
  links to new domains and accounts. So qisqa refuses to start when a
  `d<number>` folder is there and `key` or `meta.json` is not:

  ```
  time=2026-11-15T11:48:28.676+05:00 level=ERROR msg="qisqa failed" error="data/key is missing but the folder holds links: put the key file back, or move the d* folders away to start empty"
  ```

- **Backup.** A backup is a copy of the whole folder, `key` and `meta.json`
  with the links. Stop qisqa while you copy it: a copy made while it runs
  may miss or lose the links that change during the copy. Keep the copy as
  private as the folder.
- **Restoring a copy.** A copy that is put back goes back to the moment it
  was made: the links made since are gone, and so is how far numbering had
  gone. Left at that, the next new links get the numbers of the lost ones,
  one after the other, and with the same `key` exactly their codes: a short
  link sent out in that time then leads to another account's target. So,
  with qisqa stopped:

  1. Put the whole copy back in place of the folder.
  2. Run `qisqa -skip N`, with `-config FILE` or `-data DIR` as for an
     admin command, and as the user qisqa runs as: a file written by
     another user is one qisqa may not be able to read. It starts no server,
     refuses to run while a qisqa runs on the folder, and refuses a folder
     without `key`, which is not the copy:

     ```
     $ qisqa -config config.yaml -skip 50000
     added 50000 to data/skipped, which now holds 50000: when qisqa starts, every numbering goes on 50000 numbers further than it would have
     ```
  3. Start qisqa. Every code length of every domain goes on `N` numbers
     further than it would have, once.

  `N` counts numbers, not links. Every link made uses one, and so does every
  code turned down because it holds a word from the built-in list or from
  `blacklist`, so a count of links is never enough by itself. Take the most
  links made on any one domain since the copy was made, add a tenth, more
  with a long `blacklist`, and add anything skipped on the folder since
  then. When unsure, take more: for example the days since the copy times
  the most links made in a day, doubled. It costs numbers only, no disk
  space. A code length that `N` fills is passed, so new codes get a
  character longer, as when a length fills up by itself (see
  [How codes work](#how-codes-work)). Domains added later start at the
  total too: the copy's `meta.json` may give them the numbers of domains
  added after it was made. The total is kept in `skipped`, and each `-skip`
  adds to it; until qisqa is started you may correct the number in it by
  hand. Keep the file afterwards, as you keep `state.json`. Without
  `-skip`, the codes made since the copy are handed out again one by one.
- **Old links.** qisqa removes nothing by itself: an expired or deleted link
  keeps its place in its file, so the folder grows until you delete old
  chunk files. Deleting a link, or giving it a new URL, overwrites the old
  URL's path, and whatever follows it, with zeros. The part before the path
  can stay: the host, with a user name, password or port if the URL has
  them, is kept once in its file for every link that names it, until the
  file is deleted. You may delete a chunk file while qisqa runs: within
  about 10 seconds its links answer as expired ones do. With `sync_every`
  set, a file changed in the current interval is noticed once those changes
  are synced, up to `sync_every` later. Every link in the file dies with
  it, whatever its lifetime. As long as `state.json` stays, the domain
  hands out none of its codes again. Once qisqa has found a chunk file
  gone, whether it was deleted while qisqa ran or before, it does not look
  for it again: one put back is read after the next restart.
- **Never delete the newest file of a length**: new links go into it. If it
  goes anyway, qisqa logs an error and goes on in the next file:

  ```
  time=2026-11-15T11:48:02.360+05:00 level=ERROR msg="the newest chunk file of a code length is lost; new links of that length go into the following chunk" file=data/d1/6-000000000000.chunk error="removed while qisqa was running"
  ```

- **Never delete `state.json`, nor the folder of a domain still in use.**
  `state.json` remembers how far the numbering of each length has gone,
  also past files that were lost or damaged. Without it qisqa counts on
  from the chunk files that are left, and from 0 when the domain's folder is
  gone. It can then hand out again codes that old links had, and an old
  short link then leads to a new target. Delete a domain's folder only after
  `domain remove`: no new link goes into it then.

- **When the disk fails.** A *sync* asks the system to write a file's data
  through to the disk; by default qisqa answers a request that changes a
  link only after its sync (see
  [When a change is on disk](#when-a-change-is-on-disk)). When the disk
  reports that a sync of a chunk file failed, nothing written to that file
  since its last good sync can be trusted. qisqa then logs `a sync of a
  chunk file failed` with the file's name, finishes the requests in
  progress and stops with status 1, instead of going on with data it cannot
  trust. With `sync_every` set the line also says that the changes answered
  since the last good sync may not be on disk, and `since_last_good_sync`
  says how long before the failure that sync began, or qisqa started if no
  sync had succeeded yet: those changes may be lost. Run qisqa under
  something that starts it again, as the [compose file](#docker) and the
  [supervisor example](#running-under-a-supervisor) do, and it goes on from
  what is on disk. A disk that keeps failing shows as a qisqa that keeps
  restarting.
- **Damaged files.** Every link is checked when it is read: a damaged one
  leads nowhere, rather than somewhere else, and is logged. A chunk file
  that is damaged as a whole is logged as `a chunk file is damaged and its
  links lead nowhere` and is not read again until qisqa restarts. After you
  repair such a file or put back a good copy, restart qisqa.
- **A short path.** The admin socket's path, `<data_dir>/qisqa.sock`, must
  fit into a socket address, about 100 bytes; a relative `data_dir` counts
  as written. qisqa does not start with a longer one, and says so:

  ```
  time=2026-11-15T11:59:19.431+05:00 level=ERROR msg="qisqa failed" error="admin socket: the path the-data-folder-of-the-url-shortener-on-this-server/kept-in-a-folder-whose-name-is-far-too-long/qisqa.sock is too long for a socket on this system, which allows 103 bytes: choose a shorter data_dir"
  ```

### When a change is on disk

`sync_every` decides when qisqa answers a request that changes a link: a
create, an edit, a delete, and the first open of a secured link. Lookups
never wait for the disk.

- **`sync_every: 0`, the default.** qisqa answers once the change is synced
  to disk. Nothing it answered is lost, whatever happens to the machine.
  Changes that come in together share a sync: while one sync runs, the
  changes that arrive wait, and the next sync covers them all.
- **An interval from `100ms` to `1m`, such as `sync_every: 1s`.** qisqa
  answers at once and syncs what changed within the interval, at most once
  an interval and only when something changed, so a burst of creates does
  not wait in line for the disk. When the machine loses power, the system
  crashes or the disk fails, the changes of the last interval can be lost:
  a link created in it leads nowhere, a link edited or deleted in it may
  lead to its old target or nowhere, and a one-time link opened in it may
  open once more. A link never leads to a target it was not given. A stop or
  a crash of qisqa alone loses nothing, for the system holds what it wrote,
  and a stop in order (`SIGTERM`, Ctrl-C) syncs everything first.

A code is never handed out twice, either way. With an interval, before qisqa
hands out a number it has not saved yet, it writes into `state.json` a
number 4,096 further and syncs it; after a power loss numbering goes on from
there. The codes of the links lost answer as codes nobody was given, or,
where a lost link's slot reached the disk without its record, as expired
links do, and each lookup of such a code logs `a link is damaged and leads
nowhere`. A stop in order writes the exact number, so no number is wasted
then. A stop that was not in order passes over up to 4,096 numbers of each
code length of each domain that had new links: it costs numbers only, no
disk space. In either mode, a power loss in the moment a new chunk file is
being created, before any of its links is answered, passes over the rest of
that chunk's 65,536 numbers instead, and the next start logs that the file
is lost.

## Accounts and domains

The short domains, the accounts, the aliases and the blocked hosts are kept
in `meta.json` in the data folder and managed with admin commands: the same
binary, with command words after its flags. A command reaches the running
server through `qisqa.sock` in the data folder, so it works only while qisqa
runs. It finds the folder through `-config FILE` (default `config.yaml`) and
its `data_dir`, or `-data DIR` names the folder directly:

```sh
./qisqa -config config.yaml domain list
./qisqa -data ./data domain list
```

A relative `data_dir`, such as `./data`, is taken from the folder the
command is run in, as it is for the server. So run the command in the
server's folder, or name the data folder with `-data`.

With no server on the folder:

```
qisqa is not running for data folder ./data
```

A change applies at once. A command that worked exits with status 0; one
that did not prints why and exits with 1. An unknown command prints the
list of commands. With the logs on, every command is written to the admin
log. Below, `qisqa` stands for the binary with its `-config` or `-data`
flag, and the examples go on from the [Quick start](#quick-start).

### Domains

```sh
qisqa domain add go.example --fallback https://example.com/ --allow example.com --allow '*.example.org'
qisqa domain set go.example --fallback ""
qisqa domain list
qisqa domain remove go.example
```

```
added domain 2  go.example
updated domain go.example
2  go.example  allow=example.com,*.example.org
1  short.example
removed domain go.example; its files stay in d2
```

- `domain add HOST` adds a short domain: a host name in lower case, without
  scheme, port or `www.`; `www.HOST` is served too. The domain gets a number
  that is never handed out again, and its links go into `d<number>`.
- `--fallback URL` is where unknown and expired codes of the domain are
  sent, and where `/` leads. Without it they go to the config's
  `fallback_url`, else to a small 404 page.
- `--allow HOST` limits the domain to links to these hosts: `example.com` is
  that host, `*.example.org` every subdomain of `example.org` but not
  `example.org` itself. Give it once for each. Without it, any host is
  allowed.
- `domain set HOST` changes `--fallback`, `--allow` or both; `--allow`
  replaces the whole list. `--fallback ""` removes the fallback, and
  `--allow ""` allows every host again.
- `domain remove HOST` stops the domain at once and takes it off every
  account. Its folder `d<number>`, there once it has links, stays. Only now
  that the domain is removed may you delete that folder, when you no longer
  want its links; the folder of a domain still in use must stay (see
  [Data folder](#data-folder)). A domain added again under the same name is
  a new domain with a new number and its own codes: the old links do not
  come back, but a new link can get a code an old link had, so an old short
  link may one day lead to a new target. While old links are still out
  there, keep the domain instead, and stop links with `account disable` or
  `account delete` (see [How codes work](#how-codes-work)). An account
  whose only domain was removed can create nothing (`403`, `domain: this
  account has no domain`) until `account set ID --domain HOST` gives it
  one.
- `meta.json` remembers every host a domain was removed from, and
  `domain add` refuses such a host, so that it is not added again by
  accident:

  ```
  domain "go.example" was removed before, and its old short links are dead. A domain added again hands out its own codes, so an old short link still in someone's hands may one day lead to a new target. To add it anyway: qisqa domain add go.example --again
  ```

  `--again` adds it anyway, as a new domain with a new number. It is needed
  every time the host is added again, and does no harm to a host that was
  never removed.

### Aliases

```sh
qisqa alias add short.example android https://example.com/app
qisqa alias remove short.example android
```

```
added alias short.example/android
removed alias short.example/android
```

An alias is a fixed name on one domain: `short.example/android` redirects to
its URL, never expires and ignores letter case. A name is 3 to 64 letters,
digits, `_` or `-`; `api` and `healthz` are taken. No code that equals an
alias is ever handed out, and an alias is looked up before the links, so it
hides a link that had its name as code. `domain list` shows a domain's
aliases.

### Accounts

```sh
qisqa account add team --domain short.example
qisqa account set 2 --name crew
qisqa account token 2
qisqa account disable 2
qisqa account enable 2
qisqa account delete 2
qisqa account list
```

```
added account 2  team
token: cc5dfb9a4705665f9ec7a2801e4cc507ba7488430bb9a6e1593e160ca1f8a421
Keep the token: it is not shown again.
updated account 2
token: e8c51187cd4f1872f68be30ebce97aa9b2c83dfa3ff99d87a576bae59e17f483
The old token no longer works.
disabled account 2; its links are stopped
enabled account 2; its links work again
deleted account 2; its links are stopped for good
1  me  short.example  enabled
```

- `account add NAME --domain HOST...` adds an account, with a name of 1 to
  64 bytes, that creates links on these domains; the first is its default.
  The token is shown once and kept only as a hash, so it cannot be shown
  again. Every account manages only its own links.
- `account set ID` changes `--name`, `--domain` or both; `--domain`
  replaces the whole list. Links on a domain taken off the account keep
  working, but the account can no longer edit or delete them.
- `account token ID` gives the account a new token. The old one stops
  working at once; the links do not change.
- `account disable ID` stops the token (`401`) and the account's links: they
  lead where expired links do. Nothing is lost: `account enable ID` brings
  both back.
- `account delete ID` removes the account for good. Its links stop and
  never work again, since its number is never handed out again. They stay in
  their chunk files until those are deleted.
- `account list` shows the number, name, domains and state of every
  account.

### Blocked hosts

```sh
qisqa block add bad.example
qisqa block remove bad.example
qisqa block list
```

```
blocked bad.example
unblocked bad.example
```

`block add HOST` stops every link to that host and to every subdomain of
it, whoever made it, and refuses new ones with `400` (`host
"sub.bad.example" is blocked`). `block remove HOST` lets the links work
again. `block list` shows one blocked host per line. Like every host an
admin command takes, a host with letters outside ASCII is written in its
`xn--` form, the form the links are checked in: `block add
xn--e1afmkfd.example` blocks `пример.example`, also in capitals.

## API

All API requests carry `Authorization: Bearer <token>` with the token of an
account. Bodies are JSON.
The complete reference is served by qisqa itself, without a token:

- `/api/llms.txt`: the whole API in one plain-text file, written for people
  and for language models;
- `/api/openapi.yaml`: the same as an OpenAPI 3.1 document.

The examples below use `https://short.example` as the address of the service.

### Create a link: `POST /api/links`

```sh
curl -X POST https://short.example/api/links \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url": "https://example.com/articles/2026/11/a-long-title?ref=mail", "expires_in": "10d"}'
```

```json
{
  "code": "aB3xK",
  "domain": "short.example",
  "short_url": "https://short.example/aB3xK",
  "url": "https://example.com/articles/2026/11/a-long-title?ref=mail",
  "expires_at": "2026-11-23T10:00:00Z",
  "secured": false,
  "password": false,
  "created": true
}
```

Only `url` is required.

| Field | Meaning |
|---|---|
| `url` | The long URL: `http://` or `https://`, at most 2048 bytes. A host with letters outside ASCII, such as `пример.example`, is turned into the ASCII form a browser looks up, `xn--e1afmkfd.example`: that form is checked, stored and returned in `url`, and the rest of the URL is kept as sent. An IPv4 address is written as four numbers, like `192.0.2.1`, never in an IPv6 form. Other spellings that a browser reads as another host, such as an IPv4 address written as one number, are refused, and so is a host name with an empty label, such as `evil.example..`, or with an `xn--` label that is not the ASCII form of a name, such as `xn--evil-.example`. |
| `domain` | The short domain to use, one of the account's. Default: the account's first domain. |
| `expires_in` | Lifetime of the link. Default: `default_ttl`. |
| `expires_after_open` | Makes the link [secured](#secured-links). |
| `password` | Makes the link ask for a [password](#password-links) of 4 to 128 bytes. |
| `format` | Shape of `short_url`: `https`, `https_www`, `bare` or `www`. Default: `short_url_format`. |

The answer is `201` for a new link. The same request sent again within 10
minutes gets the same link back with `200` and `"created": false`, so a
retry does not make a second link; a secured or password link is always
new. A domain the account may not use is refused with `403`. A URL to a
blocked host, or one whose host is no host name or is written in another
spelling, is refused with `400`:

```json
{"error": "url: \"-пример.example\" is not a valid host name"}
```

### Shorten the URLs inside a text: `POST /api/links`

```sh
curl -X POST https://short.example/api/links \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"text": "Read more: https://example.com/articles/2026/11/a-long-title?ref=mail. Thank you!", "format": "bare"}'
```

```json
{
  "text": "Read more: short.example/aB3xK. Thank you!",
  "links": [
    {
      "code": "aB3xK",
      "domain": "short.example",
      "short_url": "short.example/aB3xK",
      "url": "https://example.com/articles/2026/11/a-long-title?ref=mail",
      "expires_at": "2026-11-23T10:00:00Z",
      "secured": false,
      "password": false,
      "created": true
    }
  ]
}
```

See [Text mode](#text-mode) for the rules.

### Edit a link: `PATCH /api/links/{code}`

```sh
curl -X PATCH "https://short.example/api/links/aB3xK?domain=short.example" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url": "https://example.com/articles/2026/12/another-title", "expires_in": "3d"}'
```

```json
{
  "code": "aB3xK",
  "domain": "short.example",
  "short_url": "https://short.example/aB3xK",
  "url": "https://example.com/articles/2026/12/another-title",
  "expires_at": "2026-11-16T10:00:00Z",
  "secured": false,
  "password": false,
  "created": false
}
```

Send `url`, `expires_in` or both. `expires_in` counts from now and may also
shorten the lifetime. `?domain=` is optional and defaults to the account's
first domain. A secured or password link cannot be edited. An account edits
and deletes only its own links; another account's link is answered as one
that does not exist.

### Delete a link: `DELETE /api/links/{code}`

```sh
curl -X DELETE "https://short.example/api/links/aB3xK?domain=short.example" \
  -H "Authorization: Bearer $TOKEN"
```

The answer is `204` with no body, and the link stops working at once.

### Errors

Every error is a JSON object with a message that says what to fix:

```json
{"error": "expires_in: invalid duration \"10x\" (examples: 30m, 12h, 10d)"}
```

| Status | Meaning |
|---|---|
| `400` | The request is wrong, or its URL may not be shortened. |
| `401` | The token is missing or wrong, or its account is disabled. |
| `403` | The domain is not one of the account's, or the account has no domain. |
| `404` | No such link, or it belongs to another account. |
| `409` | The link is secured or has a password, and cannot be edited. |
| `429` | This IP is blocked after too many bad requests. |
| `500` | qisqa failed; the reason is in its log. |

### Other addresses

| Address | What it does |
|---|---|
| `GET /{code}` | Redirects to the long URL with `302`, and an alias to its URL. A secured or password link gets a page instead. |
| `POST /{code}` | Sent by the script of that page: answers with the long URL, see [Secured links](#secured-links) and [Password links](#password-links). |
| `GET /` | Redirects to the domain's fallback URL, else shows a small page. |
| `GET /healthz` | Health check, see [Running under a supervisor](#running-under-a-supervisor). |
| `GET /robots.txt` | Tells search engines not to index short links. |

## How codes work

- A code is a number scrambled with the data folder's `key`. Every domain
  counts the links of each code length 0, 1, 2, and so on. A link's number
  is put through a shuffle that `key` chooses and written with the 62
  letters and digits, `min_length` characters long. Neighbouring numbers
  give unrelated codes, so without `key` nobody can tell which codes are in
  use.
- A code leads straight to its link: its number names the chunk file and
  the place in it. A code that was never handed out is known as such without
  reading any link from disk.
- **A length is used up to a tenth.** Of the 62^length codes of a length,
  qisqa hands out at most a tenth. Then it goes on with codes one character
  longer by itself and logs a warning. Raise `min_length` when you see it.

**Choosing `min_length`.** A random guess finds a code that was handed out
with a chance of *codes handed out ÷ 62^length*, and only a live link opens.
As a length is used up to a tenth, that is at worst 1 guess in 10:

| Length | Codes it hands out | A guess finds a link, with 10,000 links | with 1 million links |
|---|---|---|---|
| 3 | 23,832 | 1 in 24 | (full) |
| 4 | 1,477,633 | 1 in 1,478 | 1 in 15 |
| 5 | 91,613,283 | 1 in 91,613 | 1 in 916 |
| 6 | 5,680,023,558 | 1 in 5.7 million | 1 in 56,800 |
| 7 | 352 billion | 1 in 352 million | 1 in 3.5 million |
| 8 | 21.8 trillion | 1 in 21.8 billion | 1 in 218 million |

The limit on unknown codes slows a guesser down but does not stop one who
has many addresses. So when links lead to something private, choose the
length for the number of links you expect: the length grows by itself only
when a tenth of its codes are handed out, and then a guess finds a link one
time in ten. `min_length` can be changed at any time, and existing links
keep working.

- A code never contains a word from the built-in list of Uzbek, Russian and
  English profanity, nor from `blacklist`. Digits that look like letters
  count as letters: `0` as `o`, `1` as `i`, `3` as `e`, `4` as `a`, `5` as
  `s`, `7` as `t`.
- Codes are case-sensitive: `aB3xK` and `ab3xk` are different links.
- Every domain has its own links. The same code on two domains is two
  different links.
- A domain never hands out a code again, also after its link expired or was
  deleted, or its file was deleted, so an old link does not lead somewhere
  new. Three things undo this: deleting `state.json` or the folder of a
  domain in use, or putting back an older copy of the data folder without
  `qisqa -skip` (see [Data folder](#data-folder)); and removing a domain
  and adding its host again, which `domain add` does only with `--again`.
  A host added again is a new domain with its own codes: the old links stay
  dead, but a new link there can get a code an old link had. A short link
  still in someone's hands can then lead to a new target. The chance for
  one old link is the new domain's links of that length ÷ 62^length, at
  worst 1 in 10. What to do instead is under `domain remove` in
  [Domains](#domains).
- An unknown or expired link goes to the domain's fallback URL, else to the
  global `fallback_url`, else to a small 404 page. So does a link of a
  disabled or deleted account, and a link to a blocked host.
- An IP that asks for more than `not_found_limit.per_minute` codes that do
  not exist is blocked for `not_found_limit.block_for` and gets `429` for
  every request. Wrong API tokens and wrong passwords count as well. Expired
  links do not count: those are real people opening an old link.

## Secured links

A link created with `expires_after_open` is *secured*:

```sh
curl -X POST https://short.example/api/links \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url": "https://example.com/articles/2026/11/a-long-title?ref=mail", "expires_after_open": "1m"}'
```

- `"1m"`: the link works for one minute after it is first opened.
  `"0"`: it works exactly once.
- A secured link that nobody opens still expires at `expires_at`.
- Opening it does not redirect. The browser gets a small page whose script
  asks qisqa for the long URL and then goes there. Messengers and other
  programs that only fetch a page to preview it neither use up the link nor
  learn where it leads.
- Every request creates a new code, even for the same URL.
- A secured link cannot be edited. Deleting it kills it at once.
- As for every link, its domain does not hand out its code again; see
  [How codes work](#how-codes-work) for what undoes that.

This is not absolute protection: a scanner that runs a full browser executes
the script like a person's browser does.

## Password links

A link created with `password` opens only for someone who knows the
password:

```sh
curl -X POST https://short.example/api/links \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url": "https://example.com/reports/2026/q3.pdf", "password": "plum-river-42"}'
```

- The password is 4 to 128 bytes. qisqa keeps only a salted hash of it
  (PBKDF2-HMAC-SHA256), never the password itself; the answer says
  `"password": true`.
- Opening the link shows a small page with one password field. Its script
  sends `POST /{code}` with `{"password": "..."}`, which is answered with
  `200` and `{"url": "..."}` for the right password, `403` and
  `{"error": "wrong password"}` for a wrong one, and `429` while the link is
  locked. The page and every answer before the right password hold nothing
  of the long URL.
- Five wrong passwords in a row lock the link for five minutes; the right
  password starts the count again. A wrong password also counts against the
  client's IP, as a code that does not exist does. Neither count is written
  to disk.
- No more of a link's passwords are checked at once than could still lock
  it, and no more passwords of all links together than one fewer than the
  CPUs qisqa may use, one at least: with 2 CPUs one at a time, and the other
  CPU is left to the redirects. A password sent while that many are being
  checked gets `429` with `Retry-After: 1`: it is not counted, and a second
  later there is room.
- With `expires_after_open` as well, the link asks for the password first:
  only the right password opens it, so only the right password uses up a
  one-time link.
- Every request creates a new code, even for the same URL. A password link
  cannot be edited. Deleting it kills it at once.

## Text mode

Send `text` instead of `url` and qisqa replaces the URLs inside it:

- Only URLs that start with `http://` or `https://` are replaced. Punctuation
  after a URL stays in the text.
- A URL that already points at one of the service's own short domains, with
  or without `www.`, is never replaced: a short link is not shortened again.
- A URL that may not be shortened, or whose short link would not be shorter
  than the URL itself, is left as it is.
- To choose the URLs yourself, list them in `urls`. Then exactly those are
  shortened, and a URL in the list that may not be shortened is an error.
- A URL that occurs several times becomes one link.
- `domain`, `expires_in`, `expires_after_open`, `password` and `format`
  apply to every link of the text.
- Limits: 10 KB of text and 20 links per request.

## Logs

- **Service log.** Start, stop, configuration reloads and errors go to
  standard error.
- **Log files.** Off by default; turn them on with `log.enabled: true`.
  qisqa then keeps three logs in `log.dir`, each in one file per UTC day:

| File | A line for | Columns after the time |
|---|---|---|
| `access-2026-11-14.log` | every created, edited or deleted link and every opened secured or password link; with `log.redirects: true` every redirect too | event (`create`, `update`, `delete`, `open` or `redirect`), domain, code, client address, account number |
| `admin-2026-11-14.log` | every admin command | result (`ok` or the error), then the words of the command |
| `error-2026-11-14.log` | every warning and error of the service log | level (`WARN` or `ERROR`), message, then one `key=value` column per detail |

A line is the time in UTC (`HH:MM:SS`), then each column after a tab:

```
08:36:36	create	short.example	aB3xK	203.0.113.7	1
```

The account number is that of the account that made the request, and is
empty for an open or a redirect; its token is never written. Inside a
column, a backslash, tab, newline and carriage return are written as `\\`,
`\t`, `\n` and `\r`, and any other control character as `\x` and two hex
digits, so a tab always separates columns and a line is always one event.

A file stays as text for `log.plain_days` days, then is compressed to
`.log.gz` and kept for `log.archive_days` days, then is removed; today's
file is never compressed. qisqa does this by itself, so no external log
rotation is needed. Read an archive with `gzip -dc`:

```sh
gzip -dc logs/access-2026-11-01.log.gz
```

qisqa does not count how often a link is opened, but the access log has a
line for every open. With `log.redirects: true`, this counts the opens of
`short.example/aB3xK`, in the text files and the archives alike:

```sh
gzip -dcf logs/access-* | awk -F'\t' '$3 == "short.example" && $4 == "aB3xK" && ($2 == "open" || $2 == "redirect")' | wc -l
```

The access log holds client addresses and the codes of secured and password
links, so qisqa creates the folder and the files, archives too, readable
only by the user it runs as. Keep the folder writable by that user alone.
With the logs on, qisqa does not start when it cannot write to `log.dir`,
nor when every user of the machine may write to it, and warns when its
group may, as for the [data folder](#data-folder).
Files of the earlier single log, `qisqa-<date>.log`, are left alone: delete
them by hand.

## Running under a supervisor

qisqa is one process that runs in the foreground and stops cleanly on
`SIGTERM`: it finishes the requests in progress and closes the data folder.
It also stops by itself, with status 1, when a sync fails (see
[Data folder](#data-folder)), and then needs to be started again. A block
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

**Health check.** `GET /healthz` needs no token and works on any host name:

```sh
curl http://localhost:8080/healthz
```

```json
{"status":"ok"}
```

It answers `200` whenever the process can answer at all. Restart the
process when `/healthz` does not answer.

## Behind nginx

qisqa speaks plain HTTP, so put a proxy that handles HTTPS in front of it.
With [nginx](https://github.com/nginx/nginx) on the same machine, two
settings in `config.yaml`:

```yaml
listen: "127.0.0.1:8080"         # only nginx can reach qisqa
not_found_limit:
  client_ip_header: X-Real-IP    # the header nginx puts the client address in
```

and one file for nginx, for example `/etc/nginx/conf.d/qisqa.conf`:

```nginx
upstream qisqa {
    server 127.0.0.1:8080;
    keepalive 32;    # reuse the connections to qisqa
}

server {
    listen 80;
    server_name short.example www.short.example;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl;
    server_name short.example www.short.example;

    ssl_certificate     /etc/letsencrypt/live/short.example/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/short.example/privkey.pem;

    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_set_header Host $host;                # qisqa finds the domain by it
    proxy_set_header X-Real-IP $remote_addr;    # replaces whatever the client sent

    location / {
        proxy_pass http://qisqa;
    }

    # Optional: only these addresses may use the API.
    # location /api/ {
    #     allow 203.0.113.10;
    #     deny  all;
    #     proxy_pass http://qisqa;
    # }
}
```

- List every short domain of `domain list`, with and without `www.`, in
  both `server_name` lines. The certificate must cover all of them.
- Without `client_ip_header` every request seems to come from nginx, and one
  client that guesses codes gets everyone blocked.
- Set `client_ip_header` only when qisqa can be reached through the proxy
  alone, as with `listen: "127.0.0.1:8080"`. Anyone who can reach qisqa
  directly can write that header themselves.

## Docker

The image holds nothing but the binary, `/qisqa`, on a base without a
shell. It runs as user `65532`, not root, reads its configuration from
`/etc/qisqa/config.yaml` and keeps the data folder in a volume at `/data`.
Relative paths are taken from `/`, so the example's `data_dir: ./data` is
that volume.

```sh
cp config.example.yaml config.yaml
docker build -t qisqa .
docker run -d --name qisqa -p 127.0.0.1:8080:8080 \
  -v "$PWD/config.yaml:/etc/qisqa/config.yaml:ro" -v qisqa-data:/data qisqa
docker exec qisqa /qisqa -config /etc/qisqa/config.yaml domain add short.example
```

[`docker-compose.yml`](docker-compose.yml) does the same, and starts qisqa
again whenever it stops by itself (`restart: unless-stopped`):

```sh
cp config.example.yaml config.yaml
docker compose up -d --build
docker compose exec app /qisqa -config /etc/qisqa/config.yaml account list
```

To put back a copy of the data folder (see [Data folder](#data-folder)),
stop the service, put the copy into the volume, and run `-skip` in a
container of its own before you start the service again. The words after
the service name take the place of the image's `-config
/etc/qisqa/config.yaml`, so they give it again:

```sh
docker compose stop app
docker compose run --rm app -config /etc/qisqa/config.yaml -skip 50000
docker compose start app
```

- `-p 127.0.0.1:8080:8080` makes qisqa reachable from this machine only,
  which is where a reverse proxy looks for it. Docker opens a port published
  without an address, as in `-p 8080:8080`, on every network interface, and
  host firewalls such as `ufw` do not stop it.
- User `65532` must be able to read the mounted `config.yaml`.
- Admin commands run inside the container, as in
  `docker exec <container> /qisqa -config /etc/qisqa/config.yaml account list`.
- Keep `data_dir` in `/data`. Anything else is written inside the container
  and is lost with it.
- The service log goes to `docker logs`. With the log files on, set
  `log.dir` to a folder the container's user can write, for example
  `/data/logs` in the volume. The example's `./logs` would be `/logs`, which
  that user cannot create, and qisqa does not start.

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
on one fewer at most (see [Password links](#password-links)). On disk a
chunk file starts at about 1 MB, the slots of its 65,536 links, and grows by
the URL of each link.

In a container with a memory limit, set the environment variable
`GOMEMLIMIT` a little under that limit, in units such as `MiB`. Go then
works to stay below it, and collects garbage more often as memory use comes
near it.

[Speed](#speed) shows what qisqa did in a container limited to a quarter
of a CPU and 250 MB.

## Speed

The same image, holding 10,000 links to the same URLs, in two containers:

| | Apple M1 Pro laptop, 10 cores, no limits | Container limited to 0.25 CPU and 250 MB |
|---|---:|---:|
| Redirects a second, one cached link | 304,013 | 5,428 |
| Redirects a second, random links among the 10,000, all in the cache | 242,778 | 5,657 |
| The same with `cache.max_entries: 1000`, so that most read a chunk file | 164,626 | 4,518 |
| Links created a second through the API, one request at a time | 880 | 680 |
| Memory, the highest `docker stats` showed | 107 MiB | 16.4 MiB |
| Requests that failed | 0 | 0 |
| `wrk` settings, each run 30 s | `-t10 -c2048` | `-t2 -c32` |

Both containers ran in Docker's Linux virtual machine on the same laptop,
with the example configuration, and the second with `GOMEMLIMIT=200MiB`.
`wrk` ran on the same laptop too, in a container beside qisqa's that
shared its network, so it shared the processor with the server in the first
column: there qisqa used 4.2 to 5.5 of the 10 cores. Past 2,048
connections only the one cached link got faster, to 338,909 a second with
4,096. Memory grows with the connections: in the first column it was at
most 32 MiB with 32 of them. The links were created from the laptop
itself, through the published port.

The store's own benchmarks, in `store_bench_test.go`, ran in the same
Linux virtual machine, without its CPU or memory limited, with the data on
its disk. A lookup of a random link in a store of 2,000,000 links runs
466,000 times a second on one goroutine and 2,061,000 times on 8; in a
store of 20,000,000 links, 394,000 and 1,998,000 times. These lookups read
files that the system held in memory, since the stores had just been
written. A lookup whose file is not in memory also costs a read from the
disk, which was not measured; at 49 bytes a link, the store of 20,000,000
links takes about 1 GB, more than a server with 250 MB holds in memory.
With `sync_every: 0` a create of one link, synced to disk before it
returns, runs 612 times a second with one writer; 64 writers at once share
their syncs, about 32 creates to a sync, and reach 20,840 a second (19,962
in the slowest of five runs, 22,989 in the fastest), and 23,614 a second
when Go uses one CPU. These creates were measured on an ext4 file system
kept in a file on the virtual machine's disk, and vary from run to run
with that disk. With `sync_every: 1s` a create returns before its sync:
one writer runs 250,201 creates a second, and 64 writers 206,992. A link
to a 47-byte URL takes 49 bytes of disk, there and on the laptop itself:
its 16-byte slot and a 33-byte record, with the host in the file's table.
The benchmarks run with
`go test -run '^$' -bench . -benchtime 5s`; `QISQA_BENCH_LARGE=1` adds the
store of 20,000,000 links.

## Development

```sh
make test     # go test -race ./...
make build    # builds ./qisqa
```

The tests need no other service; each works in folders of its own, so they
leave nothing behind.

The code is one Go package with one small file per job, and two
dependencies: a YAML parser, and the IDNA tables of `golang.org/x/net`
(with the `golang.org/x/text` tables they need) that turn a target host
with letters outside ASCII into its `xn--` form.

## Releases

Every release comes with ready-to-run binaries for Linux and macOS, each for
`amd64` and `arm64`. An archive holds the binary, this README, the license
and `config.example.yaml`; `checksums.txt` lists the SHA-256 of every archive.

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

A release is made by pushing a version tag. The release workflow builds the
archives and publishes them:

```sh
git tag v0.1.0
git push origin v0.1.0
```

`make dist VERSION=0.1.0` builds the same archives locally into `dist/`.

## License

MIT. See [LICENSE](LICENSE).
