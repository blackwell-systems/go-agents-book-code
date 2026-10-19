# Changelog

## v1.3.0 (2026-08-02)

- Add per-client rate limiting. Each client is identified by its IP
  address; the limit is set by rate_limit.requests_per_minute.
- Fix a redirect loop when a short link points to itself.

## v1.2.0 (2026-05-14)

- Add link expiry: links can be created with an expiry date.
- Store click counts per day instead of a single total.

## v1.1.0 (2026-03-03)

- Add a JSON API for creating links: POST /api/links.
- Serve a health check at /healthz.
