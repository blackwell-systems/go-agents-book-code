# shortlink

shortlink is a small URL shortener written in Go. It stores links in
SQLite, in a single file named shortlink.db by default, and serves
redirects and a JSON API from one binary.

Start it with `go run ./cmd/shortlink`. Settings come from
config/defaults.yaml; see docs/configuration.md for every setting and
how to override it.

The documentation:

- docs/install.md: installing shortlink and a first run
- docs/configuration.md: every setting and its default
- docs/cli.md: every command and its flags
- docs/api.md: the JSON API for creating and managing links
- docs/api-keys.md: creating, scoping, and revoking API keys
- docs/short-codes.md: generated and custom codes, and what a target may be
- docs/redirects.md: how short URLs redirect
- docs/rate-limits.md: how requests are limited per client
- docs/analytics.md: click counts and how long they are kept
- docs/webhooks.md: events sent to another service, and verifying them
- docs/import-export.md: moving links in and out as CSV
- docs/admin.md: the read-only admin page
- docs/deploy.md: running shortlink in production
- docs/database.md: the SQLite database, migrations, and backups
- docs/upgrading.md: moving to a new release
- docs/logging.md: log formats and request IDs
- docs/metrics.md: Prometheus metrics
- docs/performance.md: measured throughput, the cache, and timeouts
- docs/security.md: keeping a shortlink server safe
- docs/architecture.md: how the code is organized
- docs/troubleshooting.md: common problems and their fixes
- docs/faq.md: short answers to common questions
- CHANGELOG.md: what changed in each release
- CONTRIBUTING.md: building, testing, and sending changes
