# Security

## TLS at the proxy

shortlink serves plain HTTP. Terminate TLS at a reverse proxy such as nginx or Caddy and point clients at the proxy. See deploy.md for the proxy setup.

When shortlink runs behind a proxy, every request appears to come from the proxy's address unless you tell it otherwise. List the proxy's address in trusted_proxies; shortlink then takes the client's address from the X-Forwarded-For header the proxy adds. That address is what rate limiting and request logs use, so only trust proxies you control.

## API keys

Create keys only on the server, with the narrowest scope that works:

```sh
shortlink keys create --name ci --scope write
```

A key starts with `sl_` and is shown once, at creation. The server stores only its SHA-256 hash, so a lost key cannot be recovered; create a new one and revoke the old one with `shortlink keys revoke <id>`. Treat keys like passwords: pass them in the Authorization header and keep them out of source control and shell history.

Scopes are read (GET requests), write (also create and delete links), and admin (also read every link's clicks across the server). A revoked key is rejected with 401 from the next request on.

The admin password is separate from API keys. Set it with `shortlink admin set-password`, which stores its hash; a login lasts 12 hours.

## Target URLs

Targets must be http or https URLs no longer than max_url_length (2048 bytes). Refuse whole domains when you create links by listing them in blocked_domains; a link to one of those domains is refused with 400 and the error "domain blocked". The check runs when a link is created, so review blocked_domains before importing links in bulk with `shortlink links import`.

The codes api, admin, healthz, and metrics are reserved and can't be used as slugs. A slug already in use gets 409 Conflict.

With redirect_status 302 (the default), deleting a link or letting it expire stops the redirect at once. With 301, browsers cache the target and keep going there even after the link is deleted or expires. Use 301 only for links that never change.

## Metrics and the admin page

Prometheus metrics are served on metrics_addr (`:9090` by default), on a separate port from listen_addr. Keep that port off the public internet: bind it to a private interface or block it at the firewall. Set metrics_addr to an empty value to turn metrics off.

The admin page is off by default (admin.enabled false). It is read-only, but it shows links and their daily clicks, so put it behind the proxy's access controls as well as its own password. It is served on listen_addr under /admin.

## Webhook signing

If you use webhooks, set webhooks.secret and verify each delivery at the receiver. Every request carries `X-Shortlink-Signature: sha256=<hex>`, an HMAC-SHA256 of the raw body keyed with webhooks.secret. Compute the same HMAC over the body you received and compare it to the header. See docs/webhooks.md for the event payloads.

## Reporting vulnerabilities

Report vulnerabilities to security@shortlink.example, not in public issues. Expect a reply within 3 working days.
