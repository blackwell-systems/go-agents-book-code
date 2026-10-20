# Performance

## Measured redirect rates

On a 2-vCPU virtual machine, shortlink serves about 9,000 redirects a second with the cache on and about 2,500 with it off. The numbers are a single-instance measurement on SQLite, not a benchmark of your hardware.

## The link cache

shortlink keeps recently used links in memory so a redirect does not need a database read. The cache holds `cache_size` links (10,000 by default) and drops the least recently used entry when it is full. Set `cache_size` to 0 to turn the cache off.

```yaml
cache_size: 10000
```

Or with an environment variable:

```sh
CACHE_SIZE=0 shortlink serve
```

Watch how the cache behaves with `shortlink_cache_hits_total` and `shortlink_cache_misses_total` on `metrics_addr` (see metrics.md).

## Database size

SQLite has been tested to 10 million links. Use the settings in config/defaults.yaml as a starting point and measure on your own hardware before relying on any rate here.

## Click counting

Each redirect counts one click. Counts are held in memory and written to the database every `clicks.flush_interval` (5 seconds by default):

```yaml
clicks:
  flush_interval: 5s
  retention_days: 400
```

This batching has two consequences:

- **Freshness**: a new click shows up in the API within about 5 seconds of the redirect. Raise or lower `clicks.flush_interval` to trade write frequency against how stale the counts look.
- **Crashes**: a crash can lose up to one flush interval of counts. A normal shutdown writes them first, so stopping the server with SIGTERM keeps every count it has taken.

Daily click counts are kept for `clicks.retention_days` (400 by default).

## Timeouts

Three timeouts bound how long requests can take. Tune them in config/defaults.yaml or by environment variable:

| Setting | Default | Meaning |
| --- | --- | --- |
| read_timeout | 5s | Longest time to read a request |
| write_timeout | 10s | Longest time to write a response |
| shutdown_timeout | 15s | Time allowed for requests in flight when stopping |

```yaml
read_timeout: 5s
write_timeout: 10s
shutdown_timeout: 15s
```

```sh
SHUTDOWN_TIMEOUT=30s shortlink serve
```

`shutdown_timeout` applies when the server stops. shortlink waits up to this long for in-flight requests to finish and writes out counted clicks before exiting. Give it longer than the slowest response you expect to serve.

## Where to look next

- rate-limits.md covers per-client request limits.
- metrics.md lists the Prometheus metrics available on `metrics_addr`.
- troubleshooting.md covers rate limits that hit everyone at once and the "database is locked" error.
