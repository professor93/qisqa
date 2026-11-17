# Logs

This page describes the service log and the three log files qisqa can keep,
with the columns of each.

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
group may, as for the [data folder](data-folder.md).
Files of the earlier single log, `qisqa-<date>.log`, are left alone: delete
them by hand.
