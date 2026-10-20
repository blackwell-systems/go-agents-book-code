# Contributing to shortlink

## Building

shortlink is a single Go package tree. The main package is in `cmd/shortlink`; the rest of the code is under `internal/`: `internal/store` (SQLite), `internal/redirect`, `internal/api`, `internal/limit` (rate limiting), `internal/clicks`, `internal/hooks` (webhooks), and `internal/admin`.

Building needs Go 1.26 or later. To install the latest code:

```sh
go install example.com/shortlink/cmd/shortlink@latest
```

Release binaries exist for linux/amd64, linux/arm64, and darwin/arm64, so you only need to build from source when you are changing it.

To run what you built, use the settings in `config/defaults.yaml`. Any setting can be overridden by an environment variable of the same name in capitals, with dots turned into underscores: `rate_limit.burst` becomes `RATE_LIMIT_BURST`. Check the configuration before starting:

```sh
shortlink check-config
shortlink serve
```

The server stores links in one SQLite file, `shortlink.db` by default. Run one process per database file; two processes on one file fail with "database is locked".

## Tests and linting

Both must pass before a change is merged:

```sh
go test ./...
golangci-lint run
```

## Pull requests

- Every pull request that changes behavior adds a `CHANGELOG` entry under "Unreleased".
- Every bug fix comes with a test.
- Commits are signed off:

```sh
git commit -s
```

## Reporting security problems

Report vulnerabilities to security@shortlink.example. Do not open a public issue. Expect a reply within 3 working days.

## Documentation

User-facing behavior is described in the files under `docs/`: `docs/api.md`, `docs/deploy.md`, `docs/troubleshooting.md`, and `docs/rate-limits.md`. If your change alters what an operator or developer does with shortlink, update the matching file in the same pull request.
