# Deploying shortlink

shortlink is one static binary. Copy it to the server with
config/defaults.yaml and run it under a process supervisor such as
systemd.

## Behind a reverse proxy

When shortlink runs behind a reverse proxy such as nginx, every request
appears to come from the proxy's address, so every user shares one rate
limit. List the proxy's address in trusted_proxies; shortlink then takes
the client's address from the X-Forwarded-For header that the proxy
adds.

## Backups

The database is a single SQLite file. Back it up with
`sqlite3 shortlink.db ".backup backup.db"` while the server runs;
copying the file directly can catch it partway through a write.
