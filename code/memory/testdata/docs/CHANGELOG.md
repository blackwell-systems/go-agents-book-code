# Changelog

## v1.4.0 (2026-09-15)

- Add daily click counts per link, kept for 400 days.
- Add the trusted_proxies setting, so the real client address is used
  behind a reverse proxy.
- Add a read-only admin page at /admin, off by default.

## v1.3.0 (2026-08-02)

- Add per-client rate limiting.
- Add webhooks for link.created, link.deleted, and link.expired.
- Run database migrations automatically at startup.
- Fix a redirect loop when a short link points to itself.

## v1.2.0 (2026-05-14)

- Add link expiry: a link can be created with an expires_at time.
- Add custom short codes, given in the slug field.
- Add `shortlink links import` and `shortlink links export` for CSV.

## v1.1.0 (2026-03-03)

- Add the JSON API for creating links.
- Add API keys with read, write, and admin scopes.
- Serve a health check at /healthz.
- Write logs as JSON by default; set log_format to text for the old
  format.

## v1.0.0 (2026-01-12)

- First release: redirects, the `shortlink links` commands, SQLite
  storage, Prometheus metrics on metrics_addr, and an in-memory link
  cache.
