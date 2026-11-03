# syntax=docker/dockerfile:1
FROM debian:13.7-slim@sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad

ARG TOR_VERSION=0.4.9.14-0+deb13u1
ARG TORRC_FILE=torrc

# Debian's package creates the unprivileged debian-tor account. Exact Tor and
# GeoIP package versions are required; no runtime updater is present.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        "tor=${TOR_VERSION}" "tor-geoipdb=${TOR_VERSION}" ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && install -d -o debian-tor -g debian-tor -m 0700 /var/lib/tor /run/tor

# The default Compose uses torrc. Hardened deployment overlays may select an
# equally reviewed fixed-address config from this build context without a
# runtime host bind mount.
COPY --chown=debian-tor:debian-tor ${TORRC_FILE} /etc/tor/torrc

USER debian-tor:debian-tor
EXPOSE 9050
HEALTHCHECK --interval=5s --timeout=3s --start-period=15s --retries=12 \
  CMD grep -q ':235A ' /proc/net/tcp || exit 1
ENTRYPOINT ["/usr/bin/tor", "-f", "/etc/tor/torrc"]
