#!/usr/bin/env bash
# Attach a target container to the sealed Uroboros lab network so Uroboros can scan it,
# while the target stays cut off from the internet and the host.
#
#   ./attach-target.sh <container-name-or-id> [alias]
#
# Default alias is monixa.local — Uroboros will reach the target at http://monixa.local
# (resolved by Docker's embedded DNS inside the sealed network).
#
# IMPORTANT: for a truly sealed range, the target should NOT also sit on an
# internet-capable network. If it does, this script warns you — Uroboros could then
# reach the internet by pivoting through the target. Put the target on this lab
# network only (or remove its other networks) for a hermetic test.
set -euo pipefail
LAB="uroboros_sandbox_lab"
TARGET="${1:?usage: ./attach-target.sh <container-name-or-id> [alias]}"
ALIAS="${2:-monixa.local}"

docker network inspect "$LAB" >/dev/null 2>&1 || {
  echo "Lab network '$LAB' not found — start the sandbox first:"
  echo "  docker compose -f docker-compose.sandbox.yml up -d --build"
  exit 1
}
docker inspect "$TARGET" >/dev/null 2>&1 || { echo "Container '$TARGET' not found."; exit 1; }

# Warn if the target already has an egress-capable (non-internal) network.
for net in $(docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$TARGET"); do
  [ "$net" = "$LAB" ] && continue
  if [ "$(docker network inspect -f '{{.Internal}}' "$net" 2>/dev/null)" = "false" ]; then
    echo "WARNING: '$TARGET' is also on network '$net' which CAN reach the internet."
    echo "         For a hermetic lab, detach it from that network:  docker network disconnect $net $TARGET"
  fi
done

echo "Attaching '$TARGET' to '$LAB' as alias '$ALIAS' ..."
docker network connect --alias "$ALIAS" "$LAB" "$TARGET" 2>/dev/null \
  && echo "attached." || echo "already attached (or alias set); continuing."

# Verify Uroboros can resolve+reach the target, and that egress is still blocked,
# using a throwaway probe on the same sealed network.
echo "Verifying from inside the sealed lab ..."
REACH=$(docker run --rm --network "$LAB" curlimages/curl:latest -s -o /dev/null -w '%{http_code}' --max-time 6 "http://$ALIAS/" 2>/dev/null || true)
echo "  GET http://$ALIAS/  -> HTTP ${REACH:-<none>}  (any response means Uroboros can reach it)"
EGR=$(docker run --rm --network "$LAB" curlimages/curl:latest -s -o /dev/null -w '%{http_code}' --max-time 5 http://1.1.1.1/ 2>/dev/null || echo 000)
[ "$EGR" = "000" ] && echo "  internet from lab -> BLOCKED (good)" || echo "  WARNING: internet reachable from lab (HTTP $EGR) — check the target's other networks"
echo
echo "Done. In the Uroboros UI, point a task/scope at:  http://$ALIAS"
