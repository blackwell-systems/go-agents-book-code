# Logging

shortlink writes one log line per request. Since v1.1.0 the default is
JSON (`log_format` json); earlier logs were text. Both formats carry
the same fields.

## Settings

| Setting | Default | Meaning |
| --- | --- | --- |
| log_format | json | `json` or `text` |
| log_level | info | `debug`, `info`, `warn`, or `error` |

Set them in config/defaults.yaml or override with environment
variables: `LOG_FORMAT=text`, `LOG_LEVEL=debug`.

## Request log fields

Each request line has these fields:

| Field | Meaning |
| --- | --- |
| method | HTTP method of the request |
| path | Request path |
| status | Response status code |
| duration_ms | Time taken to answer, in milliseconds |
| client_ip | Client address, from X-Forwarded-For when the request comes from a trusted proxy (see deploy.md) |
| request_id | ID of the request, see below |

A JSON line looks like this:

```json
{"method":"GET","path":"/x7Kp2","status":302,"duration_ms":2,"client_ip":"203.0.113.9","request_id":"a1b2c3d4"}
```

The text format prints the same fields on one line, for reading in a
terminal when you set `LOG_FORMAT=text`.

## Request IDs

Every request has a request ID. If the client sends an X-Request-ID
header, shortlink uses its value as the request_id in the log line. If
there is no such header, shortlink makes one and returns it in the
X-Request-ID response header, so the value is in the log either way.

Pass an ID you generate yourself when you want to follow one request
through a proxy, shortlink, and anything else that logs it. The same
ID appears in shortlink's log line and in the header you sent or
received.

## Levels

`log_level` sets the lowest level logged: `debug`, `info`, `warn`, or
`error`. Request lines are logged at `info`, so with the default
`log_level` info every request is logged. Set `LOG_LEVEL=warn` to log
warnings and errors only.

## Related

- Metrics live on a separate port, metrics_addr (`:9090` by default);
  see docs/metrics.md.
- Webhook delivery failures are logged and counted in
  `shortlink_webhook_failures_total` (see webhooks.md).
- Shutdown behavior is covered in docs/performance.md: a normal stop
  writes pending click counts before the process exits.
