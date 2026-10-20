# Analytics

Since v1.4.0, shortlink counts clicks per link per day. Daily counts are
kept for clicks.retention_days days, 400 by default, and then deleted.
The total click count for a link is kept for as long as the link exists.

GET /api/links/{code}/clicks returns the daily counts that are still
kept, oldest first.
