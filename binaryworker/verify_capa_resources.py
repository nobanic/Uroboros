#!/usr/bin/env python3
"""Fail a worker build if its locked Python or capa resources drift."""

from __future__ import annotations

import argparse
import hashlib
import importlib.metadata
import pathlib
import re


EXPECTED_RULES = (1045, 836_221, "1910582fdcc7fd0bff198cfe07f4f1e3ceaf3acbd0c840d52d6507009d2bb5e2")
EXPECTED_SIGNATURES = (3, 15_179_585, "1b28cbecf167dbf458268ba0c282202d1347f663bba3a6089e0846391f3e083e")
FORBIDDEN_DISTRIBUTIONS = {
    "anyio",
    "certifi",
    "h11",
    "httpcore",
    "httpx",
    "ida-hcli",
    "ida-netnode",
    "ida-settings",
    "idapro",
    "pip",
    "platformdirs",
    "questionary",
    "rich-click",
    "tenacity",
    "truststore",
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
            raise SystemExit(f"invalid lock line in {path}: {line}")
        name = canonical_name(match.group(1))
        if name in expected:
            raise SystemExit(f"duplicate locked distribution: {name}")
        expected[name] = match.group(2)
    return expected


def verify_distributions(site_packages: pathlib.Path, locks: list[pathlib.Path]) -> None:
    expected: dict[str, str] = {}
    for lock in locks:
        for name, version in read_lock(lock).items():
            if name in expected:
                raise SystemExit(f"distribution appears in more than one lock: {name}")
            expected[name] = version

    installed: dict[str, str] = {}
    for distribution in importlib.metadata.distributions(path=[str(site_packages)]):
        name = canonical_name(distribution.metadata["Name"])
        installed[name] = distribution.version

    if installed != expected:
        missing = sorted(set(expected) - set(installed))
        extra = sorted(set(installed) - set(expected))
        wrong = sorted(name for name in expected.keys() & installed.keys() if expected[name] != installed[name])
        raise SystemExit(f"installed distribution closure mismatch: missing={missing} extra={extra} wrong={wrong}")
    forbidden = sorted(FORBIDDEN_DISTRIBUTIONS & installed.keys())
    if forbidden:
        raise SystemExit(f"forbidden network/update/IDA distributions installed: {forbidden}")


def corpus_identity(root: pathlib.Path, extensions: set[str]) -> tuple[int, int, str]:
    all_files = [path for path in root.rglob("*") if path.is_file()]
    unexpected = sorted(path.relative_to(root).as_posix() for path in all_files if path.suffix.lower() not in extensions)
    if unexpected:
        raise SystemExit(f"unexpected file(s) in {root}: {unexpected[:8]}")
    files = sorted(all_files, key=lambda path: path.relative_to(root).as_posix())
    manifest = "".join(
        f"{path.relative_to(root).as_posix()}\t{path.stat().st_size}\t{hashlib.sha256(path.read_bytes()).hexdigest()}\n"
        for path in files
    ).encode("utf-8")
    return len(files), sum(path.stat().st_size for path in files), hashlib.sha256(manifest).hexdigest()


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--lief-site-packages", required=True, type=pathlib.Path)
    parser.add_argument("--lief-lock", required=True, type=pathlib.Path)
    parser.add_argument("--capa-site-packages", required=True, type=pathlib.Path)
    parser.add_argument("--capa-lock", required=True, type=pathlib.Path)
    parser.add_argument("--rules", required=True, type=pathlib.Path)
    parser.add_argument("--signatures", required=True, type=pathlib.Path)
    args = parser.parse_args()

    verify_distributions(args.lief_site_packages, [args.lief_lock])
    verify_distributions(args.capa_site_packages, [args.capa_lock])
    actual_rules = corpus_identity(args.rules, {".yml"})
    actual_signatures = corpus_identity(args.signatures, {".sig", ".pat"})
    if actual_rules != EXPECTED_RULES:
        raise SystemExit(f"capa rules identity mismatch: {actual_rules}")
    if actual_signatures != EXPECTED_SIGNATURES:
        raise SystemExit(f"capa signatures identity mismatch: {actual_signatures}")
    print(
        "verified capa 9.4.0 resources: "
        f"lief_distributions={len(list(importlib.metadata.distributions(path=[str(args.lief_site_packages)])))} "
        f"capa_distributions={len(list(importlib.metadata.distributions(path=[str(args.capa_site_packages)])))} "
        f"rules={actual_rules[0]} signatures={actual_signatures[0]}"
    )


if __name__ == "__main__":
    main()
