# Changelog

## v1.4.0 (2026-09-15)

- Weekly schedules take a time zone (`tz`), defaulting to the user's.
- Import users from the old reminders system, keeping their API tokens.
- duration: reject a number without a unit, such as "1h30", instead of
  crashing.
- Digests: skip reminders past the last day instead of crashing.
- Requests stuck for 30 seconds now produce a crash report with every
  goroutine's stack.
- Export and stats endpoints.
- Shared lists: invite people to see or edit a list of reminders.
- Search over your reminders, and a calendar feed (`/v1/me/calendar.ics`).
- Webhook deliveries are signed (`X-Remind-Signature`).
- Plans with reminder quotas and per-user rate limits.
- Notifications in Portuguese, German, and Japanese.
- Prometheus metrics at `/metrics`, an audit log for administrators, and a
  configuration file (`-config`).

## v1.3.2 (2026-08-04)

- Webhook deliveries time out after 10 seconds.
- Snooze accepts days and weeks ("2d", "1w").

## v1.3.0 (2026-07-01)

- Recurring reminders ("every 2h", "on mon,wed at 09:30").
- Daily digests.
