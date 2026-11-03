"""Model-free vulnerable/fixed smoke gate for the Schemathesis WFC runner."""

from __future__ import annotations

import argparse
import hashlib
import json
import tempfile
import time
import uuid
from pathlib import Path

from api_lab.engine import digest
from api_lab.run_katana_shadow import DEFAULT_GUARD_IMAGE
from api_lab.run_schemathesis_wfc import execute as execute_wfc
from api_lab.run_schemathesis_wfc_api_lab import execute as build_api_lab_drafts
from api_lab.runtime_evidence import loads_bundle
from api_lab.schemathesis_wfc import (
    SCHEMATHESIS_IMAGE,
    load_identity_material,
    make_plan,
)
from api_lab.schemathesis_wfc_api_lab import POLICY_CONTRACT
from api_lab.traffic_schema import infer
from api_lab.verify_katana_shadow import (
    DEFAULT_FIXTURE_IMAGE,
    _docker,
    _remove,
    _run_support,
    _write_new,
)


ORIGIN = "http://fixture:8080"


def _observation(exchange_id, timestamp, method, path, body=None, response=None, status=200):
    return {
        "exchange_id": exchange_id,
        "timestamp": timestamp,
        "method": method,
        "url": ORIGIN + path,
        "status": status,
        "request_json_present": body is not None,
        "request_json": body,
        "response_json_present": response is not None,
        "response_json": response,
        "request_body_sha256": digest(body),
        "response_body_sha256": digest(response),
        "request_head_sha256": "a" * 64,
        "response_head_sha256": "b" * 64,
        "identity_ref": "alice",
    }


def _schema():
    note = {"id": 1, "owner": "alice", "title": "seed", "secret": "private"}
    return infer(
        ORIGIN,
        [
            _observation("wfc-smoke-create", 1, "POST", "/notes", {"title": "seed", "secret": "private"}, note, 201),
            _observation("wfc-smoke-read", 2, "GET", "/notes/1", response=note),
        ],
        provenance={"kind": "synthetic_schemathesis_wfc_smoke", "selected_exchanges": 2},
    )


def _canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("ascii") + b"\n"


def _identity_bytes():
    return json.dumps({
        "identities": {
            "alice": {"origins": [ORIGIN], "headers": {"Authorization": "Bearer fixture-alice"}},
            "bob": {"origins": [ORIGIN], "headers": {"Authorization": "Bearer fixture-bob"}},
        }
    }, sort_keys=True, separators=(",", ":")).encode("ascii") + b"\n"


def _policy_bytes(document):
    return _canonical({
        "contract": POLICY_CONTRACT,
        "target_origin": ORIGIN,
        "rules": [{
            "rule_id": "fixture-private-note",
            "producer_operation_id": document["paths"]["/notes"]["post"]["operationId"],
            "read_operation_id": document["paths"]["/notes/{id}"]["get"]["operationId"],
            "path_parameter": "id",
            "object_pointer": "/id",
            "principal_pointer": "/owner",
            "private_fields": ["/secret"],
            "claim": "A private note may only be read by its owner",
            "policy_source": "operator:synthetic-fixture-contract",
        }],
    })


def _start_target(name, network, mode):
    _docker([
        "run", "-d", "--pull", "never", "--name", name,
        "--network", network, "--network-alias", "fixture",
        "--read-only", "--tmpfs", "/tmp:rw,nosuid,nodev,size=16m",
        "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
        "--pids-limit", "64", "--memory", "96m", "--cpus", "0.5",
        "--user", "65532:65532", "--env", "HOME=/tmp",
        SCHEMATHESIS_IMAGE,
        "python", "-m", "api_lab.fixture", "--mode", mode, "--port", "8080",
    ])
    deadline = time.monotonic() + 8
    while time.monotonic() < deadline:
        state = _docker(["inspect", "--format", "{{.State.Running}}", name], check=False)
        if state.returncode == 0 and state.stdout.strip().lower() == "true":
            time.sleep(0.25)
            return
        time.sleep(0.1)
    raise RuntimeError("WFC fixture target did not become ready")


def _run_mode(destination, temporary, token, mode, fixture_image, guard_image, document, raw_schema,
              identity_raw, identity_material, policy_path, policy_sha256):
    network = "uroboros-schemathesis-wfc-%s-%s" % (mode, token)
    target = "uroboros-schemathesis-wfc-target-%s-%s" % (mode, token)
    recorder = "uroboros-schemathesis-wfc-recorder-%s-%s" % (mode, token)
    run_id = "schemathesis-wfc-%s-%s" % (mode, token)
    _docker([
        "network", "create", "--internal",
        "--label", "uroboros.schemathesis-wfc-smoke=" + token,
        network,
    ])
    target_log = recorder_log = b""
    try:
        _start_target(target, network, mode)
        _run_support(
            recorder,
            network,
            fixture_image,
            [
                "recorder", "--listen", "127.0.0.1:8788",
                "--target-origin", ORIGIN,
                "--run-id", run_id,
            ],
        )
        schema_path = temporary / (run_id + ".openapi.json")
        identity_path = temporary / (run_id + ".identities.json")
        schema_path.write_bytes(raw_schema)
        identity_path.write_bytes(identity_raw)
        selected = make_plan(
            document=document,
            schema_sha256=hashlib.sha256(raw_schema).hexdigest(),
            target_origin=ORIGIN,
            identity_material=identity_material,
            run_id=run_id,
            target_revision="synthetic-%s-v1" % mode,
            scope_ref="authorization:synthetic-fixture",
            mutation_scope_ref="authorization:synthetic-fixture-mutations",
            reset_ref="reset:fresh-disposable-%s" % mode,
            network_container=recorder,
            max_requests=100,
            max_seconds=15,
            rate_limit_per_second=4,
            max_examples_per_operation=5,
            max_failures=5,
            request_timeout=3,
            max_response_bytes=262144,
            max_steps=2,
            seed=7,
        )
        report = execute_wfc(
            selected,
            identity_material,
            schema_path,
            identity_path,
            destination,
            guard_image=guard_image,
        )
        evidence_path = destination / report["evidence"]["name"]
        bundle = loads_bundle(evidence_path.read_bytes(), report["evidence"]["bundle_sha256"])
        raw_report_path = destination / report["artifacts"]["raw"]["name"]
        proposal_path = destination / (run_id + ".api-lab-proposals.json")
        proposal_run = build_api_lab_drafts(
            schema_path,
            hashlib.sha256(raw_schema).hexdigest(),
            raw_report_path,
            report["artifacts"]["raw"]["sha256"],
            evidence_path,
            report["evidence"]["bundle_sha256"],
            policy_path,
            policy_sha256,
            proposal_path,
        )
        proposal_document = json.loads(proposal_path.read_text(encoding="ascii"))
        if proposal_document["summary"]["claims"] != {"findings": 0, "confirmed_vulnerabilities": 0}:
            raise RuntimeError("WFC API Lab bridge promoted a claim")
        failure_types = sorted(
            node["data"]["failure_type"]
            for node in bundle["nodes"]
            if node["kind"] == "hypothesis"
        )
        request_identities = sorted({
            node["data"]["identity_ref"]
            for node in bundle["nodes"]
            if node["kind"] == "request_observation"
        })
        artifact_paths = [destination / value["name"] for value in report["artifacts"].values()]
        artifact_paths.extend([evidence_path, destination / report["run_report"]["name"], proposal_path])
        for path in artifact_paths:
            raw = path.read_bytes()
            if any(secret in raw for secret in (b"fixture-alice", b"fixture-bob")):
                raise RuntimeError("WFC smoke artifact retained a credential: " + path.name)
        guard_log = (destination / report["artifacts"]["guard_log"]["name"]).read_text(encoding="utf-8")
        if '"method":"POST"' not in guard_log or '"event":"allowed"' not in guard_log:
            raise RuntimeError("WFC scope guard did not record an allowed mutation")
        return {
            "mode": mode,
            "run_id": run_id,
            "bundle_sha256": report["evidence"]["bundle_sha256"],
            "evidence_status": report["evidence_status"],
            "summary": bundle["summary"],
            "failure_types": failure_types,
            "request_identities": request_identities,
            "scope_guard": bundle["manifest"]["coverage"]["scope_guard"],
            "claims": bundle["summary"]["claims"],
            "api_lab_draft": {
                "artifact": proposal_path.name,
                "artifact_sha256": proposal_run["artifact_sha256"],
                "eligible_bola_cases": proposal_run["eligible_bola_cases"],
                "draft_experiments": proposal_run["draft_experiments"],
                "findings": proposal_run["findings"],
            },
        }
    finally:
        recorder_log = _remove(recorder)
        target_log = _remove(target)
        _docker(["network", "rm", network], check=False)
        if any(secret in recorder_log + target_log for secret in (b"fixture-alice", b"fixture-bob")):
            raise RuntimeError("WFC support logs retained a credential")


def verify(output_dir, *, fixture_image=DEFAULT_FIXTURE_IMAGE, guard_image=DEFAULT_GUARD_IMAGE):
    destination = Path(output_dir).resolve()
    destination.mkdir(parents=True, exist_ok=True)
    token = uuid.uuid4().hex[:10]
    for image in (SCHEMATHESIS_IMAGE, fixture_image, guard_image):
        _docker(["image", "inspect", image])
    document = _schema()
    if set(document["paths"]) != {"/notes", "/notes/{id}"}:
        raise RuntimeError("Traffic-to-spec did not preserve the semantic resource identifier")
    raw_schema = _canonical(document)
    identity_raw = _identity_bytes()
    identity_material = load_identity_material(
        identity_raw,
        hashlib.sha256(identity_raw).hexdigest(),
        ORIGIN,
        ("alice", "bob"),
    )
    error = None
    results = {}
    try:
        with tempfile.TemporaryDirectory(prefix="uroboros-wfc-smoke-input-") as directory:
            temporary = Path(directory)
            policy_raw = _policy_bytes(document)
            policy_path = temporary / "private-read-policy.json"
            policy_path.write_bytes(policy_raw)
            policy_sha256 = hashlib.sha256(policy_raw).hexdigest()
            for mode in ("vulnerable", "fixed"):
                results[mode] = _run_mode(
                    destination,
                    temporary,
                    token,
                    mode,
                    fixture_image,
                    guard_image,
                    document,
                    raw_schema,
                    identity_raw,
                    identity_material,
                    policy_path,
                    policy_sha256,
                )
        bola = "ObjectLevelAuthorizationViolation"
        if bola not in results["vulnerable"]["failure_types"]:
            raise RuntimeError("vulnerable WFC smoke did not produce a BOLA hypothesis")
        if bola in results["fixed"]["failure_types"]:
            raise RuntimeError("fixed WFC smoke produced a false BOLA hypothesis")
        if any(result["claims"].get("findings") != 0 for result in results.values()):
            raise RuntimeError("WFC smoke promoted a finding")
        if results["vulnerable"]["api_lab_draft"]["draft_experiments"] != 1:
            raise RuntimeError("vulnerable WFC smoke did not produce one API Lab private-read draft")
        if results["fixed"]["api_lab_draft"]["draft_experiments"] != 0:
            raise RuntimeError("fixed WFC smoke produced an API Lab private-read draft")
        result = {
            "contract": "uroboros-schemathesis-wfc-smoke-v1",
            "passed": True,
            "network_internal": True,
            "schema_sha256": hashlib.sha256(raw_schema).hexdigest(),
            "identity_config_sha256": identity_material.source_sha256,
            "ephemeral_wfc_sha256": identity_material.wfc_sha256,
            "policy_sha256": policy_sha256,
            "credentials_archived": False,
            "runs": results,
            "assertions": {
                "semantic_id_path": True,
                "vulnerable_bola_hypothesis": True,
                "fixed_bola_hypothesis": False,
                "proxy_lineage_present": True,
                "state_changing_scope_guard": True,
                "findings_promoted": 0,
                "raw_credentials_retained": False,
                "fresh_target_per_mode": True,
                "vulnerable_private_read_drafts": 1,
                "fixed_private_read_drafts": 0,
            },
        }
    except Exception as exc:
        error = exc
        result = {
            "contract": "uroboros-schemathesis-wfc-smoke-v1",
            "passed": False,
            "error": str(exc),
            "runs": results,
        }
    report_path = _write_new(destination / ("schemathesis-wfc-smoke-%s.json" % token), result)
    if error is not None:
        raise RuntimeError(str(error) + "; smoke report: " + str(report_path)) from error
    return report_path, result


def main(argv=None):
    parser = argparse.ArgumentParser(description="Run the model-free Schemathesis WFC smoke gate")
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--fixture-image", default=DEFAULT_FIXTURE_IMAGE)
    parser.add_argument("--guard-image", default=DEFAULT_GUARD_IMAGE)
    args = parser.parse_args(argv)
    path, result = verify(args.output_dir, fixture_image=args.fixture_image, guard_image=args.guard_image)
    print(json.dumps({"report": str(path), **result}, sort_keys=True, indent=2))


if __name__ == "__main__":
    main()
