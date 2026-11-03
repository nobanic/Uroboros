#!/usr/bin/env python3
"""Fixed-policy capa entrypoint; no operator/model flags cross this boundary."""

from __future__ import annotations

import os
import sys
from pathlib import Path

CAPA_SITE_PACKAGES = "/opt/uroboros-binary-worker/capa-site-packages"
CAPA_RULES = "/opt/uroboros-binary-worker/capa-rules"
CAPA_SIGNATURES = "/opt/uroboros-binary-worker/capa-signatures"


def main() -> int:
    if len(sys.argv) != 2:
        return 64
    artifact = Path(sys.argv[1])
    try:
        resolved = artifact.resolve(strict=True)
    except OSError:
        return 64
    work = Path("/work").resolve(strict=True)
    if work not in resolved.parents or not resolved.is_file():
        return 64

    # Never inherit capa's opt-in Vivisect workspace persistence switch. The
    # raw artifact is read-only and all cache/scratch state remains ephemeral.
    os.environ["CAPA_SAVE_WORKSPACE"] = "0"
    sys.path.insert(0, CAPA_SITE_PACKAGES)
    import capa.main  # type: ignore[import-not-found]

    return int(capa.main.main([
        "--json",
        "--color", "never",
        "--rules", CAPA_RULES,
        "--signatures", CAPA_SIGNATURES,
        str(resolved),
    ]))


if __name__ == "__main__":
    raise SystemExit(main())
