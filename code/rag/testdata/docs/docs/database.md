# The database

shortlink keeps everything in one SQLite file. The `database` setting
names it (`shortlink.db` by default) and can be overridden with the
`DATABASE` environment variable. The container image keeps its database
in the /data volume, so set `DATABASE=/data/shortlink.db` there. SQLite
runs in WAL mode. All storage goes through `internal/store`.

| Setting | Default | Meaning |
| --- | --- | --- |
| database | shortlink.db | The SQLite file |
| clicks.retention_days | 400 | How long daily click counts are kept |

## Tables

| Table | Holds |
| --- | --- |
| links | Every link: its short code, target URL, creation time, and expiry time |
| clicks_daily | Daily click counts per link, kept for clicks.retention_days (400 days by default) |
| api_keys | API keys by name and scope, plus revocation state and each key's SHA-256 hash |
| schema_version | Which migrations have been applied to this file |

Only the hash of an API key is stored, never the key itself. A link's
target cannot be changed after the link is created; delete the link and
create it again with the same slug.

## Migrations

Since v1.3.0 migrations run automatically when the server starts. The
`shortlink migrate` command applies them by hand and exits:

```sh
shortlink migrate
```

Use it to migrate without starting the server, and when upgrading from
v1.2.0 or earlier: those databases predate automatic migrations, so run
`shortlink migrate` with the new binary before starting it.

Migrations only go forward. Downgrading after a migration is not
supported; restore a backup instead. Back up before every upgrade.

## One process per database file

Run one shortlink process per database file. A second process pointed at
the same file fails with "database is locked". If you need more than one
instance, give each its own `database` file.

## Supported databases

SQLite is the only supported database. The file named by `database` is
the whole datastore.

## Backups

The database is a single SQLite file, but copying it directly can catch
it partway through a write. Back it up with the SQLite backup command
while the server runs:

```sh
sqlite3 shortlink.db ".backup backup.db"
```

Back up the database before every upgrade, and before running
`shortlink migrate` by hand. If an upgrade or a migration has to be
undone, stop the server and put the backup back in place as the database
file; run one process per database file as usual.
