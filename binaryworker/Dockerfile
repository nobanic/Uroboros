FROM python:3.12.15-slim-trixie@sha256:a0f57064cbdcd6285fa45f51237ab0b35ea8ebaed06a1f5973e89e19dfb86704 AS dependencies
COPY binaryworker/requirements.lock /tmp/requirements.lock
COPY binaryworker/wheelhouse/ /tmp/wheelhouse/
COPY binaryworker/capa-requirements.lock /tmp/capa-requirements.lock
COPY binaryworker/capa-wheelhouse/ /tmp/capa-wheelhouse/
COPY binaryworker/floss-requirements.lock /tmp/floss-requirements.lock
COPY binaryworker/floss-wheelhouse/ /tmp/floss-wheelhouse/
RUN python -m pip install --no-index --no-deps --require-hashes --disable-pip-version-check \
      --target /opt/uroboros-binary-worker/site-packages \
      --find-links /tmp/wheelhouse -r /tmp/requirements.lock && \
    python -m pip install --no-index --no-deps --require-hashes --disable-pip-version-check \
      --target /opt/uroboros-binary-worker/capa-site-packages \
      --find-links /tmp/capa-wheelhouse -r /tmp/capa-requirements.lock && \
    python -m pip install --no-index --no-deps --require-hashes --disable-pip-version-check \
      --target /opt/uroboros-binary-worker/floss-site-packages \
      --find-links /tmp/floss-wheelhouse -r /tmp/floss-requirements.lock && \
    rm -f /opt/uroboros-binary-worker/floss-site-packages/floss/sigs/README.md && \
    find /opt/uroboros-binary-worker/site-packages \
      /opt/uroboros-binary-worker/capa-site-packages \
      /opt/uroboros-binary-worker/floss-site-packages \
      -type d -name __pycache__ -prune -exec rm -rf '{}' +
COPY binaryworker/capa-rules-corpus/ /opt/uroboros-binary-worker/capa-rules/
COPY binaryworker/capa-signatures-corpus/ /opt/uroboros-binary-worker/capa-signatures/
COPY binaryworker/verify_capa_resources.py /tmp/verify_capa_resources.py
COPY binaryworker/verify_floss_resources.py /tmp/verify_floss_resources.py
RUN python /tmp/verify_capa_resources.py \
      --lief-site-packages /opt/uroboros-binary-worker/site-packages \
      --lief-lock /tmp/requirements.lock \
      --capa-site-packages /opt/uroboros-binary-worker/capa-site-packages \
      --capa-lock /tmp/capa-requirements.lock \
      --rules /opt/uroboros-binary-worker/capa-rules \
      --signatures /opt/uroboros-binary-worker/capa-signatures && \
    python /tmp/verify_floss_resources.py \
      --site-packages /opt/uroboros-binary-worker/floss-site-packages \
      --lock /tmp/floss-requirements.lock \
      --signatures /opt/uroboros-binary-worker/floss-site-packages/floss/sigs && \
    find /opt/uroboros-binary-worker/capa-rules \
      /opt/uroboros-binary-worker/capa-signatures -type d -exec chmod 0555 '{}' + && \
    find /opt/uroboros-binary-worker/capa-rules \
      /opt/uroboros-binary-worker/capa-signatures -type f -exec chmod 0444 '{}' +
RUN set -eux; \
    root=/minimal; \
    install -d "$root/usr/local/bin" "$root/usr/local/lib" "$root/lib64" \
      "$root/lib/x86_64-linux-gnu" "$root/etc" "$root/var/lib/dpkg" \
      "$root/usr/share/doc/libc6" "$root/usr/share/doc/libgcc-s1" \
      "$root/usr/share/doc/libstdc++6" "$root/usr/share/doc/zlib1g" \
      "$root/usr/share/doc/libbz2-1.0" "$root/usr/share/doc/liblzma5" \
      "$root/usr/share/doc/libffi8" "$root/usr/share/doc/libsqlite3-0" \
      "$root/work"; \
    install -m 0555 /usr/local/bin/python3.12 "$root/usr/local/bin/python"; \
    install -m 0555 /usr/local/lib/libpython3.12.so.1.0 "$root/usr/local/lib/libpython3.12.so.1.0"; \
    cp -a /usr/local/lib/python3.12 "$root/usr/local/lib/python3.12"; \
    rm -rf "$root/usr/local/lib/python3.12/ensurepip" \
      "$root/usr/local/lib/python3.12/idlelib" \
      "$root/usr/local/lib/python3.12/site-packages" \
      "$root/usr/local/lib/python3.12/test" \
      "$root/usr/local/lib/python3.12/tkinter" \
      "$root/usr/local/lib/python3.12/turtledemo" \
      "$root/usr/local/lib/python3.12/venv"; \
    rm -f "$root/usr/local/lib/python3.12/lib-dynload/_hashlib"*.so \
      "$root/usr/local/lib/python3.12/lib-dynload/_ssl"*.so; \
    install -m 0555 "$(readlink -f /lib64/ld-linux-x86-64.so.2)" "$root/lib64/ld-linux-x86-64.so.2"; \
    install -m 0555 "$(readlink -f /lib/x86_64-linux-gnu/libc.so.6)" "$root/lib/x86_64-linux-gnu/libc.so.6"; \
    install -m 0555 "$(readlink -f /lib/x86_64-linux-gnu/libm.so.6)" "$root/lib/x86_64-linux-gnu/libm.so.6"; \
    install -m 0555 "$(readlink -f /lib/x86_64-linux-gnu/libpthread.so.0)" "$root/lib/x86_64-linux-gnu/libpthread.so.0"; \
    install -m 0555 "$(readlink -f /lib/x86_64-linux-gnu/librt.so.1)" "$root/lib/x86_64-linux-gnu/librt.so.1"; \
    install -m 0555 "$(readlink -f /lib/x86_64-linux-gnu/libdl.so.2)" "$root/lib/x86_64-linux-gnu/libdl.so.2"; \
    install -m 0555 "$(readlink -f /lib/x86_64-linux-gnu/libgcc_s.so.1)" "$root/lib/x86_64-linux-gnu/libgcc_s.so.1"; \
    install -m 0555 "$(readlink -f /lib/x86_64-linux-gnu/libstdc++.so.6)" "$root/lib/x86_64-linux-gnu/libstdc++.so.6"; \
    install -m 0555 "$(readlink -f /lib/x86_64-linux-gnu/libz.so.1)" "$root/lib/x86_64-linux-gnu/libz.so.1"; \
    install -m 0555 "$(readlink -f /lib/x86_64-linux-gnu/libbz2.so.1.0)" "$root/lib/x86_64-linux-gnu/libbz2.so.1.0"; \
    install -m 0555 "$(readlink -f /lib/x86_64-linux-gnu/liblzma.so.5)" "$root/lib/x86_64-linux-gnu/liblzma.so.5"; \
    install -m 0555 "$(readlink -f /lib/x86_64-linux-gnu/libffi.so.8)" "$root/lib/x86_64-linux-gnu/libffi.so.8"; \
    install -m 0555 "$(readlink -f /lib/x86_64-linux-gnu/libsqlite3.so.0)" "$root/lib/x86_64-linux-gnu/libsqlite3.so.0"; \
    install -m 0444 /etc/os-release "$root/etc/os-release"; \
    printf 'uroboros-worker:x:65532:65532:Uroboros worker:/work:/sbin/nologin\n' > "$root/etc/passwd"; \
    printf 'uroboros-worker:x:65532:\nbinary-worker:x:65533:uroboros-worker\n' > "$root/etc/group"; \
    { for package in libc6 libgcc-s1 libstdc++6 zlib1g libbz2-1.0 liblzma5 libffi8 libsqlite3-0; do dpkg-query -s "$package"; echo; done; } > "$root/var/lib/dpkg/status"; \
    install -m 0444 /usr/share/doc/libc6/copyright "$root/usr/share/doc/libc6/copyright"; \
    install -m 0444 /usr/share/doc/libgcc-s1/copyright "$root/usr/share/doc/libgcc-s1/copyright"; \
    install -m 0444 /usr/share/doc/libstdc++6/copyright "$root/usr/share/doc/libstdc++6/copyright"; \
    install -m 0444 /usr/share/doc/zlib1g/copyright "$root/usr/share/doc/zlib1g/copyright"; \
    install -m 0444 /usr/share/doc/libbz2-1.0/copyright "$root/usr/share/doc/libbz2-1.0/copyright"; \
    install -m 0444 /usr/share/doc/liblzma5/copyright "$root/usr/share/doc/liblzma5/copyright"; \
    install -m 0444 /usr/share/doc/libffi8/copyright "$root/usr/share/doc/libffi8/copyright"; \
    install -m 0444 /usr/share/doc/libsqlite3-0/copyright "$root/usr/share/doc/libsqlite3-0/copyright"

FROM scratch
ARG UROBOROS_SOURCE_REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/nobanic/UroborOS" \
      org.opencontainers.image.revision="${UROBOROS_SOURCE_REVISION}" \
      org.opencontainers.image.title="Uroboros minimal one-artifact LIEF, capa, and FLOSS worker" \
      org.opencontainers.image.licenses="AGPL-3.0-only"
COPY --from=dependencies /minimal/ /
COPY --from=dependencies /opt/uroboros-binary-worker/site-packages /opt/uroboros-binary-worker/site-packages
COPY --from=dependencies /opt/uroboros-binary-worker/capa-site-packages /opt/uroboros-binary-worker/capa-site-packages
COPY --from=dependencies /opt/uroboros-binary-worker/floss-site-packages /opt/uroboros-binary-worker/floss-site-packages
COPY --from=dependencies /opt/uroboros-binary-worker/capa-rules /opt/uroboros-binary-worker/capa-rules
COPY --from=dependencies /opt/uroboros-binary-worker/capa-signatures /opt/uroboros-binary-worker/capa-signatures
COPY --chmod=0444 binaryworker/worker.py /opt/uroboros-binary-worker/worker.py
COPY --chmod=0444 binaryworker/capa_runner.py /opt/uroboros-binary-worker/capa_runner.py
COPY --chmod=0444 binaryworker/floss_runner.py /opt/uroboros-binary-worker/floss_runner.py
COPY --chmod=0444 binaryworker/THIRD_PARTY_NOTICES.md /opt/uroboros-binary-worker/THIRD_PARTY_NOTICES.md
COPY --chmod=0444 LICENSE /usr/share/licenses/uroboros/LICENSE
COPY --chmod=0444 binaryworker/LICENSES/LIEF-Apache-2.0.txt /usr/share/licenses/lief/LICENSE
COPY --chmod=0444 binaryworker/LICENSES/LIEF-Apache-2.0.txt /usr/share/licenses/capa/LICENSE
COPY --chmod=0444 binaryworker/LICENSES/LIEF-Apache-2.0.txt /usr/share/licenses/capa-rules/LICENSE
COPY --chmod=0444 binaryworker/LICENSES/LIEF-Apache-2.0.txt /usr/share/licenses/floss/LICENSE
COPY --chmod=0444 binaryworker/LICENSES/binary2strings-MIT.txt /usr/share/licenses/binary2strings/LICENSE
COPY --chmod=0444 binaryworker/LICENSES/halo-MIT.txt /usr/share/licenses/halo/LICENSE
USER 65532:65533
WORKDIR /work
ENV HOME=/work XDG_CACHE_HOME=/work/.cache TMPDIR=/work \
    PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 LD_LIBRARY_PATH=/usr/local/lib
ENTRYPOINT ["/usr/local/bin/python", "-I", "/opt/uroboros-binary-worker/worker.py"]
