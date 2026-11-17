# Development

This page is for working on qisqa itself: the tests, the code, and how a
release is made.

```sh
make test     # go test -race ./...
make build    # builds ./qisqa
```

The tests need no other service; each works in folders of its own, so they
leave nothing behind.

The code is one Go package, `internal/app`, with one small file per job,
and a `main.go` at the root that only starts it. It has two dependencies: a
YAML parser, and the IDNA tables of `golang.org/x/net` (with the
`golang.org/x/text` tables they need) that turn a target host with letters
outside ASCII into its `xn--` form.

## Making a release

A release is made by pushing a version tag. The release workflow builds the
archives and publishes them:

```sh
git tag v0.1.0
git push origin v0.1.0
```

`make dist VERSION=0.1.0` builds the same archives locally into `dist/`.
