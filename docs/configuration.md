# Configuration

This page lists every key of the config file, with its default and whether
a change to it applies without a restart.

Everything is set in one YAML file. [`config.example.yaml`](../config.example.yaml)
explains every key. The short domains and the accounts are not in it: they
are in the data folder, see [Accounts and domains](accounts-and-domains.md).

qisqa checks the file every 5 seconds. Keys marked **yes** below apply
without a restart. If the file is broken, the error is logged and the running
configuration stays. Durations are written as `30s`, `15m`, `12h` or `10d`,
at most `36500d`. A relative path, as in `data_dir` and `log.dir`, is taken
from the folder qisqa was started in.

| Key | Default | Meaning | Live reload |
|---|---|---|---|
| `listen` | `:8080` | Address the HTTP server listens on. | no |
| `data_dir` | `./data` | The [data folder](data-folder.md): the links, and the accounts and domains in `meta.json`. Created if missing. | no |
| `sync_every` | `0` | When a change is answered: `0` once it is synced to disk; an interval from `100ms` to `1m` at once, and synced within the interval. See [When a change is on disk](data-folder.md#when-a-change-is-on-disk). | no |
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
