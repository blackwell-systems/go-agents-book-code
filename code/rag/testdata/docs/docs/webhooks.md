# Webhooks

Since v1.3.0, shortlink can notify an HTTP endpoint when a link changes.

## Turning webhooks on

Set `webhooks.url` and `webhooks.secret` in config/defaults.yaml, or the
environment variables of the same name in capitals:

```yaml
webhooks:
  url: https://ops.example/hooks/shortlink
  secret: 6f1c2f4d0a8b4e1c9f0a3b7c5d2e8f11
```

```sh
WEBHOOKS_URL=https://ops.example/hooks/shortlink
WEBHOOKS_SECRET=6f1c2f4d0a8b4e1c9f0a3b7c5d2e8f11
```

An empty `webhooks.url` turns webhooks off. The same secret is used to
sign every delivery, so keep it in sync with your receiver.

## Events

shortlink POSTs a JSON body for three events:

| Event | Sent when |
| --- | --- |
| `link.created` | A link is created |
| `link.deleted` | A link is deleted |
| `link.expired` | A link passes its `expires_at` time |

A delivery for `link.expired` looks like this:

```json
{
  "event": "link.expired",
  "code": "x7Kp2",
  "url": "https://example.com/a/long/path",
  "expires_at": "2027-01-01T00:00:00Z"
}
```

Treat any 2xx response as accepted. A delivery that does not get a 2xx
within 10 seconds is treated as failed.

## Verifying a signature

Each request carries the header `X-Shortlink-Signature: sha256=<hex>`,
an HMAC-SHA256 of the raw request body keyed with `webhooks.secret`. To
verify it:

1. Read the raw body bytes first, before parsing them as JSON.
2. Compute HMAC-SHA256 over those bytes with `webhooks.secret`.
3. Compare the hex digest with the part of the header after `sha256=`,
   using a constant-time comparison. Reject the request if they differ.

```go
func check(body []byte, header, secret string) bool {
	want, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	got := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(got), []byte(want))
}
```

## Retries

A failed delivery is retried, up to 5 attempts in all. The waits
between attempts are 1s, 2s, 4s, and 8s.

After the last failure shortlink logs the event and drops it. It is not
kept for later delivery. Each dropped event increments the counter
`shortlink_webhook_failures_total` on the metrics port
(metrics_addr, `:9090` by default); see metrics.md for the full list.
