"""Operator-only one-shot runner for the Schemathesis shadow contract.

The command is deliberately absent from MCP, the API Lab database, and the
planner.  It never pulls or builds images and writes artifacts only to an
explicit operator-owned destination.
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
    _docker,
    _guard_proxy,
    _image,
    _remove_container,
    _running_container,
    _start_guard,
    _stop_guard,
    parse_guard_coverage,
)
from api_lab.runtime_evidence import (
    bundle_sha256,
    dumps_bundle,
    schemathesis_terminal_exit_reason,
)
from api_lab.schemathesis_shadow import (
    REPORT_PATH,
    SCHEMATHESIS_IMAGE,
    SCHEMATHESIS_VERSION,
    SchemathesisShadowPlan,
    finalize_evidence,
    load_executable_schema,
    make_plan,
    plan_document,
    schemathesis_docker_argv,
)


OUTPUT_CAP_BYTES = 16 * 1024 * 1024


def _now():
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def _sha256(data):
    return hashlib.sha256(data).hexdigest()


def _scanner_name(plan, token):
    suffix = hashlib.sha256(plan.run_id.encode("ascii")).hexdigest()[:12]
    return "uroboros-schemathesis-shadow-" + suffix + "-" + token


def _guard_name(plan, token):
    suffix = hashlib.sha256(plan.run_id.encode("ascii")).hexdigest()[:12]
    return "uroboros-schemathesis-guard-" + suffix + "-" + token


def _probe_schemathesis(image_id):
    completed = _docker([
        "run", "--rm", "--pull", "never", "--network", "none",
        "--read-only", "--tmpfs", "/tmp:rw,nosuid,nodev,size=16m",
        "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
        "--pids-limit", "64", "--memory", "128m", "--cpus", "0.5",
        "--user", "65532:65532", "--workdir", "/tmp",
        "--env", "HOME=/tmp", "--env", "XDG_CACHE_HOME=/tmp/.cache",
        image_id, "schemathesis", "--version",
    ])
    output = (completed.stdout + "\n" + completed.stderr).strip()
    if not re.search(r"(?:^|[^0-9])" + re.escape(SCHEMATHESIS_VERSION) + r"(?:[^0-9]|$)", output):
        raise RuntimeError("local Schemathesis image did not report version " + SCHEMATHESIS_VERSION)
    return output[:256]


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


def _read_bounded(path, maximum=OUTPUT_CAP_BYTES):
    if not path.is_file():
        return b""
    size = path.stat().st_size
    if size > maximum:
        raise ValueError("Schemathesis NDJSON report exceeds the byte limit")
    return path.read_bytes()


def _require_new_artifact_names(destination, run_id):
    suffixes = (
        "schemathesis.ndjson",
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
        raise ValueError("Schemathesis output artifacts already exist for run_id: " + run_id)


def _load_plan(schema_path, expected_sha256, *, target_origin, **values):
    selected = Path(schema_path).resolve()
    if not selected.is_file():
        raise ValueError("schema path does not name a file")
    raw = selected.read_bytes()
    document, _ = load_executable_schema(raw, expected_sha256)
    return make_plan(
        document=document,
        schema_sha256=expected_sha256,
        target_origin=target_origin,
        **values,
    )


def execute(plan, schema_path, output_dir, *, guard_image=DEFAULT_GUARD_IMAGE):
    """Execute one isolated run and return a credential-free artifact index."""
    destination = Path(output_dir).resolve()
    if destination.exists() and not destination.is_dir():
        raise ValueError("output directory is not a directory")
    _require_new_artifact_names(destination, plan.run_id)

    source_schema = Path(schema_path).resolve()
    if not source_schema.is_file():
        raise ValueError("schema path does not name a file")
    schema_bytes = source_schema.read_bytes()
    load_executable_schema(schema_bytes, plan.schema_sha256)
    state = preflight(plan, guard_image)
    token = uuid.uuid4().hex[:8]
    guard_name = _guard_name(plan, token)
    scanner_name = _scanner_name(plan, token)
    guard_proxy = _guard_proxy(plan)
    started_at = _now()
    guard_log = b""
    runner_result = None
    raw = b""

    with tempfile.TemporaryDirectory(prefix="uroboros-schemathesis-") as temporary:
        workspace = Path(temporary)
        staged_schema = workspace / "openapi.json"
        report_dir = workspace / "report"
        report_dir.mkdir(mode=0o777)
        try:
            os.chmod(report_dir, 0o777)
        except OSError:
            pass
        _write_new(staged_schema, schema_bytes, mode=0o444)
        try:
            _start_guard(plan, state["scope_guard_image_id"], guard_name, guard_proxy)
            command = schemathesis_docker_argv(
                plan,
                container_name=scanner_name,
                schema_path=staged_schema,
                output_dir=report_dir,
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
        raw = _read_bounded(report_dir / Path(REPORT_PATH).name)

    finished_at = _now()
    if runner_result is None:
        raise RuntimeError("Schemathesis runner did not start")
    stdout = runner_result["stdout"].encode("utf-8", errors="replace")
    stderr = runner_result["stderr"].encode("utf-8", errors="replace")
    prefix = plan.run_id + "."
    artifacts = {
        "raw": _artifact(destination / (prefix + "schemathesis.ndjson"), raw),
        "stdout": _artifact(destination / (prefix + "schemathesis.stdout.txt"), stdout),
        "stderr": _artifact(destination / (prefix + "schemathesis.stderr.txt"), stderr),
        "guard_log": _artifact(destination / (prefix + "scope-guard.jsonl"), guard_log),
    }
    report = {
        "contract": "uroboros-schemathesis-shadow-run-v1",
        "run_id": plan.run_id,
        "started_at": started_at,
        "finished_at": finished_at,
        "cli_exit_state": runner_result["exit_state"],
        "cli_output_truncated": bool(runner_result["truncated"]),
        "terminal_event_authoritative": False,
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
            exit_reason = schemathesis_terminal_exit_reason(raw)
            report["terminal_event_authoritative"] = True
        except Exception as exc:
            terminal_error = exc
            report["terminal_error"] = {
                "type": type(exc).__name__,
                "message_sha256": _sha256(str(exc).encode("utf-8", errors="replace")),
            }
    if exit_reason is not None:
        try:
            guard_coverage = parse_guard_coverage(guard_log, state["scope_guard_image_id"])
            evidence_truncated = exit_reason != "completed"
            bundle = finalize_evidence(
                raw,
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
            "Schemathesis report is incomplete or invalid; raw diagnostics were retained in %s" % destination
        ) from terminal_error
    if evidence_error is not None:
        raise RuntimeError(
            "Schemathesis evidence finalization failed; raw diagnostics and run report were retained in %s"
            % destination
        ) from evidence_error
    if report["evidence"] is None:
        raise RuntimeError("Schemathesis produced no admissible evidence; diagnostics were retained in %s" % destination)
    return report


def _arguments(argv=None):
    parser = argparse.ArgumentParser(description="Run a model-free Schemathesis shadow test")
    parser.add_argument("command", choices=("plan", "preflight", "run"))
    parser.add_argument("--schema", required=True)
    parser.add_argument("--schema-sha256", required=True)
    parser.add_argument("--target", required=True)
    parser.add_argument("--target-revision", required=True)
    parser.add_argument("--scope-ref", required=True)
    parser.add_argument("--network-container", required=True)
    parser.add_argument("--run-id", default="schemathesis-" + uuid.uuid4().hex[:16])
    parser.add_argument("--recording-proxy", default="http://127.0.0.1:8788")
    parser.add_argument("--max-requests", type=int, default=200)
    parser.add_argument("--max-seconds", type=int, default=30)
    parser.add_argument("--max-concurrency", type=int, default=1)
    parser.add_argument("--rate-limit", type=int, default=4)
    parser.add_argument("--max-examples", type=int, default=10)
    parser.add_argument("--max-failures", type=int, default=20)
    parser.add_argument("--request-timeout", type=int, default=5)
    parser.add_argument("--max-response-bytes", type=int, default=1048576)
    parser.add_argument("--seed", type=int, default=1)
    parser.add_argument("--guard-image", default=DEFAULT_GUARD_IMAGE)
    parser.add_argument("--output-dir")
    return parser.parse_args(argv)


def main(argv=None):
    args = _arguments(argv)
    selected = _load_plan(
        args.schema,
        args.schema_sha256,
        target_origin=args.target,
        run_id=args.run_id,
        target_revision=args.target_revision,
        scope_ref=args.scope_ref,
        network_container=args.network_container,
        recording_proxy=args.recording_proxy,
        max_requests=args.max_requests,
        max_seconds=args.max_seconds,
        max_concurrency=args.max_concurrency,
        rate_limit_per_second=args.rate_limit,
        max_examples_per_operation=args.max_examples,
        max_failures=args.max_failures,
        request_timeout=args.request_timeout,
        max_response_bytes=args.max_response_bytes,
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
    report = execute(selected, args.schema, args.output_dir, guard_image=args.guard_image)
    print(json.dumps({
        "run_id": selected.run_id,
        "evidence": report["evidence"],
        "evidence_status": report.get("evidence_status"),
        "summary": report.get("summary"),
        "run_report": report["run_report"],
    }, sort_keys=True, indent=2))


if __name__ == "__main__":
    main()
