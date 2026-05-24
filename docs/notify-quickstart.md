# NotifyMe quickstart — fresh-start runbook

You walked back into this repo cold. Here's how to get from `git clone`
to a running watcher in under ten minutes. Architecture is in
[`notify-me.md`](notify-me.md); this page is purely operational.

## What it does

One long-running process (`resy-snipe watch`) polls your Gmail inbox
every 5 seconds for Resy NotifyMe alert emails. When a matching alert
fires, it transitions the snipe to Awaiting and runs the booking race
against Resy's `/3/find` + `/3/book`. Pre-fire: zero traffic to Resy.

## One-time prerequisites

### 1. Resy: enroll for NotifyMe on each venue you want to watch

Open the Resy app, find the venue, tap **Notify me**, pick the date
and party size. Without an active enrollment, no email will ever
arrive and the watcher has nothing to act on.

### 2. Resy: log into resy-snipe so the session is sealed

Two paths depending on what kind of Resy account you have:

**A. Email + password account (most common)** — interactive prompt:

```bash
resy-snipe login -user jponwith@sandiego.edu
```

Paste your Resy password when prompted.

**B. Phone-based account (no settable password)** — import a JWT
you've captured from the mobile app or browser DevTools:

```bash
read -s -p "Resy JWT: " RESY_AUTH_TOKEN
export RESY_AUTH_TOKEN
resy-snipe login -user jponwith@sandiego.edu -token-env RESY_AUTH_TOKEN
```

The login command parses the JWT's `exp` claim, refuses to import an
already-expired token, and seals it under the supplied user. When
the token expires, re-capture and re-run.

Either way: the session lands in
`~/Library/Application Support/resy-snipe/db.sqlite` and is re-used
by every subsequent run until expiry.

### 3. Gmail: enable 2-Step Verification + create an app password

- 2SV: <https://myaccount.google.com/security> → enable 2-Step
  Verification if it isn't already on.
- App password: <https://myaccount.google.com/apppasswords> →
  name it "resy-snipe" → copy the 16-char password (shown once).
- The address Resy sends alerts to is the inbox we poll. For this
  setup that's `jponwith@sandiego.edu` (a Google-Workspace inbox; same
  IMAP host as personal Gmail).

### 4. Confirm bd is available (optional, only for issue tracking)

```bash
command -v bd        # /opt/homebrew/bin/bd
bd list --status open
```

If you don't have it: `brew install beads`.

## Each-session prerequisites

### Build the binary

```bash
just build           # → bin/resy-snipe
```

Or `go build -o ./bin/resy-snipe ./cmd/resy-snipe` if you don't have
`just`.

### Put the IMAP password into the environment, silently

```bash
read -s -p "Gmail app password: " RESY_SNIPE_IMAP_PASS
export RESY_SNIPE_IMAP_PASS
echo "${#RESY_SNIPE_IMAP_PASS}"   # 16 (or 19 with spaces) — not zero
```

This is the only place the password ever appears in plaintext. It
doesn't land in shell history or process listings.

## Edit `docs/examples/watch.toml`

Each `[[watches]]` block is one venue+date the daemon will snipe.
Today's config already has 4 Charles + Torrisi for May 22; replace or
extend.

To add a new venue, resolve its Resy id first:

```bash
bin/resy-snipe venue resolve "Carbone"
```

That prints the candidate matches with their `resy:NNNNN` ids. Pick
the one you want and add a new block:

```toml
[[watches]]
venue_id     = 5832
venue_name   = "Carbone"             # display name — must match what Resy puts in the email body
date         = "2026-06-14"
party_size   = 2
res_times    = ["19:00","19:30","20:00","20:30","21:00"]
retry_window = "168h"                # how long to keep watching before giving up
user         = "jponwith@sandiego.edu"
```

The watcher does case-insensitive name matching, so `Carbone` matches
`carbone`, `CARBONE`, etc. — but the literal string must be a
substring of whatever Resy puts in the body.

## Launch

```bash
scripts/notify-launch.sh
```

Behavior:
- Builds the binary (idempotent).
- Prompts for the Gmail app password if `RESY_SNIPE_IMAP_PASS` is
  unset.
- Stops any previously running `resy-snipe watch` process.
- Launches the new one under `nohup` so it survives shell exit.
- Fails fast (within 2 s) if creds or session are broken and prints
  the relevant log lines.
- Tails `watch.log` until Ctrl-C. The watcher keeps running.

## Monitor

```bash
tail -f watch.log
ps -ef | grep "resy-snipe watch" | grep -v grep
```

What you'll see in the log:

```
{"level":"INFO","msg":"watch: email source attached","imap_addr":"imap.gmail.com:993","imap_user":"jponwith@sandiego.edu","watch_count":2}
{"level":"INFO","msg":"watch: submitted","venue_name":"4 Charles Prime Rib","snipe_id":"snp_..."}
{"level":"INFO","msg":"watch: submitted","venue_name":"Torrisi","snipe_id":"snp_..."}
```

Then silence until an alert email lands. On alert:

```
{"level":"INFO","msg":"snipe transition","from":"discovering","to":"awaiting","via":"notify_me",...}
{"level":"INFO","msg":"snipe transition","from":"awaiting","to":"finding",...}
{"level":"INFO","msg":"snipe transition","from":"booking","to":"booked","confirmation_id":"..."}
```

If you have multiple watches for the same `(user, date)` (e.g., 4
Charles AND Torrisi on the same night), only the first one to land
an alert attempts the booking — the others cancel cleanly with
reason `duplicate_already_booked`.

## Stop

```bash
pkill -f "resy-snipe watch"        # or `kill <PID>` from the launch script
```

## Test cases

The full test suite covers all the new behavior:

```bash
# with `just` installed (brew install just):
just test                          # full suite under -race
just test-pkg ./internal/alerts/   # email parser, IMAP source
just test-pkg ./internal/engine/   # NotifyMeRelease engine path
just test-pkg ./cmd/resy-snipe/    # watch subcommand + dedup tracker

# or with go directly:
go test -race ./...
go test -race ./internal/alerts/...
go test -race ./internal/engine/...
go test -race ./cmd/resy-snipe/...
```

Manual integration test (against a real Gmail account, real Resy
account, real NotifyMe enrollment) is by definition not in the suite
— that's what `scripts/notify-launch.sh` is for.

## Known limitations

Tracked in beads — `bd list --status open` for the live list. As of
this writing:

- `resy-snipe-1y9` — no pre-existing-reservation check; manual
  reservations made outside this process are not detected.
- `resy-snipe-bz9` — no auto-enroll Resy NotifyMe after a failed
  booking attempt.
- `resy-snipe-sil` — `RESY_SNIPE_SIGNER_BIN` is unset by default, so
  Find/Book runs against Resy's noop signer. Usually fine for
  low-volume use, but tighten this before scaling.

## Quick recovery — "I broke it, how do I start over"

```bash
pkill -f "resy-snipe watch"        # stop running watchers
git pull --rebase                  # latest code
git checkout notify-v2             # this branch
just build                         # rebuild
# Re-run the prereqs section above if your shell session is fresh,
# then `scripts/notify-launch.sh`.
```

That's it — you should be back in a known state.
