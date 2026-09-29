# resy-snipe

A Go CLI that checks Resy for availability at a target venue/date/party size, then attempts to book matching time + seating/table-type options at a scheduled "snipe" time. It supports both flag-driven usage and an interactive prompt mode.

Important note (ethics / ToS): This tool automates interactions with Resy endpoints and may violate Resy's terms, rate limits, or acceptable-use policies. Use responsibly, keep request volume low, and only for accounts/venues you're authorized to book.

## Features
- CLI flags for date, party size, venue ID, desired reservation times, seating/table types, and the snipe schedule.
- Interactive mode (`-interactive`) that prompts you through all inputs (with defaults).
- Time + table-type matching: provide one or more reservation times and (optionally) one or more seating/table types; the tool builds a list of candidate combinations.
- Reservation discovery loop with a short retry interval (10ms between attempts).
- Concurrent booking attempts: if multiple candidate config tokens are found, the workflow tries booking them concurrently.

## Demo
![Demo](demo.gif)

## Requirements
- Go 1.20+
- A Resy account with valid tokens:
  - `RESY_API_KEY`
  - `RESY_AUTH_TOKEN`

## Setup
1) Export credentials:
```bash
export RESY_API_KEY="your_api_key"
export RESY_AUTH_TOKEN="your_auth_token"
```

2) Run from the repo root:
```bash
go run .
```

If env vars are missing/empty, requests will fail because the API client reads keys directly from env vars at startup.

## Default behavior
If you run with no flags:
- Reservation date defaults to 7 days from now in America/New_York.
- Party size defaults to 2.
- Venue defaults to Dead Rabbit.
- Reservation times default to whatever is in `ResTimeTypes` (currently `18:45:00`).
- Snipe time defaults to `00:00` (midnight).

## CLI usage
### Flags
- `-interactive` Prompt for settings interactively.
- `-date` Reservation date (YYYY-MM-DD).
- `-party-size` Party size.
- `-venue-id` Resy venue ID.
- `-res-times` One or more desired times, comma-separated. Times are normalized to `HH:MM:SS`.
- `-table-types` Optional seating/table types, comma-separated. Use `none` for any.
- `-snipe-date` Date to perform the snipe (defaults to reservation date).
- `-snipe-time` Time to perform the snipe (24h clock).
- `-retry` How long to keep retrying the availability search (default `60s`). Accepts Go durations such as `30s`, `2m`.
- `-targets` Path to a JSON plan file describing multiple venue/date targets (see Batch plans).
- `-decision-window` With `-targets`, how long to hold for a higher-priority venue once something becomes available (default `2s`).
- `-validate` Check a `-targets` plan file and exit. Offline: no credentials, no network.
- `-dry-run` Report what each venue is currently offering and exit. Books nothing and ignores the snipe schedule.
- `-watch` Poll targets continuously and act on each target's `on_hit` policy. Ctrl-C to stop.
- `-poll-interval` How often `-watch` polls (default `60s`). Below ~30s risks 429 rate limiting.
- `-max-bookings` Cap on total auto-bookings during `-watch` (default `1`, `0` = unlimited).
- `-alert-log` Append-only JSON log of alerts and bookings (default `logs/alerts.jsonl`).
- `-notify` Join Resy's Notify waitlist for targets with `"notify": true`, then exit.
- `-find-venue` Look up venue IDs by name and exit.

### Examples
Run at midnight tonight (local time), booking 7 days out (default date), Dead Rabbit, party of 2:
```bash
go run . -snipe-time 00:00
```

Book a specific venue and date at 09:00, try multiple times:
```bash
go run . \
  -date 2026-01-22 \
  -party-size 2 \
  -venue-id 466 \
  -res-times "18:30,18:45,19:00" \
  -table-types "Parlor,Bar" \
  -snipe-date 2026-01-15 \
  -snipe-time 09:00
```

Interactive prompt mode:
```bash
go run . -interactive
```

## Batch plans (multiple venues and nights)
A plan file snipes several venue/date combinations in one run:

```bash
go run . -validate -targets targets.json   # check the file offline
go run . -dry-run  -targets targets.json   # check availability, book nothing
go run . -targets targets.json             # run for real
```

Work up through those three: `-validate` needs neither credentials nor network, `-dry-run` proves the credentials and venue IDs work without booking anything, and only the third can create a reservation.

`-dry-run` prints every slot the venue is offering, not just the ones you asked for, so it doubles as a way to discover which times and table types a venue actually returns:

```
Don Angie (1505) 2026-09-14
  14 slot(s) offered across 7 start time(s):
    17:45:00  Dining Room
    18:00:00  Bar, Dining Room
    ...
  matching your requested times: 3
```

### How competing targets are resolved
Resy only allows one reservation per night, so targets are grouped by date:

- **Within a night**, every target's availability is searched *concurrently* (searching is a read-only call), then booking happens *strictly in priority order* and stops at the first success. `priority: 1` is the most wanted. The losers are reported as `skipped`.
- **Across nights**, groups are independent and run in parallel, so each night can land its own reservation.

This covers both shapes: several venues competing for one night, and one venue across many nights.

### The decision window
If a lower-priority venue reports availability first, the group waits `decision_window` for a higher priority to land before committing. Set it to `0` to always grab the first thing available. Longer windows favour getting the venue you actually want; shorter ones favour getting *something* before the slot is taken.

### Plan format
Priorities must be distinct within a night (the loader rejects ties, since a tie has no defined winner). Omit `priority` entirely to fall back to file order. Copy `targets.example.json` to start.

```json
{
  "snipe_date": "2026-08-16",
  "snipe_time": "00:00",
  "retry": "90s",
  "decision_window": "3s",
  "targets": [
    {"name": "Don Angie", "venue_id": 1505, "date": "2026-09-14",
     "party_size": 2, "res_times": ["19:00", "19:30"], "table_types": [], "priority": 1}
  ]
}
```

`snipe_date` + `snipe_time` are when the process wakes up and fires; the per-target `date` is the night being booked. All targets in one file share a single snipe time — venues that drop at different times need separate plan files.

## Watch mode (continuous monitoring)
A snipe fires once at a scheduled drop. Watch mode instead polls indefinitely, which is what you want for cancellations that appear at random:

```bash
go run . -watch -targets targets.september.json
```

Each target chooses what happens on a hit via `on_hit`:

- `"book"` (default) — reserve it automatically. Fastest, but spends a real reservation without you in the loop.
- `"alert"` — notify you and do nothing else. You book manually.

That lets one plan auto-book the restaurant you're sure about while only alerting on the others.

### Guardrails
Watching many nights with `on_hit: "book"` would otherwise reserve **one table per night**. Three things prevent that:

- **`max_bookings`** (default `1`) caps total auto-bookings for the whole watch. Once reached, auto-booking stops but alert-only targets keep working.
- **One reservation per night** — booking any target for a date stops all targets for that date.
- Both are enforced by claiming a slot *before* booking, so concurrent polls cannot slip past the cap.

### Alerts
Alerts are **edge-triggered**: a target fires when it goes from unavailable to available, not on every poll, so an open table does not generate an alert per minute.

Three sinks, and a failing one never blocks the others:
- **Terminal** — always on, with a bell.
- **Log file** — one JSON object per line at `-alert-log`, for auditing what ran unattended.
- **Email** — opt-in via `ALERT_EMAIL_TO`. Gmail requires an App Password in `SMTP_PASS`, not your account password. See `.env.example`.

The two alert kinds mean different things: `TABLE OPEN` is actionable (go book it), while `BOOKED` is confirmation after the fact plus the reservation token.

## Resy Notify waitlist
Separately from polling, targets with `"notify": true` can join Resy's own Notify waitlist so Resy alerts you directly:

```bash
go run . -notify -targets targets.september.json
```

**Unverified:** the Notify request shape is inferred from Resy's booking widget and has not been confirmed against a live account. The command prints the raw response for every target so a rejection can be diagnosed and the payload in `resy/resy-api.go` corrected. Treat the local watcher as the reliable path until this is confirmed working.

## Venue IDs
Resolve a venue ID by name:

```bash
go run . -find-venue "4 Charles"
```

The CLI also prints a venue menu in interactive mode based on constants in config (you can still enter a custom venue ID).

Current built-ins:
- Dead Rabbit (`38660`)
- Rubirosa (`466`)
- Red Pearl (`69820`)
- Rafs (`65679`)
- Carbone (`6194`)
- Don Angie (`1505`)
- San Sabino (`78799`)
- Gertrudes (`71935`)
- Au Cheval (`5769`)
- HOWOO (`86696`)

To add more, extend the constants in `config/resy-config.go` and the `venueOptions` list in `resy-bot.go`.

## Project layout
- `resy-bot.go`: main CLI entrypoint (flags, interactive prompts, schedule, run workflow).
- `config/resy-config.go`: environment-based auth keys, venue constants, default reservation details.
- `config/targets.go`: plan file parsing and validation, reservation-time normalization.
- `resy/resy-multi-target.go`: multi-venue/multi-night orchestration with per-night priority.
- `resy/resy-venue-search.go`: venue ID lookup by name.
- `resy/resy-api.go`: low-level HTTP requests to Resy endpoints (`/find`, `/details`, `/book`) plus headers.
- `resy/resy-client.go`: parses find results into candidate booking config tokens, fetches booking details, books reservations.
- `resy/resy-booking-workflow.go`: orchestration: find -> details -> book.

## How it works
1) Scheduling
- The app computes a target `scheduledTime` from `-snipe-date` + `-snipe-time`, prints the sleep duration, and sleeps until that time.
- If the scheduled time is in the past, the computed duration will be negative and the sleep will effectively be skipped.

2) "Find" availability
- At runtime, the workflow calls Resy "find" (`/4/find`) with date, party size, and venue.
- The response is parsed into a structure mapping `start time -> table type -> config token`.
- The client compares available slots against your requested reservation time/type list, building a queue of matching config tokens.

3) "Details" and "Book"
- For each candidate config token:
  - Call `/3/details` to fetch `payment_method_id` and `book_token`.
  - Call `/3/book` with `book_token` and the payment method payload.

4) Concurrency model
- If multiple config tokens match, the workflow spins a goroutine per token and attempts booking concurrently.
- The first booking to succeed wins; its token is returned and later successes are discarded. All shared state is guarded by a mutex.
- If every attempt fails, the errors are combined with `errors.Join` and returned together.

## Configuration
Edit `config/resy-config.go` to change defaults:
- Default reservation date/time options: `ResTimeTypes`
- Default venue / party size / date: `ReservationDetailss`
- Default snipe time: `SnipeTimee`

## Retry tuning
- The retry loop sleeps 10ms between attempts.
- The retry window is controlled by `-retry` and defaults to `60s`. It is threaded through `Run(retryFor)` into `findReservations`.

Pass any Go duration string, for example `-retry 2m` to keep hunting for two minutes, or `-retry 0` to make a single attempt.

## Troubleshooting
### "No Hits"
The tool prints `No Hits` when it finds no matching slots for your requested time(s)/table type(s).

Common causes:
- Too narrow reservation times (try a wider range).
- Table types don't match the venue's returned types. A requested table type the venue never offered is treated as a miss, so the search keeps retrying instead of queueing an empty token.
- Retry window is too short (see Retry tuning above); raise it with `-retry`.

### Expired auth token (419 / "Unauthorized")
`RESY_AUTH_TOKEN` is a JWT and expires every few months. The tool decodes its `exp` claim locally and refuses to start once it has lapsed, so you get a clear error instead of a 419 at the drop. It also checks the token against the *scheduled* snipe time, catching a token that works now but expires before the run fires.

To refresh: log in at resy.com, open DevTools → Network, click any restaurant, and copy the `x-resy-auth-token` request header from a call to `api.resy.com`.

`RESY_API_KEY` is long-lived and rarely needs changing.

### HTTP errors (401/403/429)
- 401/403 typically indicates invalid/expired tokens or missing env vars. Tokens are read from `RESY_API_KEY` and `RESY_AUTH_TOKEN`.
- 429 indicates rate limiting. Reduce request rate and widen your retry interval.

When non-OK responses occur, the code reads and prints response body details and returns an error.

### Negative "Sleeping for ..."
If your `-snipe-date`/`-snipe-time` is in the past, the computed duration will be negative. The tool will proceed immediately because `time.Sleep(duration)` effectively does not delay.

## Known limitations
- Assumes at least one payment method: `getReservationDetails` uses the first payment method in the array without checking length.
- One snipe time per plan file. Venues with different drop schedules need separate plan files and separate runs.
- `-find-venue` targets `/3/venuesearch/search`; if Resy changes that endpoint's shape the command prints the raw response so it can be re-mapped.
- Duplicate-booking guard is best-effort: a goroutine checks whether another slot already booked before it starts, but attempts already in flight are not cancelled. If two slots are booked simultaneously you may end up with two reservations.
- `nil` table type ("any") picks an arbitrary table type via map iteration order, so the choice is not deterministic.

## Security tips
- Never commit your `RESY_API_KEY` / `RESY_AUTH_TOKEN`. The app is already designed to read them from environment variables.
- Consider using a local `.env` file + a loader (dotenv) for convenience (but keep it out of git).

## Development ideas
- Cancel in-flight booking attempts once one succeeds (thread a `context.Context` through `ResyAPI`) to close the duplicate-booking window.
- Add structured logging (request IDs, status codes, elapsed time).
- Add a "dry run" mode: find + details without booking.
- Add backoff and jitter to respect rate limits.
- Add deterministic selection (prefer earliest time, prefer specific table type, stop after first success).
