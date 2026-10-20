# Rate limits

Each client, identified by its IP address, may make
rate_limit.requests_per_minute requests a minute (120 by default), plus
a short burst of rate_limit.burst more (20 by default).

A client over its limit gets 429 Too Many Requests, with a Retry-After
header giving the number of seconds to wait. Redirects count against
the limit too, so a link shared widely from one office network can
exhaust that network's limit.
