#!/usr/bin/env bash
# Cancellation backstop: watches Sep 15 at Schmuck and auto-books the first
# matching slot. Run by resy-schmuck-watch.service; exits cleanly once the
# campaign has booked anything, or once Sep 15 has passed.
set -u
cd "$(dirname "$0")"
mkdir -p logs
exec >>logs/schmuck-snipe.log 2>&1

if [ -f logs/schmuck-BOOKED ]; then
  echo "[$(date)] watch: campaign already booked; not watching"
  exit 0
fi
if [[ "$(date +%F)" > "2026-09-15" ]]; then
  echo "[$(date)] watch: Sep 15 has passed; exiting"
  exit 0
fi

set -a; source .env; set +a

echo "[$(date)] watch: starting Sep 15 cancellation watch (poll 60s)"
./bin/resy-bot -watch -targets targets-schmuck-watch-0915.json -alert-log logs/alerts.jsonl
status=$?

# With max_bookings=1 the watcher only exits on its own after booking; confirm
# via the alert log and halt the rest of the campaign.
if grep -q '"kind":"booked".*"date":"2026-09-15"' logs/alerts.jsonl 2>/dev/null; then
  touch logs/schmuck-BOOKED
  echo "[$(date)] watch: BOOKED Sep 15 — campaign finished."
  exit 0
fi
exit $status
