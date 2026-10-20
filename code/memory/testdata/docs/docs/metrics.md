# Metrics

shortlink serves Prometheus metrics on a separate port. Set that port with
metrics_addr (:9090 by default):

```yaml
metrics_addr: ":9090"
```

The endpoint is /metrics on metrics_addr. It is on its own port so that
metrics stay off the public port where redirects, the API, and /admin are
served. Point Prometheus at the metrics port only; do not expose it through
the reverse proxy that fronts the public port. Set metrics_addr to an empty
value to turn metrics off.

Override the port with an environment variable if needed:

```sh
METRICS_ADDR=":9191" shortlink serve
```

## Metric names

| Metric | Type | What it counts |
| --- | --- | --- |
| shortlink_redirects_total{status} | counter | Redirects served, by response status |
| shortlink_api_requests_total{method,code} | counter | API requests, by HTTP method and response code |
| shortlink_rate_limited_total | counter | Requests rejected because the client was over its rate limit |
| shortlink_cache_hits_total | counter | Link lookups answered from the in-memory cache |
| shortlink_cache_misses_total | counter | Link lookups that reached the database |
| shortlink_links | gauge | Links currently stored |
| shortlink_webhook_failures_total | counter | Webhook events dropped after their last retry failed |

Cache metrics only move when the cache is on (cache_size above 0). With the
cache off, lookups always go to the database and these counters stay flat.
shortlink_webhook_failures_total goes up once per event that has used all 5
delivery attempts (see docs/webhooks.md).

## Example queries

Rate of rate-limited requests over the last 5 minutes:

```promql
rate(shortlink_rate_limited_total[5m])
```

Cache hit ratio over the last 5 minutes. It is 1 when every lookup is served
from the cache:

```promql
rate(shortlink_cache_hits_total[5m])
  /
(rate(shortlink_cache_hits_total[5m]) + rate(shortlink_cache_misses_total[5m]))
```

Redirects per second, by status:

```promql
sum by (status) (rate(shortlink_redirects_total[5m]))
```

API errors (non-2xx responses) per second:

```promql
sum(rate(shortlink_api_requests_total{code!~"2.."}[5m]))
```

## Notes

shortlink_links is a gauge, not a counter: it rises and falls as links are
created, deleted, or expired, so use `shortlink_links` directly rather than
`rate()`.

For the request logs behind these numbers, see docs/logging.md; each request line carries method, path, status, and duration_ms. For
deployment details such as reverse proxies and backups, see docs/deploy.md.
