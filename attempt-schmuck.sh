#!/usr/bin/env bash
# Invoked by resy-schmuck.timer at each drop (and on boot catch-up via
# Persistent=true). Attempts every not-yet-tried drop that is due, in order,
# and stops the whole campaign at the first successful booking.
set -u
cd "$(dirname "$0")"
mkdir -p logs
LOG=logs/schmuck-snipe.log
exec >>"$LOG" 2>&1

if [ -f logs/schmuck-BOOKED ]; then
  echo "[$(date)] already booked; nothing to do"
  exit 0
fi

set -a; source .env; set +a

# Pause the Sep 15 cancellation watch while sniping: two processes booking at
# once could land two tables, and its polling competes for the rate limit.
systemctl --user stop resy-schmuck-watch.service 2>/dev/null || true

now=$(date +%s)
ran=0
for spec in \
  "targets-schmuck-0915.json|2026-09-01 09:59" \
  "targets-schmuck-0918.json|2026-09-04 09:59" \
  "targets-schmuck-0919.json|2026-09-05 09:59" \
  "targets-schmuck-0925.json|2026-09-11 09:59"; do
  plan="${spec%%|*}"
  when="${spec##*|}"
  tag="${plan%.json}"; tag="${tag##*-}"
  marker="logs/schmuck-attempt-$tag.done"
  drop=$(date -d "$when" +%s)

  [ -f "$marker" ] && continue
  # Not due yet: the timer fires at 09:58, one minute early, so "due" means
  # within 5 minutes. A drop hours in the past (missed while powered off) is
  # still attempted once — a leftover or cancelled table may remain.
  (( drop > now + 300 )) && continue

  ran=1
  touch "$marker"
  echo "[$(date)] attempting $plan (drop $when)"
  if ./bin/resy-bot -targets "$plan"; then
    touch logs/schmuck-BOOKED
    echo "[$(date)] BOOKED via $plan — campaign finished."
    exit 0
  fi
  echo "[$(date)] $plan did not book"
done

if (( ran == 0 )); then
  echo "[$(date)] no attempt due (next drops are still in the future)"
fi

# Nothing booked: resume the cancellation watch (its wrapper exits on its own
# if it is no longer needed).
systemctl --user start resy-schmuck-watch.service 2>/dev/null || true
exit 0
