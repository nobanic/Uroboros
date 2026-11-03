#!/usr/bin/env python3
"""Fail a worker build if the FLOSS closure or embedded signatures drift."""

from __future__ import annotations

import argparse
import hashlib
import importlib.metadata
import pathlib
import re

EXPECTED_SIGNATURES = (3, 15_179_585, "1b28cbecf167dbf458268ba0c282202d1347f663bba3a6089e0846391f3e083e")
FORBIDDEN_DISTRIBUTIONS = {
    "anyio", "certifi", "httpcore", "httpx", "pip", "platformdirs",
    "pre-commit", "pytest", "requests", "setuptools", "urllib3", "wheel",
}


def canonical_name(name: str) -> str:
    return re.sub(r"[-_.]+", "-", name).lower()


def read_lock(path: pathlib.Path) -> dict[str, str]:
    expected: dict[str, str] = {}
    pattern = re.compile(r"^([^= ]+)==([^ ]+) --hash=sha256:[0-9a-f]{64}$")
    for raw_line in path.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        match = pattern.fullmatch(line)
        if match is None:
            raise SystemExit(f"invalid lock line: {line}")
        name = canonical_name(match.group(1))
        if name in expected:
            raise SystemExit(f"duplicate locked distribution: {name}")
        expected[name] = match.group(2)
    return expected


def corpus_identity(root: pathlib.Path) -> tuple[int, int, str]:
    files = sorted(
        (path for path in root.iterdir() if path.is_file()),
        key=lambda path: path.name,
    )
    unexpected = [path.name for path in files if path.suffix.lower() != ".sig"]
    if unexpected:
        raise SystemExit(f"unexpected FLOSS signature resources: {unexpected}")
    manifest = "".join(
        f"{path.name}\t{path.stat().st_size}\t{hashlib.sha256(path.read_bytes()).hexdigest()}\n"
        for path in files
    ).encode("utf-8")
    return len(files), sum(path.stat().st_size for path in files), hashlib.sha256(manifest).hexdigest()


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--site-packages", required=True, type=pathlib.Path)
    parser.add_argument("--lock", required=True, type=pathlib.Path)
    parser.add_argument("--signatures", required=True, type=pathlib.Path)
    args = parser.parse_args()

    expected = read_lock(args.lock)
    installed = {
        canonical_name(distribution.metadata["Name"]): distribution.version
        for distribution in importlib.metadata.distributions(path=[str(args.site_packages)])
    }
    if installed != expected:
        missing = sorted(set(expected) - set(installed))
        extra = sorted(set(installed) - set(expected))
        wrong = sorted(name for name in expected.keys() & installed.keys() if expected[name] != installed[name])
        raise SystemExit(f"FLOSS distribution closure mismatch: missing={missing} extra={extra} wrong={wrong}")
    forbidden = sorted(FORBIDDEN_DISTRIBUTIONS & installed.keys())
    if forbidden:
        raise SystemExit(f"forbidden FLOSS distributions installed: {forbidden}")
    signatures = corpus_identity(args.signatures)
    if signatures != EXPECTED_SIGNATURES:
        raise SystemExit(f"FLOSS signature identity mismatch: {signatures}")
    viv_workspaces = sorted(path.as_posix() for path in args.site_packages.rglob("*.viv"))
    if viv_workspaces:
        raise SystemExit(f"unexpected Vivisect workspaces: {viv_workspaces[:8]}")
    print(f"verified FLOSS 3.2.0 resources: distributions={len(installed)} signatures={signatures[0]}")


if __name__ == "__main__":
    main()
