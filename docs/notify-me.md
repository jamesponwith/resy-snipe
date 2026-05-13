# NotifyMeRelease strategy

`NotifyMeRelease` is the fourth release strategy
([`release-strategies.md`](release-strategies.md)). It lets a snipe
sit dormant until Resy's NotifyMe alert fires for the target
(venue, date), then transitions straight into the booking race. The
intended use case is **catching cancellations and post-drop openings
without paying the anti-bot cost of polling `/4/find`**.

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
   | (PollAlerts returns ErrAlertEnrollmentRequired)
   ↓
Failed (reason=notify_me_enrollment_required)
```

`Discovering` carries `via=notify_me` on its event attrs to distinguish
it from a `DiscoveredRelease` discovering event.

## CLI

```
resy-snipe \
  -venue-id 38660 -date 2026-06-01 -party-size 2 \
  -res-times 19:00,19:30 \
  -release-strategy notify-me \
  -retry-window 4h \
  -user you@example.com
```

`-retry-window` becomes the `ProbeUntil − ProbeFrom` span. `ProbeFrom`
defaults to `now`, so polling starts immediately and gives up after
`retry-window` elapses.

## Anti-bot trade-off

| Strategy | Endpoint | Anti-bot exposure |
|---|---|---|
| Explicit | One `Find` at fire time | Lowest |
| Discovered | `Calendar` every PollFloor | Low–medium |
| **NotifyMe** | `PollAlerts` every PollFloor | **Low** (account endpoint) |
| Continuous | `Find` every PollFloor | Highest |

Find is the most-watched endpoint on Resy's surface. The NotifyMe
alert surface is account-bound and tied to a feature Resy expects
users to lean on heavily, so it's significantly more permissive.

## Implementation status

**Engine side: complete.** State machine, dispatch, audit events, ctx
cancellation, expiry, and enrollment-required classification are all
wired and tested
([`internal/engine/release.go`](../internal/engine/release.go),
[`internal/engine/release_test.go`](../internal/engine/release_test.go)).

**Resy adapter side: stubbed.** The HTTP wire format for the NotifyMe
alert-state endpoint is not captured in this repo. The stub at
[`internal/resy/notify_alerts.go`](../internal/resy/notify_alerts.go)
returns `ErrAlertEnrollmentRequired` so that any caller exercising the
path fails loudly rather than silently doing nothing.

To complete the integration:

1. Identify the endpoint via a packet capture of the Resy mobile app's
   NotifyMe flow. Candidate paths to watch for: `/3/notify`, `/2/user/notify`.
2. Map the response shape onto `providers.AlertState{Fired: bool}`.
   Resy typically uses a per-enrollment state string (e.g.
   `"pending"` → `"fired"`).
3. Decide whether `PollAlerts` should auto-enroll on first call or
   require an explicit `EnrollAlert` provider method. The engine
   classifies `ErrAlertEnrollmentRequired` as terminal, so explicit
   enrollment is cleanly accommodated.
4. Route the request through `doSignedAndRetry` to share the unified
   anti-bot envelope.

The `providers.Provider` interface, the engine loop, and the CLI flag
are all stable across this work — only the body of `Client.PollAlerts`
in `internal/resy/notify_alerts.go` needs to change.

## Sentinels

- `providers.ErrAlertEnrollmentRequired` — adapter signal that there
  is no active NotifyMe enrollment for the (venue, date) pair. The
  engine surfaces this as a `Failed` event with
  `reason=notify_me_enrollment_required` so the CLI/MCP layer can
  prompt for the out-of-band enrollment step.

## Tests

- `TestNotifyMeReleaseFiresOnAlert` — happy path: poll twice, second
  call returns `Fired:true`, snipe lands in `Awaiting`.
- `TestNotifyMeReleaseExpiresWhenWindowCloses` — `ProbeUntil` elapses
  with no fire, snipe lands in `Failed`.
- `TestNotifyMeReleaseFailsOnEnrollmentRequired` — adapter returns
  `ErrAlertEnrollmentRequired`, snipe lands in `Failed`.
