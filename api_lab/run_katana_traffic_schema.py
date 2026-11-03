"""One-shot, model-free Katana recorder export -> OpenAPI command."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path

from api_lab.engine import digest
from api_lab.katana_traffic_schema import DEFAULT_MAX_ARCHIVE_BYTES, infer_from_katana
from api_lab.runtime_evidence import DEFAULT_MAX_BYTES


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


def _read_bounded(path, limit, label):
    selected = Path(path).resolve()
    if not selected.is_file():
        raise ValueError(label + " does not exist")
    if selected.stat().st_size > limit:
        raise ValueError(label + " exceeds the byte limit")
    return selected.read_bytes()


def execute(evidence_path, recorder_path, output_path, evidence_sha256, recorder_sha256):
    evidence = _read_bounded(evidence_path, DEFAULT_MAX_BYTES, "Katana evidence")
    recorder = _read_bounded(recorder_path, DEFAULT_MAX_ARCHIVE_BYTES, "recorder export")
    document = infer_from_katana(
        evidence,
        recorder,
        expected_evidence_sha256=evidence_sha256,
        expected_recorder_sha256=recorder_sha256,
    )
    encoded = json.dumps(document, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("ascii") + b"\n"
    selected = Path(output_path).resolve()
    _write_new(selected, encoded)
    source = document["x-uroboros-traffic"]["source"]
    return {
        "contract": "uroboros-katana-traffic-schema-run-v1",
        "output": str(selected),
        "artifact_sha256": hashlib.sha256(encoded).hexdigest(),
        "schema_id": digest(document),
        "path_count": len(document["paths"]),
        "operation_count": sum(len(methods) for methods in document["paths"].values()),
        "selected_exchanges": source["selected_exchanges"],
        "ignored_proxy_only_exchanges": source["ignored_proxy_only_exchanges"],
        "guard_allowed_without_durable_record": source["guard_allowed_without_durable_record"],
        "network_requests": 0,
        "model_used": False,
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description="Infer OpenAPI offline from Katana-confirmed recorder exchanges")
    parser.add_argument("--evidence", required=True)
    parser.add_argument("--evidence-sha256", required=True)
    parser.add_argument("--recorder-export", required=True)
    parser.add_argument("--recorder-sha256", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args(argv)
    report = execute(
        args.evidence,
        args.recorder_export,
        args.output,
        args.evidence_sha256,
        args.recorder_sha256,
    )
    print(json.dumps(report, sort_keys=True, indent=2))


if __name__ == "__main__":
    main()
