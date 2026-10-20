# Configuration

shortlink reads config/defaults.yaml at startup. Any setting can be
overridden with an environment variable of the same name in capitals:
listen_addr becomes LISTEN_ADDR, and dots become underscores, so
rate_limit.burst becomes RATE_LIMIT_BURST.
Run `shortlink check-config` to load the configuration and report any
errors without starting the server.

| Setting | Default | Meaning |
| --- | --- | --- |
| listen_addr | :8080 | Address for redirects, the API, and /admin |
| metrics_addr | :9090 | Address for /metrics; empty turns metrics off |
| database | shortlink.db | The SQLite file |
| base_url | http://localhost:8080 | The start of every short URL |
| code_length | 5 | Length of generated short codes |
| redirect_status | 302 | 302 (temporary) or 301 (permanent) |
| append_query | true | Append the short URL's query string to the target |
| max_url_length | 2048 | Longest target URL accepted, in bytes |
| blocked_domains | (none) | Target domains refused when a link is created |
| cache_size | 10000 | Links kept in the in-memory cache; 0 turns it off |
| rate_limit.requests_per_minute | 120 | Requests allowed per client per minute |
| rate_limit.burst | 20 | Extra requests allowed in a short burst |
| trusted_proxies | (none) | Proxy addresses whose X-Forwarded-For header is believed |
| clicks.retention_days | 400 | How long daily click counts are kept |
| clicks.flush_interval | 5s | How often counted clicks are written to the database |
| webhooks.url | (none) | Where webhook events are sent; empty turns them off |
| webhooks.secret | (none) | Key for signing webhook bodies |
| admin.enabled | false | Serve the admin page at /admin |
| admin.password_hash | (none) | Set by `shortlink admin set-password` |
| log_format | json | json or text |
| log_level | info | debug, info, warn, or error |
| read_timeout | 5s | Longest time to read a request |
| write_timeout | 10s | Longest time to write a response |
| shutdown_timeout | 15s | Time allowed for requests in flight when stopping |
