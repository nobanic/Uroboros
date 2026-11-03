#!/bin/sh
# Egress firewall for the "internet" sandbox variant.
#
# The Uroboros app shares THIS container's network namespace (network_mode:
# service:firewall), so every packet the app sends is filtered by these rules.
#
# Policy is DEFAULT-DENY (fail-safe): the OUTPUT chain policy is DROP, and we
# explicitly ALLOW only loopback + DNS, the DB subnet, established flows, and the
# public internet — while explicitly dropping the host, the LAN, and all other
# private ranges BEFORE the public catch-all. If any ACCEPT rule failed to load,
# traffic would stay blocked rather than leak. Kernel forwarding and IPv6 egress
# are both disabled so they cannot bypass this policy.
set -eu

# Space-separated private subnets to permit (e.g. the internal DB network).
ALLOW_SUBNETS="${ALLOW_SUBNETS:-}"

iptables -F OUTPUT
iptables -P OUTPUT DROP                                          # default DENY (fail-safe)
iptables -A OUTPUT -o lo -j ACCEPT
iptables -A OUTPUT -d 127.0.0.0/8 -j ACCEPT                      # embedded DNS @127.0.0.11 + loopback
iptables -A OUTPUT -m state --state ESTABLISHED,RELATED -j ACCEPT
for s in $ALLOW_SUBNETS; do
  iptables -A OUTPUT -d "$s" -j ACCEPT                           # DB subnet (private, explicitly allowed)
done
for s in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 169.254.0.0/16 100.64.0.0/10; do
  iptables -A OUTPUT -d "$s" -j DROP                             # host + LAN + metadata: blocked (all protocols)
done
iptables -A OUTPUT -j ACCEPT                                     # anything left = public internet
iptables -P FORWARD DROP                                         # never route between interfaces

# Disable IPv6 egress and forwarding entirely so they cannot bypass the v4 policy.
if command -v ip6tables >/dev/null 2>&1; then
  ip6tables -F OUTPUT 2>/dev/null || true
  ip6tables -A OUTPUT -o lo -j ACCEPT 2>/dev/null || true
  ip6tables -P OUTPUT DROP 2>/dev/null || true
  ip6tables -P FORWARD DROP 2>/dev/null || true
fi

echo "[firewall] default-DROP egress allowlist applied. ALLOW_SUBNETS='${ALLOW_SUBNETS}'."
echo "[firewall] public allowed; host/LAN/private + IPv6 + forwarding dropped."
iptables -S OUTPUT
exec tail -f /dev/null
