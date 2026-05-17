#!/usr/bin/env bash
# scripts/notify-launch.sh — one-stop launcher for `resy-snipe watch`.
#
# Builds the binary (idempotent), prompts for the IMAP password if not
# already in the environment, launches the watch process under nohup
# so it survives shell exit, prints the PID + log path, and tails the
# log until you Ctrl-C (the watch keeps running in the background).
#
# Usage:
#     scripts/notify-launch.sh                          # uses docs/examples/watch.toml
#     scripts/notify-launch.sh path/to/your-watch.toml
#
# Prerequisites — one-time setup:
#   1. Run `resy-snipe login -user <email>` once per user in the config
#      so each Resy session is stored in the sealed secrets layer.
#   2. Create a Gmail app password at
#      https://myaccount.google.com/apppasswords (requires 2-Step
#      Verification on the account). You'll paste it at the prompt.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

CONFIG="${1:-docs/examples/watch.toml}"

if [ ! -f "$CONFIG" ]; then
    echo "error: config file not found: $CONFIG" >&2
    exit 1
fi

# ---- 1. Build (idempotent — Go skips up-to-date files) ----------------
echo "==> Building resy-snipe -> bin/resy-snipe ..."
mkdir -p bin
go build -o ./bin/resy-snipe ./cmd/resy-snipe

BIN="$REPO_ROOT/bin/resy-snipe"

# ---- 2. Prompt for IMAP password if needed ----------------------------
if [ -z "${RESY_SNIPE_IMAP_PASS:-}" ]; then
    echo
    echo "==> Gmail app password (input hidden; not stored in shell history):"
    read -s -p "    Password: " RESY_SNIPE_IMAP_PASS
    echo
    export RESY_SNIPE_IMAP_PASS
fi

PASS_LEN="${#RESY_SNIPE_IMAP_PASS}"
if [ "$PASS_LEN" -lt 14 ]; then
    echo "warning: password is ${PASS_LEN} chars — Gmail app passwords are 16 (with or without spaces)." >&2
fi

# ---- 3. Stop any existing watch process (so re-running is safe) -------
EXISTING_PIDS=$(pgrep -f "resy-snipe watch" || true)
if [ -n "$EXISTING_PIDS" ]; then
    echo "==> Stopping previous watch process(es): $EXISTING_PIDS"
    # shellcheck disable=SC2086
    kill $EXISTING_PIDS 2>/dev/null || true
    sleep 1
fi

# ---- 4. Launch under nohup --------------------------------------------
LOG="$REPO_ROOT/watch.log"
echo "==> Launching watch with config: $CONFIG"
echo "    log: $LOG"
nohup "$BIN" watch -config "$CONFIG" > "$LOG" 2>&1 &
PID=$!

# Give the process a moment to fail fast (bad creds, no Resy session, etc.).
sleep 2
if ! kill -0 "$PID" 2>/dev/null; then
    echo
    echo "error: watch exited within 2 seconds. Last lines of $LOG:" >&2
    tail -20 "$LOG" >&2
    exit 1
fi

echo
echo "==> watch is running. PID: $PID"
echo "==> log:    tail -f $LOG"
echo "==> status: ps -p $PID || echo 'not running'"
echo "==> stop:   kill $PID"
echo
echo "==> Tailing log (Ctrl-C to detach; the watch keeps running):"
echo
exec tail -f "$LOG"
