"""Operator-only one-shot runner for authenticated Schemathesis stateful shadow tests.

The command is deliberately absent from MCP, the API Lab database, and the
planner. Images must already exist locally. Raw credentials are converted to a
WFC file only inside a temporary directory and are never copied to artifacts.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import tempfile
import uuid
from datetime import datetime, timezone
from pathlib import Path

from arsenal.execmgr import Runner
from api_lab.run_katana_shadow import (
    DEFAULT_GUARD_IMAGE,
    _guard_proxy,
    _image,
    _remove_container,
    _running_container,
    _start_guard,
    _stop_guard,
    parse_guard_coverage,
)
from api_lab.run_schemathesis_shadow import _probe_schemathesis
from api_lab.runtime_evidence import (
    bundle_sha256,
    dumps_bundle,
    schemathesis_terminal_exit_reason,
)
from api_lab.schemathesis_wfc import (
    JSON_REPORT_PATH,
    REPORT_PATH,
    SCHEMATHESIS_IMAGE,
    WFC_REPORT_PATH,
    WFC_SCRIPT_PATH,
    SchemathesisWfcPlan,
    finalize_evidence,
    load_executable_schema,
    load_identity_material,
    make_plan,
    plan_document,
    redact_identity_secrets,
    schemathesis_docker_argv,
    wfc_config_bytes,
)


OUTPUT_CAP_BYTES = 24 * 1024 * 1024


def _now():
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def _sha256(data):
    return hashlib.sha256(data).hexdigest()


def _scanner_name(plan, token):
    suffix = hashlib.sha256(plan.run_id.encode("ascii")).hexdigest()[:12]
    return "uroboros-schemathesis-wfc-" + suffix + "-" + token


def _guard_name(plan, token):
    suffix = hashlib.sha256(plan.run_id.encode("ascii")).hexdigest()[:12]
    return "uroboros-schemathesis-wfc-guard-" + suffix + "-" + token


def preflight(plan, guard_image=DEFAULT_GUARD_IMAGE):
    _running_container(plan.network_container)
    schemathesis = _image(SCHEMATHESIS_IMAGE)
    guard = _image(guard_image)
    version = _probe_schemathesis(schemathesis["Id"])
    return {
        "schemathesis_image_id": schemathesis["Id"],
        "scope_guard_image_id": guard["Id"],
        "schemathesis_version_output": version,
    }


def _write_new(path, data, mode=0o600):
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, mode)
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


def _artifact(path, data):
    _write_new(path, data)
    return {"name": path.name, "bytes": len(data), "sha256": _sha256(data)}


def _read_bounded(path, label, maximum=OUTPUT_CAP_BYTES):
    if not path.is_file():
        return b""
    size = path.stat().st_size
    if size > maximum:
        raise ValueError(label + " exceeds the byte limit")
    return path.read_bytes()


def _require_new_artifact_names(destination, run_id):
    suffixes = (
        "schemathesis.ndjson",
        "schemathesis.json",
        "wfc.json",
        "wfc.sh",
        "schemathesis.stdout.txt",
        "schemathesis.stderr.txt",
        "scope-guard.jsonl",
        "evidence.json",
        "run.json",
    )
    collisions = [
        run_id + "." + suffix
        for suffix in suffixes
        if (destination / (run_id + "." + suffix)).exists()
    ]
    if collisions:
        raise ValueError("Schemathesis WFC output artifacts already exist for run_id: " + run_id)


def _load_plan(
    schema_path,
    schema_sha256,
    identity_path,
    identity_sha256,
    peers,
    *,
    target_origin,
    **values,
):
    selected_schema = Path(schema_path).resolve()
    selected_identities = Path(identity_path).resolve()
    if not selected_schema.is_file():
        raise ValueError("schema path does not name a file")
    if not selected_identities.is_file():
        raise ValueError("identity path does not name a file")
    document, _ = load_executable_schema(selected_schema.read_bytes(), schema_sha256)
    material = load_identity_material(
        selected_identities.read_bytes(),
        identity_sha256,
        target_origin,
        peers,
    )
    plan = make_plan(
        document=document,
        schema_sha256=schema_sha256,
        target_origin=target_origin,
        identity_material=material,
        **values,
    )
    return plan, material


def execute(plan, identity_material, schema_path, identity_path, output_dir, *, guard_image=DEFAULT_GUARD_IMAGE):
    """Execute one isolated WFC run and return a credential-free artifact index."""
    destination = Path(output_dir).resolve()
    if destination.exists() and not destination.is_dir():
        raise ValueError("output directory is not a directory")
    _require_new_artifact_names(destination, plan.run_id)

    source_schema = Path(schema_path).resolve()
    source_identities = Path(identity_path).resolve()
    if not source_schema.is_file() or not source_identities.is_file():
        raise ValueError("schema or identity path does not name a file")
    schema_bytes = source_schema.read_bytes()
    document, _ = load_executable_schema(schema_bytes, plan.schema_sha256)
    refreshed_material = load_identity_material(
        source_identities.read_bytes(),
        plan.identity_config_sha256,
        plan.target_origin,
        plan.identity_refs,
    )
    if refreshed_material != identity_material:
        raise ValueError("identity configuration changed after WFC plan creation")
    reconstructed = make_plan(
        document=document,
        schema_sha256=plan.schema_sha256,
        target_origin=plan.target_origin,
        identity_material=identity_material,
        run_id=plan.run_id,
        target_revision=plan.target_revision,
        scope_ref=plan.scope_ref,
        mutation_scope_ref=plan.mutation_scope_ref,
        reset_ref=plan.reset_ref,
        network_container=plan.network_container,
        recording_proxy=plan.recording_proxy,
        max_requests=plan.max_requests,
        max_seconds=plan.max_seconds,
        rate_limit_per_second=plan.rate_limit_per_second,
        max_examples_per_operation=plan.max_examples_per_operation,
        max_failures=plan.max_failures,
        request_timeout=plan.request_timeout,
        max_response_bytes=plan.max_response_bytes,
        max_steps=plan.max_steps,
        seed=plan.seed,
    )
    if reconstructed != plan:
        raise ValueError("schema or identity inputs no longer reproduce the approved WFC plan")
    state = preflight(plan, guard_image)
    token = uuid.uuid4().hex[:8]
    guard_name = _guard_name(plan, token)
    scanner_name = _scanner_name(plan, token)
    guard_proxy = _guard_proxy(plan)
    started_at = _now()
    guard_log = b""
    runner_result = None
    captured = {}

    with tempfile.TemporaryDirectory(prefix="uroboros-schemathesis-wfc-") as temporary:
        workspace = Path(temporary)
        staged_schema = workspace / "openapi.json"
        staged_auth = workspace / "auth.json"
        staged_config = workspace / "schemathesis.toml"
        report_dir = workspace / "report"
        report_dir.mkdir(mode=0o777)
        try:
            os.chmod(report_dir, 0o777)
        except OSError:
            pass
        _write_new(staged_schema, schema_bytes, mode=0o444)
        _write_new(staged_auth, identity_material.wfc_bytes, mode=0o444)
        _write_new(staged_config, wfc_config_bytes(plan), mode=0o444)
        try:
            _start_guard(
                plan,
                state["scope_guard_image_id"],
                guard_name,
                guard_proxy,
                allowed_methods=plan.allowed_methods,
            )
            command = schemathesis_docker_argv(
                plan,
                container_name=scanner_name,
                schema_path=staged_schema,
                auth_path=staged_auth,
                config_path=staged_config,
                output_dir=report_dir,
                identity_material=identity_material,
                image_ref=state["schemathesis_image_id"],
                guard_proxy=guard_proxy,
            )
            runner_result = Runner().run(
                command,
                timeout=plan.max_seconds + 30,
                cap=OUTPUT_CAP_BYTES,
            )
        finally:
            _remove_container(scanner_name)
            guard_log = _stop_guard(guard_name)
        captured = {
            "raw": _read_bounded(report_dir / Path(REPORT_PATH).name, "Schemathesis NDJSON report"),
            "json": _read_bounded(report_dir / Path(JSON_REPORT_PATH).name, "Schemathesis JSON report"),
            "wfc": _read_bounded(report_dir / Path(WFC_REPORT_PATH).name, "WFC JSON report"),
            "script": _read_bounded(report_dir / Path(WFC_SCRIPT_PATH).name, "WFC reproducer script"),
        }

    finished_at = _now()
    if runner_result is None:
        raise RuntimeError("Schemathesis WFC runner did not start")
    captured["stdout"] = runner_result["stdout"].encode("utf-8", errors="replace")
    captured["stderr"] = runner_result["stderr"].encode("utf-8", errors="replace")
    captured["guard"] = guard_log
    redaction_applied = {}
    for name, data in captured.items():
        redacted = redact_identity_secrets(data, identity_material)
        redaction_applied[name] = redacted != data
        captured[name] = redacted

    prefix = plan.run_id + "."
    artifacts = {
        "raw": _artifact(destination / (prefix + "schemathesis.ndjson"), captured["raw"]),
        "json": _artifact(destination / (prefix + "schemathesis.json"), captured["json"]),
        "wfc": _artifact(destination / (prefix + "wfc.json"), captured["wfc"]),
        "script": _artifact(destination / (prefix + "wfc.sh"), captured["script"]),
        "stdout": _artifact(destination / (prefix + "schemathesis.stdout.txt"), captured["stdout"]),
        "stderr": _artifact(destination / (prefix + "schemathesis.stderr.txt"), captured["stderr"]),
        "guard_log": _artifact(destination / (prefix + "scope-guard.jsonl"), captured["guard"]),
    }
    report = {
        "contract": "uroboros-schemathesis-wfc-shadow-run-v1",
        "run_id": plan.run_id,
        "started_at": started_at,
        "finished_at": finished_at,
        "cli_exit_state": runner_result["exit_state"],
        "cli_output_truncated": bool(runner_result["truncated"]),
        "terminal_event_authoritative": False,
        "credential_redaction_applied": redaction_applied,
        "credentials_archived": False,
        "images": {
            "schemathesis_ref": SCHEMATHESIS_IMAGE,
            "schemathesis_image_id": state["schemathesis_image_id"],
            "scope_guard_image_id": state["scope_guard_image_id"],
        },
        "plan": plan_document(plan, guard_proxy),
        "artifacts": artifacts,
        "evidence": None,
    }
    evidence_error = None
    terminal_error = None
    exit_reason = None
    if not runner_result["truncated"]:
        try:
            exit_reason = schemathesis_terminal_exit_reason(captured["raw"])
            report["terminal_event_authoritative"] = True
        except Exception as exc:
            terminal_error = exc
            report["terminal_error"] = {
                "type": type(exc).__name__,
                "message_sha256": _sha256(str(exc).encode("utf-8", errors="replace")),
            }
    if exit_reason is not None:
        try:
            guard_coverage = parse_guard_coverage(
                captured["guard"],
                state["scope_guard_image_id"],
                allowed_methods=plan.allowed_methods,
            )
            evidence_truncated = exit_reason != "completed"
            bundle = finalize_evidence(
                captured["raw"],
                plan,
                started_at=started_at,
                finished_at=finished_at,
                tool_sha256=state["schemathesis_image_id"],
                guard_coverage=guard_coverage,
                exit_reason=exit_reason,
                truncated=evidence_truncated,
                guard_proxy=guard_proxy,
            )
            encoded = dumps_bundle(bundle)
            evidence = _artifact(destination / (prefix + "evidence.json"), encoded)
            evidence["bundle_sha256"] = bundle_sha256(bundle)
            report["evidence"] = evidence
            report["evidence_status"] = {
                "complete": not evidence_truncated,
                "exit_reason": exit_reason,
                "truncated": evidence_truncated,
            }
            report["summary"] = bundle["summary"]
        except Exception as exc:
            evidence_error = exc
            report["evidence_error"] = {
                "type": type(exc).__name__,
                "message_sha256": _sha256(str(exc).encode("utf-8", errors="replace")),
            }
    report_bytes = json.dumps(report, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("ascii") + b"\n"
    report_artifact = _artifact(destination / (prefix + "run.json"), report_bytes)
    report["run_report"] = report_artifact
    if terminal_error is not None:
        raise RuntimeError(
            "Schemathesis WFC report is incomplete or invalid; safe diagnostics were retained in %s" % destination
        ) from terminal_error
    if evidence_error is not None:
        raise RuntimeError(
            "Schemathesis WFC evidence finalization failed; safe diagnostics were retained in %s" % destination
        ) from evidence_error
    if report["evidence"] is None:
        raise RuntimeError("Schemathesis WFC produced no admissible evidence; diagnostics were retained in %s" % destination)
    return report


def _arguments(argv=None):
    parser = argparse.ArgumentParser(description="Run a model-free Schemathesis WFC stateful shadow test")
    parser.add_argument("command", choices=("plan", "preflight", "run"))
    parser.add_argument("--schema", required=True)
    parser.add_argument("--schema-sha256", required=True)
    parser.add_argument("--identities", required=True)
    parser.add_argument("--identities-sha256", required=True)
    parser.add_argument("--peer", action="append", required=True)
    parser.add_argument("--target", required=True)
    parser.add_argument("--target-revision", required=True)
    parser.add_argument("--scope-ref", required=True)
    parser.add_argument("--mutation-scope-ref", required=True)
    parser.add_argument("--reset-ref", required=True)
    parser.add_argument("--network-container", required=True)
    parser.add_argument("--run-id", default="schemathesis-wfc-" + uuid.uuid4().hex[:16])
    parser.add_argument("--recording-proxy", default="http://127.0.0.1:8788")
    parser.add_argument("--max-requests", type=int, default=300)
    parser.add_argument("--max-seconds", type=int, default=30)
    parser.add_argument("--rate-limit", type=int, default=4)
    parser.add_argument("--max-examples", type=int, default=10)
    parser.add_argument("--max-failures", type=int, default=20)
    parser.add_argument("--request-timeout", type=int, default=5)
    parser.add_argument("--max-response-bytes", type=int, default=1048576)
    parser.add_argument("--max-steps", type=int, default=2)
    parser.add_argument("--seed", type=int, default=1)
    parser.add_argument("--guard-image", default=DEFAULT_GUARD_IMAGE)
    parser.add_argument("--output-dir")
    return parser.parse_args(argv)


def main(argv=None):
    args = _arguments(argv)
    selected, material = _load_plan(
        args.schema,
        args.schema_sha256,
        args.identities,
        args.identities_sha256,
        args.peer,
        target_origin=args.target,
        run_id=args.run_id,
        target_revision=args.target_revision,
        scope_ref=args.scope_ref,
        mutation_scope_ref=args.mutation_scope_ref,
        reset_ref=args.reset_ref,
        network_container=args.network_container,
        recording_proxy=args.recording_proxy,
        max_requests=args.max_requests,
        max_seconds=args.max_seconds,
        rate_limit_per_second=args.rate_limit,
        max_examples_per_operation=args.max_examples,
        max_failures=args.max_failures,
        request_timeout=args.request_timeout,
        max_response_bytes=args.max_response_bytes,
        max_steps=args.max_steps,
        seed=args.seed,
    )
    if args.command == "plan":
        print(json.dumps(plan_document(selected), sort_keys=True, indent=2))
        return
    if args.command == "preflight":
        print(json.dumps(preflight(selected, args.guard_image), sort_keys=True, indent=2))
        return
    if not args.output_dir:
        raise SystemExit("run requires --output-dir")
    report = execute(
        selected,
        material,
        args.schema,
        args.identities,
        args.output_dir,
        guard_image=args.guard_image,
    )
    print(json.dumps({
        "run_id": selected.run_id,
        "evidence": report["evidence"],
        "evidence_status": report.get("evidence_status"),
        "summary": report.get("summary"),
        "run_report": report["run_report"],
    }, sort_keys=True, indent=2))


if __name__ == "__main__":
    main()
