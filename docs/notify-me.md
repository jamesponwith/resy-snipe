# NotifyMeRelease strategy

> **Just want to run it?** [`notify-quickstart.md`](notify-quickstart.md)
> is the operational one-pager. This document is the architecture.

`NotifyMeRelease` is the fourth release strategy
([`release-strategies.md`](release-strategies.md)). It lets a snipe
sit dormant until an external alert fires for the target
(user, venue, date), then transitions straight into the booking race.
The intended use case is **catching cancellations and post-drop
openings without paying the anti-bot cost of polling `/4/find`**.

## Architecture

The engine reads from an `alerts.Source` ([`internal/alerts`](../internal/alerts/alerts.go)).
The Source is intentionally decoupled from the booking provider —
booking still goes through Resy, but the signal that says "an opening
exists" can come from anywhere.

```
  domain.NotifyMeRelease           internal/alerts
  ┌──────────────────┐             ┌────────────────────┐
  │ ProbeFrom        │             │ Source.Poll(req)   │◀── engine loop
  │ ProbeUntil       │             │ State{Fired,...}   │
  │ PollInterval     │ ───────────▶│ MinPollInterval()  │
  └──────────────────┘             │ Close()            │
                                   └─────────┬──────────┘
                                             │
                  ┌──────────────────────────┼──────────────────────────┐
                  ▼                          ▼                          ▼
          alerts.MemorySource     alerts/email.Source       (future: Resy API,
          (tests, webhook         (IMAP → parse → cache)     webhook receiver)
           in-memory)
```

v1's primary Source is `internal/alerts/email`: watch a mailbox for
Resy NotifyMe emails, parse out (venue, date), match against active
quests. **The IMAP loop and the email parser are stubbed** pending a
real-email sample — `Poll` currently returns `ErrSourceNotImplemented`.

## State machine

```
Scheduled  →  Discovering  →  Awaiting  →  Finding → Booking → Booked
   |              |               |
   |              | (alert fires) | (booking race wins)
   |              |
   |              | (window expires)
   |              ↓
   |           Failed (reason=notify_me_window_expired)
   |
   | (Source returns ErrEnrollmentRequired)
   ↓
Failed (reason=notify_me_enrollment_required)
```

`Discovering` carries `via=notify_me` on its event attrs to
distinguish it from a `DiscoveredRelease` discovering event.

## Two ways to run it

### Easiest: `scripts/notify-launch.sh`

```bash
scripts/notify-launch.sh                           # uses docs/examples/watch.toml
scripts/notify-launch.sh path/to/your-watch.toml   # custom config
```

The script builds the binary, prompts silently for the Gmail app
password if it's not already in the environment, stops any
previously-running watch, launches the new one under `nohup`, and
tails the log. Ctrl-C detaches; the watch keeps running.

**Cross-venue dedup**: if multiple `[[watches]]` entries share the
same `(user, date)`, only the first one to land an alert attempts to
book — the rest cancel cleanly with reason
`duplicate_already_booked`. Useful when 4 Charles and Torrisi both
fire for the same dinner night.

### Manual: `resy-snipe watch`

If you'd rather wire the steps yourself:

```bash
export RESY_SNIPE_IMAP_PASS='your-gmail-app-password'
nohup resy-snipe watch -config docs/examples/watch.toml \
    > watch.log 2>&1 &
echo "watch PID: $!"
```

Each `[[watches]]` entry is one NotifyMeRelease snipe. Watch the log
with `tail -f watch.log`. Stop with `kill <PID>`.

### `resy-snipe` (single-venue CLI) — older shape, one snipe per process

```
export RESY_SNIPE_IMAP_PASS='your-gmail-app-password'

resy-snipe \
  -venue-id 38660 -venue-name 'COQODAQ' \
  -date 2026-06-01 -party-size 2 -res-times 19:00,19:30 \
  -release-strategy notify-me \
  -retry-window 24h -poll-interval 5s \
  -imap-addr imap.gmail.com:993 \
  -imap-user jponwith@sandiego.edu \
  -user jponwith@sandiego.edu
```

Flag breakdown:

- `-venue-name` is the **display name** Resy puts in the alert email
  body (e.g. "COQODAQ"). The email Source uses it to match incoming
  alerts to your quest. Case-insensitive; whitespace-trimmed.
- `-retry-window` is the `ProbeUntil − ProbeFrom` span — how long the
  CLI sits waiting for an alert before giving up.
- `-poll-interval` is the per-quest cadence at which the engine asks
  the in-memory cache "any alert yet?". Zero falls back to engine
  `PollFloor` (100ms). The AlertSource's `MinPollInterval` is a hard
  floor.
- `-imap-pass-env` (default `RESY_SNIPE_IMAP_PASS`) names the env var
  holding the password. The password itself never appears in CLI
  history or process listings.
- Gmail-specific: enable IMAP in Settings, and generate an **app
  password** at myaccount.google.com → Security → App passwords.
  Plain account passwords don't work over IMAP.

The CLI opens the IMAP session synchronously at boot; bad creds or
unreachable host fail immediately rather than spinning. After Start,
the daemon's IMAP loop polls every 5s by default (`-imap-min-poll`);
the engine layer polls the in-memory cache every `-poll-interval`.
Total worst-case latency from email arrival to engine notification:
`imap-min-poll + poll-interval`.

## Anti-bot trade-off

| Strategy | Origin | Anti-bot exposure |
|---|---|---|
| Explicit | One `Find` at fire time | Lowest |
| Discovered | `Calendar` every PollFloor | Low–medium |
| **NotifyMe** | **External signal** (email/webhook) | **None** (no Resy traffic until Awaiting) |
| Continuous | `Find` every PollFloor | Highest |

NotifyMe is the only strategy that issues **zero pre-fire requests to
Resy**: all polling happens against the AlertSource (mailbox, webhook
receiver). Resy first sees us in the booking race, after the alert.

## Implementation status

**Engine + alerts seam: complete.**
- `internal/alerts` defines the Source interface, sentinels, and a
  `MemorySource` for tests and webhook-driven delivery.
- `engine.WithAlertSource` wires a Source into the engine.
- `runNotifyMeRelease` honors `PollInterval`, clamps to the source's
  `MinPollInterval`, classifies `ErrEnrollmentRequired` as terminal.

**Email Source: end-to-end.** IMAP polling loop, parser, and Source
matching are all wired and live behind `Source.Start(ctx, log)`.

| Component | Status | Code |
|---|---|---|
| Email parser | **Complete**, golden-tested against a real Resy alert | [`parse.go`](../internal/alerts/email/parse.go), [`testdata/coqodaq.eml`](../internal/alerts/email/testdata/coqodaq.eml) |
| Source matching (cache + venue-name registry) | **Complete** | [`email.go`](../internal/alerts/email/email.go) |
| `Source.IngestEmail` (testable seam) | **Complete** | [`email.go`](../internal/alerts/email/email.go) |
| IMAP connect / Login / Select / UIDSearch / Fetch loop | **Complete** (polling) | [`imap.go`](../internal/alerts/email/imap.go) |
| IMAP IDLE (near-push delivery) | Future optimization | — |

The parser extracts `(VenueName, Date, PartySize, Sender, FiredAt)`
from the load-bearing summary sentence Resy emits:

```
A table for 2 at COQODAQ on January 29 may now be available.
```

Sender check (`reservations@resy.com`) and subject marker
(`Table may be available`) gate non-alert mail out with
`ErrNotAResyAlert`. A real Resy alert whose body sentence is missing
errors with `ErrUnparseable` so a Resy-side template change pages
someone instead of silently breaking.

Year inference uses an always-future heuristic: parsed month/day
combined with the email `Date` header; if the candidate date is in
the past relative to the email arrival, the alert is for next year.

The Source caches parsed alerts keyed by `(lowercase name, date,
party)` and serves them via `Poll`. Because the engine's `Request`
carries a `VenueRef` but the email carries a display name, the daemon
must call `Source.RegisterVenueName(name, venue)` when each NotifyMe
quest is submitted — without that mapping, `Poll` returns
`ErrEnrollmentRequired` (which the engine treats as terminal).

**IMAP behavior:**

- `Source.Start(ctx, log)` dials TLS, logs in with `Username` /
  `Password`, and SELECTs `Mailbox`. Bad creds or unreachable host
  fail synchronously so daemon boot is loud.
- The runLoop polls every `MinPollInterval` (default 5s) via UIDSearch
  filtered by `From: reservations@resy.com`, then FETCHes any new UIDs
  and calls `IngestEmail` on each body (Peek so messages stay Unseen).
- UID tracking is in-memory; only messages with UID > the mailbox's
  UIDNext-at-Start are processed, so pre-existing alerts in the
  inbox are ignored. A daemon restart resets this high-water mark;
  cached fires from this process are lost but already-handled snipes
  have moved past Awaiting.
- On any transport error, the loop disconnects + reconnects on the
  next tick. Latency: up to `MinPollInterval` + the engine's
  per-quest `PollInterval` from email arrival to engine notification.

**Daemon wiring still needed:**

1. Load IMAP `Password` via `internal/secrets` (rather than a CLI
   flag) when constructing `email.Config`.
2. Construct `email.New(cfg)`, call `Source.Start(ctx, log)`, pass to
   `engine.WithAlertSource`.
3. On each NotifyMe quest submission, call
   `Source.RegisterVenueName(displayName, venueRef)` so Poll can
   match incoming emails to the engine's VenueRef-keyed Request.

## Sentinels

- `alerts.ErrEnrollmentRequired` — Source observed no enrollment for
  this (user, venue, date). For the email source: no NotifyMe email
  has ever arrived (the user hasn't tapped "Notify me" in Resy).
  Engine fails the snipe so the CLI/MCP can prompt for the
  out-of-band enrollment step.
- `alerts.ErrSourceUnavailable` — transient transport failure
  (IMAP disconnect). Engine surfaces it; operator decides retry.
- `alerts.ErrSourceNotImplemented` — placeholder for scaffolded
  Sources; classified same as `ErrSourceUnavailable`.

## Tests

- `TestNotifyMeReleaseFiresOnAlert` — Source is a MemorySource;
  test fires an alert mid-poll and asserts the snipe lands in
  `Awaiting`.
- `TestNotifyMeReleaseExpiresWhenWindowCloses` — no fire before
  `ProbeUntil`; snipe lands in `Failed`.
- `TestNotifyMeReleaseFailsOnEnrollmentRequired` — Source returns
  `ErrEnrollmentRequired`; snipe lands in `Failed`.
- `TestNotifyMeReleaseHonorsPerQuestInterval` — quest asks for 50ms
  but source floor is 200ms; engine clamps and only polls at the
  source's pace.
- `TestNotifyMeReleaseRequiresAlertSource` — `Run` without
  `WithAlertSource` errors at dispatch rather than at first poll.
- `internal/alerts/alerts_test.go` — MemorySource unit tests
  (fired/unfired states, mismatch filtering, MinPollInterval clamp).
