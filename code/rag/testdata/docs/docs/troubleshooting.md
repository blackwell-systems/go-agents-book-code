# Troubleshooting

## A new link returns 404

If the link was created with an expires_at time that has already
passed, it is expired from the moment it is created, and its short URL
returns 404. Check the expires_at value, including its time zone.

## Every user hits the rate limit at once

shortlink is probably behind a proxy and sees every request as coming
from the proxy's address. Set trusted_proxies; see deploy.md.

## "database is locked"

Two shortlink processes are using the same SQLite file. Run one process
per database file.
