# Minimal egress firewall: Alpine + iptables. This is infrastructure, not the
# untrusted app — it holds only NET_ADMIN/NET_RAW, runs no application code, and
# the Uroboros app shares its network namespace so its traffic is filtered here.
FROM alpine:latest
RUN apk add --no-cache iptables ip6tables
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
