# Accounts and domains

This page describes the admin commands that manage the short domains, their
aliases, the accounts that use the API, and the blocked hosts.

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
flag, and the examples go on from the [Quick start](../README.md#quick-start).

## Domains

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
  [Data folder](data-folder.md)). A domain added again under the same name is
  a new domain with a new number and its own codes: the old links do not
  come back, but a new link can get a code an old link had, so an old short
  link may one day lead to a new target. While old links are still out
  there, keep the domain instead, and stop links with `account disable` or
  `account delete` (see [How codes work](links.md#how-codes-work)). An account
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

## Aliases

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

## Accounts

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

## Blocked hosts

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
