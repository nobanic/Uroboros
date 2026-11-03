# Arsenal MCP sidecar — pinned scanners + baked nuclei templates + MCP server.
# Build needs egress (fetch release binaries + templates); the RUNTIME is sealed (no egress).
# Every downloaded artifact is version- and checksum-pinned. Updates create a
# new image; the running sidecar never downloads tools or templates.
ARG NUCLEI_VERSION=3.3.0
ARG HTTPX_VERSION=1.6.9
ARG FFUF_VERSION=2.1.0
ARG SUBFINDER_VERSION=2.17.0
ARG NAABU_VERSION=2.6.1
ARG NUCLEI_TEMPLATES_VERSION=10.5.0
ARG ARJUN_VERSION=2.2.7
ARG NUCLEI_ARCHIVE_SHA256=235f264d32e47e1ccf58d534e2eb4d0d4eeb47f1cae1ebb30a584b8b52565202
ARG HTTPX_ARCHIVE_SHA256=c8d36461b5d736e88c3f9104fed15f2112eb7263dbda35fd08aa5a771bddfb5f
ARG FFUF_ARCHIVE_SHA256=fc2c82736c14dcbea4daf3d3cf3878c1c4773008ba45c2bc0fceba7d17b40bb5
ARG SUBFINDER_ARCHIVE_SHA256=a39c3e5c7a26ef3c430915c4d6cbf7716a00f26769a75e9405aba796142eba37
ARG NAABU_ARCHIVE_SHA256=018c4c9884dea971eda860435ede3021d1150732f34cfd245498c6726d8cab90
ARG NUCLEI_TEMPLATES_ARCHIVE_SHA256=dd836a827b4147d7d8294ca6a89526288ccb1a4bcc895dceac7af3563bc61775
ARG ARJUN_SDIST_SHA256=b193cdaf97bf7b0e8cd91a41da778639e01fd9738d5f666a8161377f475ce72e

FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc

ARG NUCLEI_VERSION
ARG HTTPX_VERSION
ARG FFUF_VERSION
ARG SUBFINDER_VERSION
ARG NAABU_VERSION
ARG NUCLEI_TEMPLATES_VERSION
ARG ARJUN_VERSION
ARG NUCLEI_ARCHIVE_SHA256
ARG HTTPX_ARCHIVE_SHA256
ARG FFUF_ARCHIVE_SHA256
ARG SUBFINDER_ARCHIVE_SHA256
ARG NAABU_ARCHIVE_SHA256
ARG NUCLEI_TEMPLATES_ARCHIVE_SHA256
ARG ARJUN_SDIST_SHA256
ARG ARSENAL_SOURCE_REVISION=unknown

LABEL org.opencontainers.image.source="https://github.com/nobanic/UroborOS" \
      org.opencontainers.image.revision="${ARSENAL_SOURCE_REVISION}"

# naabu v2.6.1's official linux/amd64 artifact uses the glibc loader. gcompat
# supplies that ABI on the otherwise-musl Alpine runtime; the scanner remains
# pinned to the upstream archive checksum below.
RUN apk add --no-cache python3 py3-pip ca-certificates wget unzip tar gcompat && \
    addgroup -S -g 10001 arsenal && \
    adduser -S -D -H -u 10001 -G arsenal arsenal
COPY requirements.lock /opt/arsenal/requirements.lock
RUN pip install --no-cache-dir --break-system-packages --no-deps --require-hashes -r /opt/arsenal/requirements.lock

# Arjun remains an independently invoked, unmodified AGPLv3 program. Install
# the exact PyPI source artifact without allowing its unpinned dependency list
# to change the image; every dependency is version-locked above.
RUN set -eux; cd /tmp; \
    wget -q "https://files.pythonhosted.org/packages/source/a/arjun/arjun-${ARJUN_VERSION}.tar.gz" -O arjun.tar.gz; \
    echo "${ARJUN_SDIST_SHA256}  arjun.tar.gz" | sha256sum -c -; \
    tar -xzf arjun.tar.gz; \
    install -d /usr/share/licenses/arjun; \
    install -m 0444 "arjun-${ARJUN_VERSION}/LICENSE" /usr/share/licenses/arjun/LICENSE; \
    pip install --no-cache-dir --break-system-packages --no-deps ./arjun.tar.gz; \
    rm -rf arjun.tar.gz "arjun-${ARJUN_VERSION}"

# ProjectDiscovery + ffuf release binaries (linux amd64), pinned.
RUN set -eux; cd /tmp; \
    wget -q "https://github.com/projectdiscovery/nuclei/releases/download/v${NUCLEI_VERSION}/nuclei_${NUCLEI_VERSION}_linux_amd64.zip" -O n.zip; \
    wget -q "https://github.com/projectdiscovery/httpx/releases/download/v${HTTPX_VERSION}/httpx_${HTTPX_VERSION}_linux_amd64.zip" -O h.zip; \
    wget -q "https://github.com/ffuf/ffuf/releases/download/v${FFUF_VERSION}/ffuf_${FFUF_VERSION}_linux_amd64.tar.gz" -O f.tgz; \
    wget -q "https://github.com/projectdiscovery/subfinder/releases/download/v${SUBFINDER_VERSION}/subfinder_${SUBFINDER_VERSION}_linux_amd64.zip" -O s.zip; \
    wget -q "https://github.com/projectdiscovery/naabu/releases/download/v${NAABU_VERSION}/naabu_${NAABU_VERSION}_linux_amd64.zip" -O a.zip; \
    echo "${NUCLEI_ARCHIVE_SHA256}  n.zip" | sha256sum -c -; \
    echo "${HTTPX_ARCHIVE_SHA256}  h.zip" | sha256sum -c -; \
    echo "${FFUF_ARCHIVE_SHA256}  f.tgz" | sha256sum -c -; \
    echo "${SUBFINDER_ARCHIVE_SHA256}  s.zip" | sha256sum -c -; \
    echo "${NAABU_ARCHIVE_SHA256}  a.zip" | sha256sum -c -; \
    unzip -o n.zip nuclei -d /usr/local/bin; \
    unzip -o h.zip httpx  -d /usr/local/bin; \
    tar -xzf f.tgz -C /usr/local/bin ffuf; \
    unzip -o s.zip subfinder -d /usr/local/bin; \
    unzip -o a.zip naabu -d /usr/local/bin; \
    chmod +x /usr/local/bin/nuclei /usr/local/bin/httpx /usr/local/bin/ffuf /usr/local/bin/subfinder /usr/local/bin/naabu; \
    rm -f n.zip h.zip f.tgz s.zip a.zip

# Bake one immutable, HTTP-only-selectable Nuclei corpus. No swallowed updater
# error and no floating "latest" template state is allowed into the image.
RUN set -eux; cd /tmp; \
    wget -q "https://github.com/projectdiscovery/nuclei-templates/archive/refs/tags/v${NUCLEI_TEMPLATES_VERSION}.zip" -O templates.zip; \
    echo "${NUCLEI_TEMPLATES_ARCHIVE_SHA256}  templates.zip" | sha256sum -c -; \
    unzip -q templates.zip; \
    mv "nuclei-templates-${NUCLEI_TEMPLATES_VERSION}" /opt/nuclei-templates; \
    rm -f templates.zip

WORKDIR /opt/arsenal
COPY __init__.py contracts.py parsers.py execmgr.py execution.py results.py nuclei.py jobs.py diagnostics.py auth.py authmw.py taskrun.py discovery.py arjunrun.py wafpassive.py server.py policy.py register.py /opt/arsenal/arsenal/
COPY wafw00f /opt/arsenal/arsenal/wafw00f
COPY wordlist.txt /opt/arsenal/wordlist.txt
RUN install -d -m 0700 -o arsenal -g arsenal /var/lib/arsenal
ENV PYTHONPATH=/opt/arsenal \
    ARSENAL_WORDLIST=/opt/arsenal/wordlist.txt \
    NUCLEI_TEMPLATE_DIR=/opt/nuclei-templates \
    ARSENAL_SOURCE_REVISION=${ARSENAL_SOURCE_REVISION} \
    HOME=/tmp/arsenal-home \
    USER=arsenal \
    LOGNAME=arsenal \
    XDG_CONFIG_HOME=/tmp/arsenal-home/config \
    XDG_CACHE_HOME=/tmp/arsenal-home/cache \
    PYTHONDONTWRITEBYTECODE=1
# ARSENAL_TOKEN / ARSENAL_ALLOWLIST supplied at runtime (compose env).
EXPOSE 8000
USER arsenal:arsenal
CMD ["python3", "-m", "arsenal.server"]
