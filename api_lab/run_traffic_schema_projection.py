"""One-shot, offline Traffic-to-spec path-prefix projection command."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path

from api_lab.engine import digest
from api_lab.traffic_schema_projection import DEFAULT_MAX_SCHEMA_BYTES, project_bytes


def _read_bounded(path, limit=DEFAULT_MAX_SCHEMA_BYTES):
    selected = Path(path).resolve()
    if not selected.is_file():
        raise ValueError("Traffic-to-spec artifact does not exist")
    if selected.stat().st_size > limit:
        raise ValueError("Traffic-to-spec artifact exceeds the byte limit")
    return selected.read_bytes()


def _write_new(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(descriptor, "wb") as handle:
            handle.write(data)
            handle.flush()
            os.fsync(handle.fileno())
    except Exception:
        try:
            path.unlink()
        except OSError:
            pass
        raise


def execute(input_path, output_path, input_sha256, include_prefixes):
    raw = _read_bounded(input_path)
    document = project_bytes(raw, input_sha256, include_prefixes)
    encoded = json.dumps(
        document, sort_keys=True, separators=(",", ":"), ensure_ascii=True
    ).encode("ascii") + b"\n"
    selected = Path(output_path).resolve()
    _write_new(selected, encoded)
    projection = document["x-uroboros-traffic"]["projection"]
    return {
        "contract": "uroboros-traffic-schema-projection-run-v1",
        "output": str(selected),
        "artifact_sha256": hashlib.sha256(encoded).hexdigest(),
        "schema_id": digest(document),
        "include_prefixes": projection["include_prefixes"],
        "source_counts": projection["source_counts"],
        "selected_counts": projection["selected_counts"],
        "excluded_counts": projection["excluded_counts"],
        "authority_unchanged": projection["authority_unchanged"],
        "network_requests": 0,
        "model_used": False,
    }


def main(argv=None):
    parser = argparse.ArgumentParser(
        description="Project a canonical Traffic-to-spec artifact by explicit path prefix"
    )
    parser.add_argument("--input", required=True)
    parser.add_argument("--input-sha256", required=True)
    parser.add_argument("--include-prefix", action="append", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args(argv)
    report = execute(
        args.input,
        args.output,
        args.input_sha256,
        args.include_prefix,
    )
    print(json.dumps(report, sort_keys=True, indent=2))


if __name__ == "__main__":
    main()
