# FAQ

## Why does shortlink use SQLite?

All links live in one SQLite file, shortlink.db by default, in WAL mode. There is no separate database server to install or keep in step with shortlink, and a backup is one command while the server runs:

```sh
sqlite3 shortlink.db ".backup backup.db"
```

See deploy.md for backup notes.

## Can shortlink use Postgres?

No. Storage is SQLite only, in a single file set by the database setting. Other databases are not supported. Run one process per database file.

## Can I change where a link points?

No. A link's target is fixed when the link is created. To repoint a short code, delete the link and create it again with the same slug:

```sh
shortlink links delete x7Kp2
shortlink links add https://example.com/new/path --slug x7Kp2
```

Deleting first matters: a slug already in use gets 409 Conflict. The codes api, admin, healthz, and metrics are reserved. Note that after a redirect_status of 301, browsers that already visited the link may keep going to the old target (see below).

## Are clicks unique visitors?

No. Each redirect counts one click, bots included. The counts tell you how often a short URL was followed, not how many different people followed it. Daily counts per link are kept for clicks.retention_days (400 days). HEAD requests are answered like GET but do not count a click.

## Why does a link still redirect after I deleted it?

If redirect_status is 301, browsers cache the redirect and keep going to the old target even after the link is deleted or expires. 302 (the default) is not cached that way. Use 301 only for links that never change.

## How soon do clicks show up?

Within about 5 seconds. Clicks are held in memory and written to the database every clicks.flush_interval (5s by default), so a new click appears in the API after roughly that long. A crash can lose up to 5 seconds of counts; a normal shutdown writes them first. GET /api/links/{code} in api.md returns a link's total clicks.

## How do I run more than one server process?

Run one process per database file. Two shortlink processes sharing one SQLite file fail with "database is locked". Give each process its own database setting and its own file. If you are running shortlink behind a reverse proxy, see deploy.md for trusted_proxies, and see troubleshooting.md for the locked-database error.

## Where do I report a security problem?

Report vulnerabilities to security@shortlink.example, not in public issues. Expect a reply within 3 working days.
