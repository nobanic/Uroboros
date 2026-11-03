"""Model-free live smoke gate for the local Schemathesis shadow runner."""

from __future__ import annotations

import argparse
import hashlib
import json
import uuid
from pathlib import Path

from api_lab.engine import digest
from api_lab.run_katana_shadow import DEFAULT_GUARD_IMAGE
from api_lab.run_schemathesis_shadow import execute
from api_lab.runtime_evidence import loads_bundle
from api_lab.schemathesis_shadow import make_plan
from api_lab.traffic_schema import infer
from api_lab.verify_katana_shadow import (
    DEFAULT_FIXTURE_IMAGE,
    _docker,
    _remove,
    _run_support,
    _write_new,
)


def _observation(exchange_id, path, response, json_present):
    return {
        "exchange_id": exchange_id,
        "timestamp": 1 if exchange_id.endswith("1") else 2,
        "method": "GET",
        "url": "http://fixture:8080" + path,
        "status": 200,
        "request_json_present": False,
        "response_json_present": json_present,
        "response_json": response if json_present else None,
        "request_body_sha256": digest(None),
        "response_body_sha256": digest(response),
        "request_head_sha256": "a" * 64,
        "response_head_sha256": "b" * 64,
        "identity_ref": "anonymous",
    }


def _schema():
    return infer(
        "http://fixture:8080",
        [
            _observation("smoke-flow-1", "/api/runtime", {"runtime": True, "fixture_secret": "shape-only"}, True),
            _observation("smoke-flow-2", "/api/static-candidate", None, False),
        ],
        provenance={"kind": "synthetic_schemathesis_shadow_smoke", "selected_exchanges": 2},
    )


def _canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("ascii") + b"\n"


def verify(output_dir, *, fixture_image=DEFAULT_FIXTURE_IMAGE, guard_image=DEFAULT_GUARD_IMAGE):
    destination = Path(output_dir).resolve()
    destination.mkdir(parents=True, exist_ok=True)
    token = uuid.uuid4().hex[:10]
    network = "uroboros-schemathesis-smoke-" + token
    target = "uroboros-schemathesis-target-" + token
    recorder = "uroboros-schemathesis-recorder-" + token
    run_id = "schemathesis-smoke-" + token
    for image in (fixture_image, guard_image):
        _docker(["image", "inspect", image])
    _docker([
        "network", "create", "--internal",
        "--label", "uroboros.schemathesis-shadow-smoke=" + token,
        network,
    ])
    target_log = recorder_log = b""
    error = None
    try:
        _run_support(
            target,
            network,
            fixture_image,
            ["target", "--listen", "0.0.0.0:8080"],
            alias="fixture",
        )
        _run_support(
            recorder,
            network,
            fixture_image,
            [
                "recorder", "--listen", "127.0.0.1:8788",
                "--target-origin", "http://fixture:8080",
            ],
        )
        document = _schema()
        raw_schema = _canonical(document)
        schema_hash = hashlib.sha256(raw_schema).hexdigest()
        schema_path = destination / (run_id + ".openapi.json")
        _write_new(schema_path, document)
        selected = make_plan(
            document=document,
            schema_sha256=schema_hash,
            target_origin="http://fixture:8080",
            run_id=run_id,
            target_revision="synthetic-fixture-v1",
            scope_ref="authorization:synthetic-fixture",
            network_container=recorder,
            max_requests=40,
            max_seconds=10,
            max_concurrency=1,
            rate_limit_per_second=2,
            max_examples_per_operation=2,
            max_failures=5,
            request_timeout=3,
            max_response_bytes=262144,
            seed=7,
        )
        report = execute(selected, schema_path, destination, guard_image=guard_image)
        evidence_path = destination / report["evidence"]["name"]
        bundle = loads_bundle(evidence_path.read_bytes(), report["evidence"]["bundle_sha256"])
        response_nodes = [node for node in bundle["nodes"] if node["kind"] == "response_observation"]
        request_nodes = [node for node in bundle["nodes"] if node["kind"] == "request_observation"]
        paths = sorted({node["data"]["path"] for node in bundle["nodes"] if node["kind"] == "endpoint"})
        if not response_nodes or any("traffic_id" not in node["data"] for node in response_nodes):
            raise RuntimeError("Schemathesis response evidence lacks recording-proxy lineage")
        if any(node["data"].get("method") not in {"GET", "HEAD", "OPTIONS"} for node in request_nodes):
            raise RuntimeError("a state-changing request entered Schemathesis target evidence")
        if bundle["summary"]["claims"]["findings"] != 0:
            raise RuntimeError("Schemathesis shadow smoke unexpectedly promoted a finding")
        result = {
            "contract": "uroboros-schemathesis-shadow-smoke-v1",
            "passed": True,
            "network_internal": True,
            "run_id": run_id,
            "schema_sha256": schema_hash,
            "bundle_sha256": report["evidence"]["bundle_sha256"],
            "paths": paths,
            "summary": bundle["summary"],
            "scope_guard": bundle["manifest"]["coverage"]["scope_guard"],
            "assertions": {
                "proxy_lineage_present": True,
                "read_only_target_evidence": True,
                "findings_promoted": 0,
            },
        }
    except Exception as exc:
        error = exc
        result = {"contract": "uroboros-schemathesis-shadow-smoke-v1", "passed": False, "error": str(exc)}
    finally:
        recorder_log = _remove(recorder)
        target_log = _remove(target)
        _docker(["network", "rm", network], check=False)
    mutation_reached = b'"mutation":true' in target_log
    result.setdefault("assertions", {})["mutation_reached_target"] = mutation_reached
    result["support_logs"] = {
        "target_sha256": hashlib.sha256(target_log).hexdigest(),
        "recorder_sha256": hashlib.sha256(recorder_log).hexdigest(),
    }
    if mutation_reached:
        result["passed"] = False
        error = RuntimeError("scope guard allowed a mutation to reach the fixture")
        result["error"] = str(error)
    report_path = _write_new(destination / ("schemathesis-smoke-%s.json" % token), result)
    if error is not None:
        raise RuntimeError(str(error) + "; smoke report: " + str(report_path)) from error
    return report_path, result


def main(argv=None):
    parser = argparse.ArgumentParser(description="Run the model-free Schemathesis shadow smoke gate")
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--fixture-image", default=DEFAULT_FIXTURE_IMAGE)
    parser.add_argument("--guard-image", default=DEFAULT_GUARD_IMAGE)
    args = parser.parse_args(argv)
    path, result = verify(args.output_dir, fixture_image=args.fixture_image, guard_image=args.guard_image)
    print(json.dumps({"report": str(path), **result}, sort_keys=True, indent=2))


if __name__ == "__main__":
    main()
