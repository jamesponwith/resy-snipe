# NotifyMeRelease strategy

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

## CLI

```
resy-snipe \
  -venue-id 38660 -date 2026-06-01 -party-size 2 \
  -res-times 19:00,19:30 \
  -release-strategy notify-me \
  -retry-window 4h \
  -poll-interval 5s \
  -user you@example.com
```

`-retry-window` is the `ProbeUntil − ProbeFrom` span. `-poll-interval`
is the per-quest cadence; zero falls back to engine `PollFloor`. The
AlertSource's `MinPollInterval` is a **hard floor** — a quest can't
poll faster than the origin allows (IMAP politeness, anti-bot).

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

**Email Source: scaffolded but stubbed.**
[`internal/alerts/email/email.go`](../internal/alerts/email/email.go)
defines the `Config` shape (IMAPAddr, Username, Password, Mailbox,
SenderFilter), constructor, and `Source` type. `Poll` returns
`ErrSourceNotImplemented`.

**To finish the email Source:**

1. Open a long-lived IMAP connection per Config — `go-imap` or
   similar. Use IDLE for near-push delivery; fall back to polling on
   servers without it.
2. On each new message matching `SenderFilter`, parse subject + body
   into `(venue, date, party_size)`. The parser belongs in
   `internal/alerts/email/parse.go` alongside a golden-test fixture
   from a real Resy NotifyMe email.
3. Cache fires in a map keyed by `(User, VenueRef, Date, PartySize)`
   so `Poll` is an O(1) lookup.
4. Wire the daemon's secret-loader so `Password` comes from
   `internal/secrets` rather than CLI flags.

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
