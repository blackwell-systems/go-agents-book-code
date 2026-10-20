# Command line reference

Every command reads config/defaults.yaml. Any setting can be overridden by an
environment variable of the same name in capitals, with dots replaced by
underscores: `rate_limit.burst` becomes `RATE_LIMIT_BURST`.

## shortlink serve

Run the server. `shortlink` with no command does the same thing. It serves
redirects, the JSON API (see api.md), and, when `admin.enabled` is true, the
admin page at /admin. Metrics are served on metrics_addr, separate from
listen_addr.

```sh
shortlink serve
```

## shortlink check-config

Load the configuration, report errors, and exit. Use it after changing
config/defaults.yaml or setting environment variables.

```sh
shortlink check-config
```

## shortlink migrate

Apply database migrations to the file given by `database` and exit. Since
v1.3.0 migrations also run automatically when the server starts, so you only
need this when upgrading from v1.2.0 or earlier:

```sh
shortlink migrate
```

Back up the database first; see deploy.md.

## shortlink version

Print the version of the binary.

```sh
shortlink version
```

## shortlink links

### shortlink links add `<url>`

Create a link and print its short URL. `--slug name` sets a custom short code
(3 to 32 characters, letters, digits, and hyphens); a slug already in use is
refused with 409 Conflict. `--expires 2027-01-01T00:00:00Z` sets the expiry
time. The target must be an http or https URL no longer than
`max_url_length`, and its domain must not be in `blocked_domains`.

```sh
shortlink links add https://example.com/a/long/path --slug launch --expires 2027-01-01T00:00:00Z
```

### shortlink links list

List every link.

```sh
shortlink links list
```

### shortlink links delete `<code>`

Remove one link. Its short URL then returns 404.

```sh
shortlink links delete x7Kp2
```

### shortlink links import `<file.csv>`

Read a CSV file with a header row and the columns url, slug, expires_at; slug
and expires_at may be empty. Rows are checked like API requests. A bad row is
reported with its line number and skipped, and the rest are imported.

```sh
shortlink links import links.csv
```

### shortlink links export

Write CSV to standard output: a header row, then code, url, expires_at,
created_at, total_clicks for every link.

```sh
shortlink links export > links.csv
```

## shortlink keys

### shortlink keys create `--name <name> --scope read|write|admin`

Create an API key. The key starts with `sl_` and is shown once, at creation;
the server stores only its SHA-256 hash. Scopes are cumulative: `read` allows
GET requests, `write` also creates and deletes links, `admin` also reads every
link's clicks across the server.

```sh
shortlink keys create --name ci --scope write
```

### shortlink keys list

List keys with their names, ids, and scopes.

```sh
shortlink keys list
```

### shortlink keys revoke `<id>`

Revoke a key. It is rejected with 401 from the next request on.

```sh
shortlink keys revoke 3
```

## shortlink admin

### shortlink admin set-password

Prompt for the admin password and store its hash in `admin.password_hash`.
Set `admin.enabled` true to serve the page at /admin on listen_addr, and put
it behind the proxy's access controls as well (see deploy.md). A login lasts
12 hours. The page is read-only: search links and view each link's daily
clicks.

```sh
shortlink admin set-password
```

For webhooks, rate limiting, and click counting, see docs/webhooks.md,
docs/rate-limits.md, and docs/analytics.md.
