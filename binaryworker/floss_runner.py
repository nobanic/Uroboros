#!/usr/bin/env python3
"""Fixed-policy FLOSS entrypoint; no operator/model flags cross this boundary."""

from __future__ import annotations

import os
import sys
from pathlib import Path

FLOSS_SITE_PACKAGES = "/opt/uroboros-binary-worker/floss-site-packages"
FLOSS_SIGNATURES = "/opt/uroboros-binary-worker/capa-signatures"


def main() -> int:
    if len(sys.argv) != 2:
        return 64
    artifact = Path(sys.argv[1])
    try:
        resolved = artifact.resolve(strict=True)
        work = Path("/work").resolve(strict=True)
    except OSError:
        return 64
    if work not in resolved.parents or not resolved.is_file():
        return 64

    # Vivisect workspace input and persistence are never part of automatic
    # triage. The fixed host profile admits only the raw, read-only artifact.
    os.environ["FLOSS_LOAD_VIV_WORKSPACE"] = "0"
    os.environ["FLOSS_SAVE_WORKSPACE"] = "0"
    sys.path.insert(0, FLOSS_SITE_PACKAGES)
    import floss.main  # type: ignore[import-not-found]

    return int(floss.main.main([
        "--only", "decoded",
        "--disable-progress",
        "--color", "never",
        "--large-file",
        "--language", "none",
        "--signatures", FLOSS_SIGNATURES,
        "--json",
        str(resolved),
    ]))


if __name__ == "__main__":
    raise SystemExit(main())
