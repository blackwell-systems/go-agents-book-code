# shortlink

shortlink is a small URL shortener written in Go. It stores links in
SQLite and serves redirects from a single binary.

Run it with `go run ./cmd/shortlink`. Settings are read from
config/defaults.yaml, and any of them can be overridden with an
environment variable of the same name in capitals, such as
LISTEN_ADDR.

See CHANGELOG.md for what changed in each release.
