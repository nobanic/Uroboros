#!/bin/sh
# Uroboros supervisor start script (Linux / macOS / Docker ENTRYPOINT)
#
# Usage:
#   ./start.sh                       Run in the foreground (Ctrl-C to stop)
#   nohup ./start.sh >uroboros.log 2>&1 &   Run detached in the background
#   ./start.sh -addr :9000           Extra arguments are passed through to uroboros as-is
#
# It does just one thing: launch uroboros, and after the process exits decide from the exit code whether to relaunch it.
#
#   0      User stopped it normally  → exit the loop
#   75     Program requested restart → relaunch immediately (one-click update or rollback clicked in the UI)
#   other  Crash                     → relaunch after a backoff (1→2→4… up to 60 seconds)
#
# Downloading, SHA256 verification, and swapping the binary are deliberately not done here: that logic would have to be written twice, for sh and for bat,
# and it is exactly the part that must never go wrong—once a non-working binary is swapped in, this script would faithfully
# relaunch it over and over, leaving the user to rescue the machine by hand. So verification/swapping all stays in Go (the selfupdate package),
# done by uroboros itself at startup, keeping the script dead simple.
set -u

cd "$(dirname "$0")" || exit 1

BIN=./uroboros
[ -x "$BIN" ] || { echo "[uroboros] executable $BIN not found" >&2; exit 1; }

RESTART_CODE=75
MAX_DELAY=60

child=0
stopping=0

# Forward the stop signal to the uroboros binary itself.
#
# This is required under Docker: docker stop sends SIGTERM only to PID 1 (this script),
# not to the child process. Without forwarding, uroboros never receives the signal, cannot shut down gracefully, and after 10 seconds is
# hard-killed by SIGKILL, cutting off any running task midway.
forward() {
	stopping=1
	if [ "$child" -ne 0 ]; then
		kill -TERM "$child" 2>/dev/null || true
	fi
}
trap forward INT TERM

delay=1
while :; do
	"$BIN" "$@" &
	child=$!

	# A signal interrupts wait and makes it return >128. At that point the child is actually still shutting down gracefully,
	# so we must wait once more to get its real exit code.
	wait "$child"
	code=$?
	if [ "$code" -gt 128 ]; then
		wait "$child"
		code=$?
	fi
	child=0

	if [ "$stopping" -eq 1 ]; then
		echo "[uroboros] stopped"
		exit 0
	fi

	case "$code" in
		0)
			echo "[uroboros] exited normally"
			exit 0
			;;
		"$RESTART_CODE")
			# Update/rollback is ready: after relaunch, uroboros completes the swap at startup (see selfupdate.Bootstrap).
			echo "[uroboros] restart requested (applying new version)…"
			delay=1
			;;
		*)
			echo "[uroboros] abnormal exit (code=$code), restarting in ${delay}s" >&2
			sleep "$delay"
			delay=$((delay * 2))
			[ "$delay" -gt "$MAX_DELAY" ] && delay=$MAX_DELAY
			;;
	esac
done
