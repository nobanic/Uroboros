"""One-shot offline CLI for Schemathesis to API Lab draft proposals."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path

from api_lab.schemathesis_api_lab import (
    DEFAULT_MAX_REPORT_BYTES,
    build_proposals,
    dumps_proposals,
)
from api_lab.traffic_schema_projection import DEFAULT_MAX_SCHEMA_BYTES


DEFAULT_MAX_EVIDENCE_BYTES = 64 * 1024 * 1024


def _read_bounded(path, limit, label):
    selected = Path(path).resolve()
    if not selected.is_file():
        raise ValueError(label + " does not exist")
    if selected.stat().st_size > limit:
        raise ValueError(label + " exceeds the byte limit")
    return selected.read_bytes()


def _write_new(path, data):
    selected = Path(path).resolve()
    selected.parent.mkdir(parents=True, exist_ok=True)
    descriptor = os.open(selected, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(descriptor, "wb") as handle:
            handle.write(data)
            handle.flush()
            os.fsync(handle.fileno())
    except Exception:
        try:
            selected.unlink()
        except OSError:
            pass
        raise
    return selected


def execute(
    schema_path,
    schema_sha256,
    report_path,
    report_sha256,
    evidence_path,
    evidence_sha256,
    output_path,
):
    schema = _read_bounded(schema_path, DEFAULT_MAX_SCHEMA_BYTES, "Traffic-to-spec schema")
    report = _read_bounded(report_path, DEFAULT_MAX_REPORT_BYTES, "Schemathesis report")
    evidence = _read_bounded(evidence_path, DEFAULT_MAX_EVIDENCE_BYTES, "runtime evidence bundle")
    proposals = build_proposals(
        schema,
        schema_sha256,
        report,
        report_sha256,
        evidence,
        evidence_sha256,
    )
    encoded = dumps_proposals(proposals)
    destination = _write_new(output_path, encoded)
    return {
        "contract": "uroboros-schemathesis-api-lab-proposal-run-v1",
        "output": str(destination),
        "artifact_sha256": hashlib.sha256(encoded).hexdigest(),
        "draft_experiments": len(proposals["entries"]),
        "eligible_server_error_cases": proposals["summary"]["eligible_server_error_cases"],
        "ignored_reasons": proposals["summary"]["ignored_reasons"],
        "network_requests": 0,
        "model_used": False,
        "automatic_execution": False,
        "findings": 0,
    }


def main(argv=None):
    parser = argparse.ArgumentParser(
        description="Build offline API Lab draft plans from proxy-confirmed Schemathesis server errors"
    )
    parser.add_argument("--schema", required=True)
    parser.add_argument("--schema-sha256", required=True)
    parser.add_argument("--report", required=True)
    parser.add_argument("--report-sha256", required=True)
    parser.add_argument("--evidence", required=True)
    parser.add_argument("--evidence-sha256", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args(argv)
    result = execute(
        args.schema,
        args.schema_sha256,
        args.report,
        args.report_sha256,
        args.evidence,
        args.evidence_sha256,
        args.output,
    )
    print(json.dumps(result, sort_keys=True, indent=2))


if __name__ == "__main__":
    main()
