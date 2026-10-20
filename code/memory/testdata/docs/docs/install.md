# Installing shortlink

shortlink is one static binary. It needs a SQLite file for its database and serves plain HTTP; TLS is terminated at a reverse proxy such as nginx or Caddy (see deploy.md).

## Release binaries

Download the archive for your platform from the releases page. Binaries are published for linux/amd64, linux/arm64, and darwin/arm64. Unpack it somewhere on the server and run the binary directly or under a process supervisor such as systemd.

## go install

With Go 1.26 or later installed:

```sh
go install example.com/shortlink/cmd/shortlink@latest
```

This puts `shortlink` in your Go binary directory.

## Container image

The image is `shortlink/shortlink`. It keeps its database in the `/data` volume, so set `DATABASE=/data/shortlink.db` and mount the volume, or the database disappears with the container:

```sh
docker run --name shortlink -p 8080:8080 \
  -v shortlink-data:/data \
  -e DATABASE=/data/shortlink.db \
  shortlink/shortlink
```

Any setting can be given as an environment variable of the same name in capitals, with dots turned into underscores. `rate_limit.burst` becomes `RATE_LIMIT_BURST`; `database` becomes `DATABASE`, as above.

## First run

Copy `config/defaults.yaml` next to the binary and check the configuration before starting anything:

```sh
shortlink check-config
```

It loads the configuration, reports errors, and exits. Fix anything it points out, then start the server:

```sh
shortlink serve
```

`shortlink` with no command does the same thing as `shortlink serve`.

The settings you are most likely to change on a first run:

| Setting | Default | Change it when |
| --- | --- | --- |
| listen_addr | :8080 | the port is taken |
| base_url | http://localhost:8080 | the server is reachable under a public address |
| database | shortlink.db | the SQLite file belongs somewhere else |

`base_url` is the start of every short URL shortlink hands out, so set it to the address people will actually use. The database is one SQLite file; run one process per database file.

## Checking it works

The health check is at /healthz. Ask for it on the listen port:

```sh
curl -i http://localhost:8080/healthz
```

If the server answers, it is up and taking requests. Your first short URL is `base_url` plus the short code; with the defaults, a link with the code `x7Kp2` is `http://localhost:8080/x7Kp2`.

Metrics are served separately on `metrics_addr` (`:9090` by default), which keeps `/metrics` off the public port.

Create your first link with the CLI or the JSON API (see docs/api.md):

```sh
shortlink links add https://example.com/a/long/path
```

If something does not behave as expected, see docs/troubleshooting.md. For backups and reverse proxy setup, see docs/deploy.md.
