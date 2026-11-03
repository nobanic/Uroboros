"""Operator-only one-shot runner for the offline Katana shadow adapter.

Nothing in this module is registered with MCP or called by the Uroboros agent.
Images must already exist locally: execution never performs an implicit pull or
build.  Artifacts are written only to an explicit operator-selected directory.
"""

from __future__ import annotations

import argparse
import collections
import hashlib
import json
import os
import re
import subprocess
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path

from arsenal.contracts import OK
from arsenal.execmgr import Runner
from api_lab.katana_scope_proxy import READ_ONLY_METHODS, SUPPORTED_HTTP_METHODS
from api_lab.katana_shadow import (
    KATANA_IMAGE,
    KATANA_VERSION,
    KatanaShadowPlan,
    katana_docker_argv,
    plan_document,
)
from api_lab.runtime_evidence import bundle_sha256, dumps_bundle


DEFAULT_GUARD_IMAGE = "uroboros-katana/scope-guard:local"
OUTPUT_CAP_BYTES = 16 * 1024 * 1024
ALLOWED_GUARD_REJECTIONS = {
    "outside_authority",
    "outside_origin",
    "method_not_read_only",
    "method_not_allowed",
    "transaction_budget",
}
GUARD_EVENTS = {"ready", "allowed", "rejected", "upstream_error", "summary", "fatal"}
MAX_GUARD_LOG_EVENTS = 2048


def _now():
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def _sha256(data):
    return hashlib.sha256(data).hexdigest()


def _docker(arguments, *, timeout=30, check=True):
    completed = subprocess.run(
        ["docker", *arguments],
        capture_output=True,
        text=True,
        timeout=timeout,
        check=False,
    )
    if check and completed.returncode:
        detail = (completed.stderr or completed.stdout).strip()[:1000]
        raise RuntimeError("docker %s failed: %s" % (" ".join(arguments[:4]), detail))
    return completed


def _image(ref):
    completed = _docker(["image", "inspect", ref])
    try:
        values = json.loads(completed.stdout)
    except json.JSONDecodeError:
        raise RuntimeError("docker returned invalid image metadata") from None
    if not isinstance(values, list) or len(values) != 1 or not isinstance(values[0], dict):
        raise RuntimeError("docker returned ambiguous image metadata")
    identifier = values[0].get("Id")
    if not isinstance(identifier, str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", identifier):
        raise RuntimeError("Docker image lacks an immutable image ID")
    return values[0]


def _running_container(name):
    completed = _docker(["container", "inspect", name])
    try:
        value = json.loads(completed.stdout)[0]
    except (IndexError, KeyError, TypeError, json.JSONDecodeError):
        raise RuntimeError("docker returned invalid container metadata") from None
    if value.get("State", {}).get("Running") is not True:
        raise RuntimeError("network container is not running: " + name)
    return value


def _remove_container(name):
    inspected = _docker(["container", "inspect", name], check=False)
    if inspected.returncode == 0:
        _docker(["container", "rm", "--force", name], check=False)


def _probe_katana():
    completed = _docker([
        "run", "--rm", "--pull", "never", "--network", "none",
        "--read-only", "--tmpfs", "/tmp:rw,nosuid,nodev,size=16m",
        "--cap-drop", "ALL",
        "--security-opt", "no-new-privileges:true",
        "--user", "65532:65532", "--env", "HOME=/tmp",
        "--env", "XDG_CACHE_HOME=/tmp/.cache", "--env", "XDG_CONFIG_HOME=/tmp/.config",
        KATANA_IMAGE, "-version", "-disable-update-check",
    ], timeout=30)
    output = (completed.stdout + "\n" + completed.stderr).strip()
    if not re.search(r"(?:^|[^0-9])v?" + re.escape(KATANA_VERSION) + r"(?:[^0-9]|$)", output):
        raise RuntimeError("pinned Katana image did not report version " + KATANA_VERSION)
    return output[:256]


def _probe_chromium():
    base = [
        "run", "--rm", "--pull", "never", "--network", "none",
        "--read-only", "--tmpfs", "/tmp:rw,nosuid,nodev,size=16m",
        "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
        "--user", "65532:65532", "--env", "HOME=/tmp",
    ]
    for binary in ("chromium-browser", "chromium"):
        completed = _docker([
            *base, "--entrypoint", binary, KATANA_IMAGE, "--version",
        ], timeout=30, check=False)
        value = (completed.stdout or completed.stderr).strip().splitlines()
        if completed.returncode == 0 and value and len(value[0]) <= 128:
            return value[0]
    raise RuntimeError("pinned Katana image did not expose the bundled Chromium version")


def _guard_name(plan, execution_token):
    suffix = hashlib.sha256(plan.run_id.encode("ascii")).hexdigest()[:12]
    return "uroboros-katana-guard-" + suffix + "-" + execution_token


def _crawler_name(plan, execution_token):
    suffix = hashlib.sha256(plan.run_id.encode("ascii")).hexdigest()[:12]
    return "uroboros-katana-shadow-" + suffix + "-" + execution_token


def _guard_proxy(plan):
    # The listener lives in the shared firewall namespace.  A run-derived high
    # port avoids collisions with the recording proxy and concurrent namespaces
    # without asking Docker to publish anything to the host.
    offset = int(hashlib.sha256(plan.run_id.encode("ascii")).hexdigest()[:8], 16) % 20000
    return "http://127.0.0.1:%d" % (30000 + offset)


def _start_guard(plan, guard_image_id, guard_name, guard_proxy, allowed_methods=None):
    parsed_port = int(guard_proxy.rsplit(":", 1)[1])
    command = [
        "run", "-d", "--pull", "never", "--name", guard_name,
        "--network", "container:" + plan.network_container,
        "--read-only", "--tmpfs", "/tmp:rw,nosuid,nodev,size=16m",
        "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
        "--pids-limit", "64", "--memory", "64m", "--cpus", "0.25",
        "--user", "65532:65532",
        guard_image_id,
        "--listen", "127.0.0.1:" + str(parsed_port),
        "--upstream", plan.recording_proxy,
        "--origin", plan.origin,
        "--max-transactions", str(plan.max_requests),
        "--max-body-bytes", str(plan.max_response_bytes),
        "--socket-timeout", str(min(15, plan.max_seconds)),
        "--tunnel-seconds", str(min(65, plan.max_seconds + 5)),
    ]
    if allowed_methods is not None:
        selected = set(allowed_methods)
        if not selected or any(
            not isinstance(method, str) or method not in SUPPORTED_HTTP_METHODS
            for method in selected
        ):
            raise ValueError("scope guard allowed methods are invalid")
        for method in sorted(selected):
            command.extend(["--allow-method", method])
    _docker(command)
    deadline = time.monotonic() + 8
    while time.monotonic() < deadline:
        logs = _docker(["logs", guard_name], check=False)
        if '"event":"ready"' in logs.stdout:
            return
        state = _docker(["container", "inspect", "--format", "{{.State.Running}}", guard_name], check=False)
        if state.returncode or state.stdout.strip().lower() != "true":
            detail = (logs.stderr or logs.stdout).strip()[:1000]
            raise RuntimeError("Katana scope guard exited before readiness: " + detail)
        time.sleep(0.1)
    raise RuntimeError("Katana scope guard did not become ready")


def _stop_guard(name):
    _docker(["container", "stop", "--time", "3", name], timeout=10, check=False)
    logs = _docker(["logs", name], check=False)
    _remove_container(name)
    return (logs.stdout + logs.stderr).encode("utf-8", errors="replace")


def parse_guard_coverage(data, guard_image_id, allowed_methods=None):
    if not isinstance(guard_image_id, str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", guard_image_id):
        raise ValueError("scope guard image ID is invalid")
    try:
        text = data.decode("utf-8") if isinstance(data, bytes) else str(data)
    except UnicodeDecodeError:
        raise ValueError("scope guard log is not UTF-8") from None
    events = []
    for number, line in enumerate(text.splitlines(), 1):
        if not line.strip():
            continue
        if len(events) >= MAX_GUARD_LOG_EVENTS:
            raise ValueError("scope guard log exceeds the event limit")
        if len(line.encode("utf-8")) > 4096:
            raise ValueError("scope guard log line exceeds the limit")
        try:
            value = json.loads(line)
        except json.JSONDecodeError:
            raise ValueError("scope guard log line %d is invalid JSON" % number) from None
        if not isinstance(value, dict) or not isinstance(value.get("event"), str):
            raise ValueError("scope guard log line %d is invalid" % number)
        if value["event"] not in GUARD_EVENTS:
            raise ValueError("scope guard log line %d has an unsupported event" % number)
        events.append(value)
    ready = [event for event in events if event["event"] == "ready"]
    if len(ready) != 1:
        raise ValueError("scope guard log requires exactly one readiness event")
    selected_methods = set(READ_ONLY_METHODS if allowed_methods is None else allowed_methods)
    if not selected_methods or any(
        not isinstance(method, str) or method not in SUPPORTED_HTTP_METHODS
        for method in selected_methods
    ):
        raise ValueError("scope guard allowed methods are invalid")
    reported_methods = ready[0].get("allowed_methods")
    if reported_methods is not None and reported_methods != sorted(selected_methods):
        raise ValueError("scope guard readiness methods disagree with the plan")
    if allowed_methods is not None and reported_methods is None:
        raise ValueError("active scope guard did not attest its allowed methods")
    allowed = [event for event in events if event["event"] == "allowed"]
    rejected = [event for event in events if event["event"] == "rejected"]
    errors = [event for event in events if event["event"] in {"fatal", "upstream_error"}]
    if any(not isinstance(event.get("reason"), str) for event in rejected):
        raise ValueError("scope guard rejection reason is invalid")
    for event in allowed:
        method = event.get("method")
        kind = event.get("kind")
        if (kind == "http" and method not in selected_methods) or (
            kind == "connect" and method != "CONNECT"
        ) or kind not in {"http", "connect"}:
            raise ValueError("scope guard allowed an unsupported method or transaction kind")
    reasons = collections.Counter(event["reason"] for event in rejected)
    unexpected = sorted(set(reasons) - ALLOWED_GUARD_REJECTIONS)
    if not allowed:
        raise ValueError("scope guard recorded no allowed target transactions")
    if errors:
        raise ValueError("scope guard reported an upstream or fatal error")
    if unexpected:
        raise ValueError("scope guard reported unexpected rejection reasons: " + ", ".join(unexpected))
    summaries = [event for event in events if event["event"] == "summary"]
    if len(summaries) != 1:
        raise ValueError("scope guard log requires exactly one summary event")
    summary = summaries[0]
    counters = {}
    for key in ("transactions", "allowed", "rejected", "upstream_errors"):
        value = summary.get(key)
        if not isinstance(value, int) or isinstance(value, bool) or value < 0:
            raise ValueError("scope guard summary counter is invalid: " + key)
        counters[key] = value
    if counters != {
        "transactions": len(allowed) + len(rejected),
        "allowed": len(allowed),
        "rejected": len(rejected),
        "upstream_errors": len(errors),
    }:
        raise ValueError("scope guard summary does not match its event stream")
    sequences = [event.get("sequence") for event in allowed + rejected]
    if (
        any(not isinstance(value, int) or isinstance(value, bool) or value <= 0 for value in sequences)
        or len(set(sequences)) != len(sequences)
        or set(sequences) != set(range(1, counters["transactions"] + 1))
    ):
        raise ValueError("scope guard transaction sequence is incomplete or ambiguous")
    return {
        "image_id": guard_image_id,
        "ready": True,
        "allowed": len(allowed),
        "rejected": len(rejected),
        "rejected_reasons": dict(sorted(reasons.items())),
        "upstream_errors": len(errors),
        "transactions": counters["transactions"],
        "allowed_methods": sorted(selected_methods),
        "read_only_methods": sorted(selected_methods & READ_ONLY_METHODS),
        "mutation_methods": sorted(selected_methods - READ_ONLY_METHODS),
        "exact_origin": True,
    }


def preflight(plan, guard_image=DEFAULT_GUARD_IMAGE):
    _running_container(plan.network_container)
    katana = _image(KATANA_IMAGE)
    guard = _image(guard_image)
    version = _probe_katana()
    browser = _probe_chromium() if plan.mode == "headless" else None
    return {
        "katana_image_id": katana["Id"],
        "guard_image_id": guard["Id"],
        "katana_version_output": version,
        "browser_version": browser,
    }


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


def _artifact(path, data):
    _write_new(path, data)
    return {"name": path.name, "bytes": len(data), "sha256": _sha256(data)}


def _require_new_artifact_names(destination, run_id):
    suffixes = (
        "katana.jsonl",
        "katana.stderr.txt",
        "scope-guard.jsonl",
        "evidence.json",
        "run.json",
    )
    collisions = [run_id + "." + suffix for suffix in suffixes if (destination / (run_id + "." + suffix)).exists()]
    if collisions:
        raise ValueError("Katana output artifacts already exist for run_id: " + run_id)


def execute(plan, output_dir, *, guard_image=DEFAULT_GUARD_IMAGE, ca_file=None):
    """Run one crawler and return a credential-free artifact index."""
    destination = Path(output_dir).resolve()
    if destination.exists() and not destination.is_dir():
        raise ValueError("output directory is not a directory")
    _require_new_artifact_names(destination, plan.run_id)
    state = preflight(plan, guard_image)
    execution_token = uuid.uuid4().hex[:8]
    guard_name = _guard_name(plan, execution_token)
    crawler_name = _crawler_name(plan, execution_token)
    guard_proxy = _guard_proxy(plan)
    started_at = _now()
    guard_log = b""
    runner_result = None
    try:
        _start_guard(plan, state["guard_image_id"], guard_name, guard_proxy)
        command = katana_docker_argv(
            plan,
            container_name=crawler_name,
            guard_proxy=guard_proxy,
            ca_file=ca_file,
        )
        runner_result = Runner().run(
            command,
            timeout=plan.max_seconds + 20,
            cap=OUTPUT_CAP_BYTES,
        )
    finally:
        _remove_container(crawler_name)
        guard_log = _stop_guard(guard_name)
    finished_at = _now()
    if runner_result is None:
        raise RuntimeError("Katana runner did not start")
    raw = runner_result["stdout"].encode("utf-8")
    stderr = runner_result["stderr"].encode("utf-8")
    artifacts = {}
    prefix = plan.run_id + "."
    artifacts["raw"] = _artifact(destination / (prefix + "katana.jsonl"), raw)
    artifacts["stderr"] = _artifact(destination / (prefix + "katana.stderr.txt"), stderr)
    artifacts["guard_log"] = _artifact(destination / (prefix + "scope-guard.jsonl"), guard_log)
    report = {
        "contract": "uroboros-katana-shadow-run-v1",
        "run_id": plan.run_id,
        "started_at": started_at,
        "finished_at": finished_at,
        "exit_state": runner_result["exit_state"],
        "truncated": bool(runner_result["truncated"]),
        "images": {
            "katana_ref": KATANA_IMAGE,
            "katana_image_id": state["katana_image_id"],
            "scope_guard_image_id": state["guard_image_id"],
        },
        "plan": plan_document(plan, guard_proxy),
        "artifacts": artifacts,
        "evidence": None,
    }
    evidence_error = None
    if runner_result["exit_state"] == OK and not runner_result["truncated"]:
        from api_lab.katana_shadow import finalize_evidence

        try:
            guard_coverage = parse_guard_coverage(guard_log, state["guard_image_id"])
            budget_exhausted = guard_coverage["rejected_reasons"].get("transaction_budget", 0) > 0
            evidence_exit_reason = "budget_exhausted" if budget_exhausted else "completed"
            evidence_truncated = budget_exhausted
            report["evidence_status"] = {
                "complete": not evidence_truncated,
                "exit_reason": evidence_exit_reason,
                "truncated": evidence_truncated,
            }
            bundle = finalize_evidence(
                raw,
                plan,
                started_at=started_at,
                finished_at=finished_at,
                browser_version=state["browser_version"],
                guard_coverage=guard_coverage,
                exit_reason=evidence_exit_reason,
                truncated=evidence_truncated,
                guard_proxy=guard_proxy,
            )
            encoded = dumps_bundle(bundle)
            evidence = _artifact(destination / (prefix + "evidence.json"), encoded)
            evidence["bundle_sha256"] = bundle_sha256(bundle)
            report["evidence"] = evidence
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
    if evidence_error is not None:
        raise RuntimeError(
            "Katana evidence finalization failed; raw diagnostics and run report were retained in %s"
            % destination
        ) from evidence_error
    if runner_result["exit_state"] != OK or runner_result["truncated"]:
        raise RuntimeError(
            "Katana shadow run failed (%s, truncated=%s); raw diagnostics were retained in %s"
            % (runner_result["exit_state"], runner_result["truncated"], destination)
        )
    return report


def _arguments(argv=None):
    parser = argparse.ArgumentParser(description="Run a model-free Katana shadow crawl")
    parser.add_argument("command", choices=("plan", "preflight", "run"))
    parser.add_argument("--target", required=True)
    parser.add_argument("--target-revision", required=True)
    parser.add_argument("--scope-ref", required=True)
    parser.add_argument("--network-container", required=True)
    parser.add_argument("--run-id", default="katana-" + uuid.uuid4().hex[:16])
    parser.add_argument("--mode", choices=("standard", "headless"), default="standard")
    parser.add_argument("--recording-proxy", default="http://127.0.0.1:8788")
    parser.add_argument("--max-requests", type=int, default=100)
    parser.add_argument("--max-seconds", type=int, default=30)
    parser.add_argument("--max-concurrency", type=int, default=2)
    parser.add_argument("--max-response-bytes", type=int, default=1048576)
    parser.add_argument("--depth", type=int, default=3)
    parser.add_argument("--guard-image", default=DEFAULT_GUARD_IMAGE)
    parser.add_argument("--ca-file")
    parser.add_argument("--output-dir")
    return parser.parse_args(argv)


def main(argv=None):
    args = _arguments(argv)
    selected = KatanaShadowPlan(
        run_id=args.run_id,
        target_url=args.target,
        target_revision=args.target_revision,
        scope_ref=args.scope_ref,
        network_container=args.network_container,
        mode=args.mode,
        recording_proxy=args.recording_proxy,
        max_requests=args.max_requests,
        max_seconds=args.max_seconds,
        max_concurrency=args.max_concurrency,
        max_response_bytes=args.max_response_bytes,
        depth=args.depth,
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
        args.output_dir,
        guard_image=args.guard_image,
        ca_file=args.ca_file,
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
