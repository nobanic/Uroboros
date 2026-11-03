#!/usr/bin/env bash
# Verify the "internet" variant: the app can reach the PUBLIC internet, but NOT
# the host or the LAN, and the database has NO internet. Run against the running
# stack (docker compose -f docker-compose.internet.yml up -d).
set -u
PROJECT="uroboros-internet"
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[36m== %s ==\033[0m\n' "$1"; }
cid() { docker ps -aqf "label=com.docker.compose.project=$PROJECT" -f "name=${PROJECT}-$1" | head -1; }

FW=$(cid firewall); APP=$(cid uroboros); PG=$(cid postgres)
DBNET="uroboros_net_dbnet"
[ -n "$FW" ] || { echo "firewall container not found — is the internet stack up?"; exit 2; }

# Probe that shares the firewall netns == exactly what the app can/can't reach.
fw_probe() { docker run --rm --network "container:$FW" curlimages/curl:latest -s -o /dev/null -w '%{http_code}' --max-time "${2:-8}" "$1" 2>/dev/null; }

hdr "1. App CAN reach the public internet"
P_IP=$(fw_probe https://1.1.1.1/ 8);        case "$P_IP" in 2??|3??) ok "public IP reachable (HTTP $P_IP)";; *) bad "public IP unreachable ($P_IP)";; esac
P_DNS=$(fw_probe https://example.com/ 12);   case "$P_DNS" in 2??|3??) ok "public DNS + HTTPS works (HTTP $P_DNS)";; *) bad "public DNS/HTTPS failed ($P_DNS)";; esac
P_LLM=$(fw_probe https://api.anthropic.com/ 12); case "$P_LLM" in 2??|3??|401|403|404) ok "LLM endpoint reachable (HTTP $P_LLM)";; *) bad "LLM endpoint unreachable ($P_LLM)";; esac

hdr "2. App CANNOT reach the host or the LAN"
H=$(fw_probe http://host.docker.internal/ 6); [ "$H" = "000" ] && ok "host.docker.internal blocked" || bad "host reachable (HTTP $H)"
for ip in 192.168.65.254 192.168.0.1 10.0.0.1 172.16.0.1; do
  R=$(fw_probe "http://$ip/" 5); [ "$R" = "000" ] && ok "private $ip blocked" || bad "private $ip reachable (HTTP $R)"
done

hdr "2b. Firewall rules are actually installed (structural — a 000 above can't be a false pass)"
RULES=$(docker exec "$FW" iptables -S OUTPUT 2>/dev/null)
echo "$RULES" | grep -q -- '-P OUTPUT DROP' && ok "default OUTPUT policy is DROP (fail-safe)" || bad "default OUTPUT policy is not DROP"
for r in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 169.254.0.0/16; do
  echo "$RULES" | grep -q -- "-d $r -j DROP" && ok "explicit DROP for $r present" || bad "no DROP rule for $r"
done
FWD=$(docker exec "$FW" sh -c 'cat /proc/sys/net/ipv4/ip_forward' 2>/dev/null)
[ "$FWD" = "0" ] && ok "kernel ip_forward=0 in firewall netns" || bad "ip_forward=$FWD (forwarding not disabled)"

hdr "3. Database has NO internet (sealed dbnet)"
DBEG=$(docker run --rm --network "$DBNET" curlimages/curl:latest -s -o /dev/null -w '%{http_code}' --max-time 5 https://example.com/ 2>/dev/null)
case "${DBEG:-000}" in 000|"") ok "dbnet cannot reach the internet";; *) bad "dbnet has internet (HTTP $DBEG)";; esac
PGPORTS=$(docker port "$PG" 2>/dev/null); [ -z "$PGPORTS" ] && ok "postgres not published" || bad "postgres publishes $PGPORTS"

hdr "4. UI reachable from host on loopback only"
U=$(curl -s -o /dev/null -w '%{http_code}' --max-time 6 http://127.0.0.1:8787/ 2>/dev/null)
case "$U" in 2??|3??|401|403) ok "UI on 127.0.0.1 (HTTP $U)";; *) bad "UI not reachable (HTTP $U)";; esac
BIND=$(docker port "$FW" 2>/dev/null | tr '\n' ' ')
echo "    published: $BIND"
if echo "$BIND" | grep -q '127.0.0.1:8787' && ! echo "$BIND" | grep -qE '0\.0\.0\.0|\[::\]'; then ok "loopback-only publish"; else bad "published beyond loopback"; fi

hdr "5. Hardening (inspect)"
for c in "$APP|uroboros" "$FW|firewall"; do
  id="${c%|*}"; nm="${c#*|}"
  RO=$(docker inspect -f '{{.HostConfig.ReadonlyRootfs}}' "$id"); [ "$RO" = "true" ] && ok "$nm read-only rootfs" || bad "$nm not read-only ($RO)"
  NNP=$(docker inspect -f '{{.HostConfig.SecurityOpt}}' "$id"); echo "$NNP" | grep -q no-new-privileges && ok "$nm no-new-privileges" || bad "$nm missing no-new-privileges"
done
UAPP=$(docker inspect -f '{{.Config.User}}' "$APP"); case "$UAPP" in 0|root|"") bad "uroboros runs as root";; *) ok "uroboros non-root ($UAPP)";; esac
CAPS=$(docker inspect -f '{{.HostConfig.CapDrop}}' "$APP"); echo "$CAPS" | grep -qi ALL && ok "uroboros drops ALL caps" || bad "uroboros caps not dropped"
FWCAPA=$(docker inspect -f '{{.HostConfig.CapAdd}}' "$FW"); echo "    firewall cap_add: $FWCAPA"
echo "$FWCAPA" | grep -q NET_ADMIN && ok "firewall has NET_ADMIN (only)" || bad "firewall missing NET_ADMIN"
BINDS=$(docker inspect -f '{{range .Mounts}}{{.Type}} {{end}}' "$APP"); echo "$BINDS" | grep -q bind && bad "uroboros has a host bind mount" || ok "uroboros: no host bind mounts"

printf '\n\033[1mRESULT: %d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] && echo "Internet variant is secured: public reachable, host/LAN blocked, DB sealed." || echo "Review the FAIL lines above."
exit "$FAIL"
