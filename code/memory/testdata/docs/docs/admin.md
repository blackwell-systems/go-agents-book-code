# The admin page

The admin page is a read-only view of the links in the database. It is
off by default and lives at /admin on `listen_addr` (`:8080` by
default). It arrived in v1.4.0.

## Turning it on

Set `admin.enabled` in config/defaults.yaml, or override it with an
environment variable of the same name in capitals:

```yaml
admin:
  enabled: true
```

```sh
ADMIN_ENABLED=true shortlink serve
```

Run `shortlink check-config` to confirm the configuration loads.

## Setting the password

`admin.password_hash` is not written by hand. Run this on the server
and enter the password at the prompt:

```sh
shortlink admin set-password
```

The command stores the hash in `admin.password_hash`. shortlink keeps
only the hash, so the password cannot be recovered from the
configuration. Run the command again to change the password.

## What it shows

Everything on the page is read-only. You can:

- Search links.
- See each link's daily click counts.

You cannot create, edit, or delete links there. To change links, use
the JSON API or the `shortlink links` commands; see api.md. Daily click
counts are kept for `clicks.retention_days` (400 by default), so the
page does not show counts older than that.

## Logging in

Sign in with the password set by `shortlink admin set-password`. A login
lasts 12 hours, after which you sign in again.

## Protecting /admin

The page is served on `listen_addr` under /admin, the same port as
redirects and the API, so it is reachable wherever that port is
reachable. Put it behind the access controls of your reverse proxy as
well, for example HTTP basic auth or an IP allowlist in nginx or Caddy.
See deploy.md for the proxy setup.

If you also run shortlink behind a proxy that sets X-Forwarded-For,
list the proxy in `trusted_proxies` so rate limiting sees the real
client address.

## Example

```yaml
listen_addr: :8080
database: shortlink.db
admin:
  enabled: true
  password_hash: "$2a$10$..."   # set by `shortlink admin set-password`
```

Then start the server and open http://localhost:8080/admin.
