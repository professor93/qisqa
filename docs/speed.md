# Speed

This page shows what qisqa did on a laptop and in a container limited to a
quarter of a CPU and 250 MB, and what the store's own benchmarks measured.

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

The store's own benchmarks, in `internal/app/store_bench_test.go`, ran in the same
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
`go test -run '^$' -bench . -benchtime 5s ./internal/app`; `QISQA_BENCH_LARGE=1` adds the
store of 20,000,000 links.
