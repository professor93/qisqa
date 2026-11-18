# Configuration

This page lists every key of the config file, with its default and whether
a change to it applies without a restart.

Everything is set in one YAML file. [`config.example.yaml`](../config.example.yaml)
explains every key. The short domains and the accounts are not in it: they
are in the data folder, see [Accounts and domains](accounts-and-domains.md).

qisqa checks the file every 5 seconds. Keys marked **yes** below apply
without a restart. If the file is broken, the error is logged and the running
configuration stays. Durations are written as `30s`, `15m`, `12h` or `10d`,
at most `36500d`. A relative path, as in `data_dir`, `log.dir`, `pages.dir`
and `pages.files`, is taken from the folder qisqa was started in.

| Key | Default | Meaning | Live reload |
|---|---|---|---|
| `listen` | `:8080` | Address the HTTP server listens on. | no |
| `data_dir` | `./data` | The [data folder](data-folder.md): the links, and the accounts and domains in `meta.json`. Created if missing. | no |
| `sync_every` | `0` | When a change is answered: `0` once it is synced to disk; an interval from `100ms` to `1m` at once, and synced within the interval. See [When a change is on disk](data-folder.md#when-a-change-is-on-disk). | no |
| `min_length` | `5` | Length of new codes, 3 to 10. | yes |
| `default_ttl` | `10d` | Lifetime of a link created without `expires_in`. | yes |
| `short_url_format` | `https` | Shape of `short_url` in API answers: `https`, `https_www`, `bare` or `www`. | yes |
| `bare_url_zones` | `top100` | Which domain zones make a [URL without a scheme](links.md#urls-without-a-scheme) in a text sent with `bare_urls`: `top100`, `top500` or `all`. | yes |
| `trim_codes` | `true` | Open a short link written with something glued after its code: `short.example/aB3xK.` opens `aB3xK`. See [Something glued to a link](links.md#something-glued-to-a-link). `false`: such a link is not found. | yes |
| `fallback_url` | empty | Where unknown hosts and unknown or expired codes are sent. Empty: a 404 page. | yes |
| `pages.dir` | empty | A folder that holds [your own pages](#your-own-pages): `index.html`, `404.html`, `429.html` and `500.html`. | yes |
| `pages.files` | empty | A path for one of your own pages, under its name: `index`, `404`, `429` or `500`. It wins over `pages.dir`. | yes |
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

## Your own pages

qisqa has a small index page and a small 404 page, and it answers a client
it has blocked in JSON and a failure of its own in plain text. Your own HTML
can take the place of each:

| Page | File in `pages.dir` | Shown |
|---|---|---|
| `index` | `index.html` | At `/` of a domain that has no fallback URL. |
| `404` | `404.html` | For an unknown host, and for an unknown or expired code, where no fallback URL is set. |
| `429` | `429.html` | To a client that `not_found_limit` has blocked. |
| `500` | `500.html` | When a link cannot be read from the data folder. |

```yaml
pages:
  dir: ./pages
  files:
    404: /srv/pages/missing.html
```

- Each page is one HTML file of at most 1 MB, written in UTF-8, that brings
  everything with it. qisqa serves no other file, so styles, scripts and
  images are written into the page or loaded from another host.
- `429` and `500` are shown to a GET or HEAD outside `/api/`. The API and
  every other request are answered as before.
- A fallback URL still redirects wherever it is set: your page is shown
  only where the built-in one was. The pages that open secured and password
  links are not replaced.
- A page that is not there leaves the built-in answer, and so does an empty
  file and a `pages.dir` that is not there. For a path of `pages.files` and
  for `pages.dir` the log says so.
- Some files cannot be a page: one over 1 MB, what is no regular file, a
  file qisqa may not read, and a link that is absolute or leads out of the
  folder it is in. The log then says which, and the pages in use stay: at
  start the built-in ones. qisqa tries again every 5 seconds until the file
  is repaired. A page never keeps the links from working.
- The files are checked with the config, every 5 seconds: a page that
  changed, appeared or went away is taken without a restart.
- From `pages.dir` only those four names are ever opened, and no name comes
  from a request. Every page, also one with a path of its own, is read from
  the folder it is in, and that folder is not left.

The pages are shown to everyone, as they are: whoever may write these files
decides what your visitors see. Keep them, like the config, writable by the
administrator only. A page is sent whole at every request, and `429.html`
to a blocked client for every request it makes, so keep them small.

The service that the installer sets up does not see `/home`, `/root` and
`/tmp`: put the pages into the install folder, for example
`/opt/qisqa/pages`. In Docker, mount the folder of the pages into the
container and name that path.
