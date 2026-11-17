# Data folder

This page says what the data folder holds and how to keep it, back it up
and put a copy back, and what is on disk when the machine or the disk fails.

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
  link's number with `key` (see [How codes work](links.md#how-codes-work)): a new
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
  [How codes work](links.md#how-codes-work)). Domains added later start at the
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
  trust. The one exception is the sync of a new chunk file, which holds no
  link yet: when it fails, only the request that created the file fails,
  and qisqa goes on. With `sync_every` set the line also says
  that the changes answered since the last good sync may not be
  on disk, and `since_last_good_sync` says how long before the
  failure that sync began, or qisqa started if no sync had succeeded
  yet: those changes may be lost. Run qisqa under something that
  starts it again, as the [compose file](docker.md) and the
  [service examples](running.md#running-as-a-service) do, and it goes on
  from what is on disk. A disk that keeps failing shows as a qisqa that
  keeps restarting.
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

## When a change is on disk

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
there. The codes of the links lost answer as codes nobody was given. A lost
link whose slot reached the disk without its record, or whose slot and
record reached it without the line of its host in the file's table of
hosts, answers as expired links do instead, and each lookup of its code
logs `a link is damaged and leads nowhere`. A stop in order writes the exact
number, so no number is wasted then. A stop that was not in order passes
over up to 4,096 numbers of each code length of each domain that had new
links: it costs numbers only, no disk space. In either mode, a power loss
in the moment a new chunk file is being created, before any of its links is
answered, passes over the rest of that chunk's 65,536 numbers instead, and
the next start logs that the file is lost.
