#!/usr/bin/env bash
# Verify the Uroboros sandbox is actually sealed, against the RUNNING stack.
#
# The app container is distroless (no shell), so we cannot `docker exec` into it.
# Instead we prove the seal two ways:
#   (1) structurally  — docker inspect: which networks, caps, user, RO fs, mounts
#   (2) behaviorally  — a throwaway probe attached to the SAME internal network;
#                       if the probe cannot egress, nothing on that network can.
#
# Exit non-zero if any check fails. Safe to run repeatedly.
set -u
PROJECT="uroboros-sandbox"
PASS=0; FAIL=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); }
hdr()  { printf '\n\033[36m== %s ==\033[0m\n' "$1"; }

cid() { docker ps -aqf "label=com.docker.compose.project=$PROJECT" -f "name=${PROJECT}-$1" | head -1; }
APP=$(cid uroboros); GW=$(cid gateway); PG=$(cid postgres)
INTNET="uroboros_sandbox_lab"     # the sealed lab network (fixed name)
EDGENET="uroboros_sandbox_edge"   # the forwarder's edge network (fixed name)

[ -n "$APP" ] || { echo "uroboros container not found — is the stack up? (docker compose -f docker-compose.sandbox.yml up -d)"; exit 2; }

hdr "1. App container is on the SEALED lab network ONLY"
NETS=$(docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$APP" | tr -s ' ')
echo "    uroboros networks: $NETS"
if echo "$NETS" | grep -q "$INTNET" && ! echo "$NETS" | grep -q "$EDGENET"; then
  ok "uroboros attached to the sealed lab network only (not edge)"
else
  bad "uroboros is attached to an egress-capable network"
fi

hdr "2. The internal network is truly internal (no external route)"
ISINT=$(docker network inspect -f '{{.Internal}}' "$INTNET" 2>/dev/null)
[ "$ISINT" = "true" ] && ok "network $INTNET Internal=true" || bad "network $INTNET Internal=$ISINT"

hdr "3. Behavioral egress test from the sealed network (throwaway probe)"
echo "    launching probe on $INTNET ..."
EGRESS=$(docker run --rm --network "$INTNET" alpine:latest sh -c \
  'wget -T4 -q -O- http://1.1.1.1/ >/dev/null 2>&1 && echo OPEN || echo BLOCKED' 2>/dev/null)
[ "$EGRESS" = "BLOCKED" ] && ok "outbound to 1.1.1.1 BLOCKED (no route)" || bad "outbound EGRESS=$EGRESS"
DNS=$(docker run --rm --network "$INTNET" alpine:latest sh -c \
  'nslookup example.com >/dev/null 2>&1 && echo RESOLVES || echo NORESOLVE' 2>/dev/null)
[ "$DNS" = "NORESOLVE" ] && ok "external DNS does NOT resolve" || bad "external DNS DNS=$DNS"
HDI=$(docker run --rm --network "$INTNET" alpine:latest sh -c \
  'nslookup host.docker.internal >/dev/null 2>&1 && echo RESOLVES || echo NORESOLVE' 2>/dev/null)
[ "$HDI" = "NORESOLVE" ] && ok "host.docker.internal does NOT resolve" || bad "host.docker.internal HDI=$HDI"
ROUTE=$(docker run --rm --network "$INTNET" alpine:latest sh -c 'ip route | grep -c "^default" ' 2>/dev/null)
[ "$ROUTE" = "0" ] && ok "no default route on the sealed network" || bad "a default route exists ($ROUTE)"

hdr "4. The app IS reachable from inside the sealed net (service works)"
APPUP=$(docker run --rm --network "$INTNET" curlimages/curl:latest -s -o /dev/null -w '%{http_code}' --max-time 6 http://uroboros:8787/ 2>/dev/null)
echo "    GET http://uroboros:8787/ -> HTTP $APPUP"
case "$APPUP" in 2??|3??|401|403) ok "uroboros answers on the internal net (HTTP $APPUP)";; *) bad "uroboros not answering internally (HTTP $APPUP)";; esac

hdr "5. UI reachable from the HOST only on loopback"
HOSTCODE=$(curl -s -o /dev/null -w '%{http_code}' --max-time 6 http://127.0.0.1:8787/ 2>/dev/null)
echo "    host GET 127.0.0.1:8787 -> HTTP $HOSTCODE"
case "$HOSTCODE" in 2??|3??|401|403) ok "UI reachable on 127.0.0.1 (HTTP $HOSTCODE)";; *) bad "UI not reachable on 127.0.0.1 (HTTP $HOSTCODE)";; esac
BIND=$(docker port "$GW" 2>/dev/null | tr '\n' ' ')
echo "    gateway published: $BIND"
if echo "$BIND" | grep -q '127.0.0.1:8787' && ! echo "$BIND" | grep -qE '0\.0\.0\.0|\[::\]'; then
  ok "published on 127.0.0.1 only (not the LAN)"
else
  bad "gateway is published beyond loopback"
fi

hdr "6. App container hardening (inspect)"
RO=$(docker inspect -f '{{.HostConfig.ReadonlyRootfs}}' "$APP")
[ "$RO" = "true" ] && ok "root filesystem read-only" || bad "root fs NOT read-only ($RO)"
USER=$(docker inspect -f '{{.Config.User}}' "$APP")
case "$USER" in 0|root|"") bad "app runs as root ($USER)";; *) ok "app runs as non-root ($USER)";; esac
CAPS=$(docker inspect -f '{{.HostConfig.CapDrop}}' "$APP")
echo "    CapDrop: $CAPS"
echo "$CAPS" | grep -qi 'ALL' && ok "all Linux capabilities dropped" || bad "capabilities not fully dropped"
NNP=$(docker inspect -f '{{.HostConfig.SecurityOpt}}' "$APP")
echo "$NNP" | grep -q 'no-new-privileges' && ok "no-new-privileges set" || bad "no-new-privileges missing"
BINDS=$(docker inspect -f '{{range .Mounts}}{{.Type}}:{{.Source}} {{end}}' "$APP")
echo "    mounts: $BINDS"
if echo "$BINDS" | grep -q 'bind:'; then bad "a HOST path is bind-mounted into the app"; else ok "no host bind mounts (named volume only)"; fi
PIDS=$(docker inspect -f '{{.HostConfig.PidsLimit}}' "$APP")
[ "$PIDS" != "0" ] && [ "$PIDS" != "<nil>" ] && ok "pids limit set ($PIDS)" || bad "no pids limit"

hdr "7. Postgres is not published anywhere"
PGPORTS=$(docker port "$PG" 2>/dev/null)
[ -z "$PGPORTS" ] && ok "postgres has no published ports" || bad "postgres publishes: $PGPORTS"

hdr "8. Forwarder cannot kernel-route between its two networks"
GWFWD=$(docker inspect -f '{{index .HostConfig.Sysctls "net.ipv4.ip_forward"}}' "$GW" 2>/dev/null)
[ "$GWFWD" = "0" ] && ok "gateway ip_forward=0 (no kernel routing edge<->lab)" || bad "gateway ip_forward=$GWFWD"

printf '\n\033[1mRESULT: %d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] && echo "Sandbox is sealed." || echo "Review the FAIL lines above."
exit "$FAIL"
