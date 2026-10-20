# Redirects

A short URL answers with a redirect to its target. The status code and the
final target URL are controlled by two settings in config/defaults.yaml,
overridable by environment variables of the same name in capitals
(REDIRECT_STATUS, APPEND_QUERY).

## Status codes

| Setting | Default | Meaning |
| --- | --- | --- |
| redirect_status | 302 | 302 (temporary) or 301 (permanent) |

302 is the safe choice and the default. Browsers follow it every time and
re-ask shortlink where the link goes.

301 is cached by browsers. After a 301, a browser that has visited the link
keeps going to the old target even after the link is deleted or expires, and
even after you change redirect_status back. Use 301 only for links that never
change, since a shortened link cannot have its target edited later
(see api.md).

## Query strings

With append_query true (the default), the query string of the short URL is
added to the target's own query string. Given the target
`https://example.com/a/long/path`:

```text
GET /x7Kp2?ref=mail
Location: https://example.com/a/long/path?ref=mail
```

Set append_query false to drop the short URL's query string and redirect to
the target exactly as it was stored. The setting is global, not per link.

## HEAD requests

A HEAD request is answered like a GET: the same status code and Location
header. It does not count a click. Use HEAD when a client only wants to know
where a short URL points.

## Deleted and expired links

A short URL whose link has been deleted or whose expires_at time has passed
returns 404. Expiry is checked on each request, so a link stops redirecting at
its expires_at time; a link created with an expires_at already in the past is
expired immediately (see troubleshooting.md).

Remember the 301 warning above: a browser that cached a permanent redirect may
keep going to the old target even after the link returns 404.

## Click counting

Every redirect counts one click, including requests from bots. Counts are not
unique visitors; the same client following a link twice counts twice. A HEAD
request does not count.

Counts are held in memory and written to the database every
clicks.flush_interval (5s by default):

- A new click shows up in the API within about 5 seconds.
- A crash can lose up to 5 seconds of counts. A shutdown that finishes
  normally writes the pending counts first.

Daily counts per link are kept for clicks.retention_days (400 by default).
Read them with GET /api/links/{code}/clicks (see docs/analytics.md) or on the
admin page (see docs/admin.md). The
shortlink_redirects_total{status} metric on metrics_addr counts redirects by
status code.
