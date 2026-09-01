# Contributing

Thanks for wanting to help. The project is small and deliberately narrow; the
fastest way to get a change in is to keep it that way.

## Before you write code

Open an issue first for anything that is not an obvious bug fix. `incoda` has
an explicit scope and non-goals list in the
[README](README.md#scope-and-non-goals); a change that fights it will be
declined no matter how well it is built.

## The gates

The gate commands are the same locally and in CI, defined once in the
[`justfile`](justfile):

```sh
just ci        # gofmt no-op, go mod tidy no-op, go vet, go test -race
```

If `just ci` passes on your machine, CI will pass too. Windows CI runs the
same recipes (with a plain `go test`, no race detector).

## Sending the change

Keep pull requests small and single-purpose. New behaviour needs a test that
fails without it, and anything that changes the interface (commands, flags,
exit codes, the `status --json` schema) needs a line in the README.

The `status --json` schema is a contract: fields are added, never renamed or
removed.
