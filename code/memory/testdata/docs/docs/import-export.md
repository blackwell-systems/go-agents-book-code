# CSV import and export

shortlink reads and writes links as CSV, since v1.2.0. Both commands use the configured database (the `database` setting) and run on the server.

## Importing links

```sh
shortlink links import links.csv
```

The file needs a header row with the columns `url`, `slug`, `expires_at`. `slug` and `expires_at` may be empty, in which case a short code is generated and the link never expires. `expires_at` takes a time such as `2027-01-01T00:00:00Z`.

```csv
url,slug,expires_at
https://example.com/a/long/path,,
https://example.com/summer-sale,summer,2027-01-01T00:00:00Z
```

A slug is 3 to 32 characters of letters, digits, and hyphens. The codes `api`, `admin`, `healthz`, and `metrics` are reserved.

Rows are checked like API requests: targets must be `http` or `https` URLs no longer than `max_url_length` (2048 bytes), and a target on a domain listed in `blocked_domains` is refused. A bad row is reported with its line number and skipped; the rest of the file is imported.

If you see "database is locked", another process is using the same file. Run one process per database file (see troubleshooting.md).

## Exporting links

```sh
shortlink links export > links.csv
```

`shortlink links export` writes CSV to standard output, with a header row and the columns `code`, `url`, `expires_at`, `created_at`, `total_clicks` for every link:

```csv
code,url,expires_at,created_at,total_clicks
x7Kp2,https://example.com/a/long/path,,2026-05-14T10:02:00Z,1841
aB3-kL,https://example.com/summer-sale,2027-01-01T00:00:00Z,2026-05-20T09:15:00Z,42
```

`total_clicks` comes from the database, so a click counted in the last `clicks.flush_interval` (5 seconds) may not be in the output yet.

## Moving links between servers

Export on the old server and import on the new one:

```sh
# old server
shortlink links export > links.csv

# new server
shortlink links import links.csv
```

The export and import columns are not the same. Import takes `url`, `slug`, `expires_at`, while export writes `code`, `url`, `expires_at`, `created_at`, `total_clicks`. To move links as they are, rename `code` to `slug` and drop `created_at` and `total_clicks`:

```csv
url,slug,expires_at
https://example.com/a/long/path,x7Kp2,
https://example.com/summer-sale,aB3-kL,2027-01-01T00:00:00Z
```

Keeping the same slug keeps the same short code on the new server. Check these before importing:

- `base_url` on the new server sets the start of every short URL. If it differs from the old server, the links keep their codes but the host changes.
- A code already in use on the new server is a bad row: it is reported with its line number and skipped. Delete the existing link first if you want the imported one (see docs/api.md and the `shortlink links` commands).
- Click counts are not carried over. `total_clicks` starts over on the new server, and the export file has no daily counts to import.

Back up the database on both servers before and after a move; see deploy.md.
