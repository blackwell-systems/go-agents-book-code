# remind

A small reminders service: users create one-off and recurring reminders
over a JSON API, and remind delivers them by email or webhook.

## Running

```sh
go run ./cmd/remind -config remind.json
```

The configuration file is JSON (see `config.Default` for every setting); a missing file means the defaults. Environment variables such as `REMIND_ADDR` and `REMIND_WEBHOOK_SECRET` override it. `users_file` names a JSON file of users to start with (`ID`, `Name`, `Email`, `Token`, `Plan`, `Admin`).

## API

Every `/v1` route takes `Authorization: Bearer <token>`.

| Route | What it does |
| --- | --- |
| `POST /v1/reminders` | create: `text` plus one of `in` ("1h30m"), `at` (RFC 3339), or `every` ("every 2h", "on mon,wed at 09:30", with optional `tz`); optional `priority` 1 (high) to 3 (low) |
| `GET /v1/reminders` | list, with `offset` and `limit` |
| `GET`, `DELETE /v1/reminders/{id}` | read or delete one |
| `POST /v1/reminders/{id}/done` | mark done |
| `POST /v1/reminders/{id}/snooze` | move it later: `{"for": "10m"}` |
| `POST /v1/reminders/{id}/test` | send it now |
| `GET /v1/me`, `PUT /v1/me/prefs` | your account and notification settings (`tz`, `channel`, `webhook`, and quiet hours as `quiet_from` and `quiet_to`, such as "22:00" and "07:00") |
| `DELETE /v1/me` | close your account: deletes your reminders and the lists you own, and takes you off other lists |
| `GET /v1/me/digest` | the next `days` (default 7) of reminders, day by day |
| `GET /v1/me/export`, `GET /v1/me/stats` | your reminders as a file, and counts |
| `POST /v1/admin/users/import` | administrators: add users from the old system |
| `DELETE /v1/admin/users/{id}` | administrators: remove a user |
| `POST /v1/admin/dispatcher/stop` | administrators: stop deliveries |
| `POST /v1/admin/snapshot` | administrators: write a snapshot of the store |
| `GET /v1/admin/dispatcher/status` | administrators: delivery status |
| `GET /v1/admin/audit` | administrators: recent administrator and sharing actions |
| `GET /v1/me/calendar.ics` | your reminders as a calendar feed |
| `GET /v1/search?q=` | search your reminders |
| `POST /v1/lists`, `GET /v1/lists`, `GET`, `DELETE /v1/lists/{id}` | shared lists |
| `POST /v1/lists/{id}/members`, `DELETE /v1/lists/{id}/members/{user}` | share a list (`role`: viewer, editor) |
| `POST /v1/lists/{id}/reminders`, `DELETE /v1/lists/{id}/reminders/{reminder}` | put reminders on a list |
| `GET /metrics` | Prometheus metrics |

## Layout

| Package | What |
| --- | --- |
| `duration` | parses "90s", "1h30m", "2d", "1w2d" |
| `schedule` | recurring rules: intervals and weekly times in a zone |
| `store` | users and reminders, in memory |
| `auth` | bearer-token middleware |
| `notify` | delivery, previews, and digests |
| `api` | HTTP handlers |
| `crash` | crash reports: recovered panics and stuck requests |
| `server` | wires the parts together |
| `share` | shared lists and their permissions |
| `search` | full-text search over reminders |
| `calendar` | iCalendar export |
| `webhook` | signed webhook deliveries with retries |
| `i18n` | notification text in several languages |
| `quota` | plans, reminder quotas, and rate limits |
| `audit` | the audit log |
| `metrics` | Prometheus metrics |
| `config` | the configuration file and environment |
| `textutil` | small text helpers |

## Crash reports

Panics in handlers are recovered and appended to the crash file as JSON
lines, with the stack, the request, and the last log lines. A request in
flight for more than 30 seconds produces a report with every goroutine's
stack.

## Tests

```sh
go test ./...
```
