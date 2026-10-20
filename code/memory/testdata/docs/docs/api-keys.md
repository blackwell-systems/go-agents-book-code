# API keys

API keys are the only credentials the JSON API accepts. They are made and managed with the `shortlink keys` commands on the server; there is no API endpoint for creating them.

## Creating a key

```sh
shortlink keys create --name ci-bot --scope write
```

The name is yours to choose and is only for recognising the key later in `shortlink keys list`. The scope is one of `read`, `write`, or `admin`.

The command prints the key once, at creation. Copy it then; the server never shows it again.

## What a key looks like

A key starts with `sl_` and continues with its random characters. Send it in the Authorization header as a bearer token:

```text
Authorization: Bearer sl_...
```

The server stores only the SHA-256 hash of the key. If you lose a key, create a new one and revoke the old one.

## Scopes

| Scope | Allows |
| --- | --- |
| read | GET requests |
| write | GET requests, plus creating and deleting links |
| admin | Everything write allows, plus reading every link's clicks across the server |

Pick the narrowest scope that fits. A key used from CI that only creates links does not need `admin`.

## Listing keys

```sh
shortlink keys list
```

This shows each key's id, name, and scope, not the key itself.

## Revoking a key

```sh
shortlink keys revoke 3
```

The argument is the key's id, as printed by `shortlink keys list`. A revoked key is rejected with 401 from the next request on. Revoke a key as soon as it is no longer needed, and immediately if you think it has been copied.

## When a request is rejected

A request with a missing, wrong, or revoked key gets `401 Unauthorized`. A valid key whose scope doesn't allow the request gets `403 Forbidden`: a key with `read` scope gets 403 on a request to create or delete a link, and a key with `write` scope gets 403 on a request for every link's clicks. Either way the JSON body has an `error` field, as described in docs/api.md.

A `429` is not a key problem; it means the client is over its rate limit. See rate-limits.md.
