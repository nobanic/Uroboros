"""Strict, bounded console-model workflow check against disposable API fixtures.

The fixture name reveals its variant, so this is a workflow smoke test rather
than a blinded vulnerability-discovery benchmark. Results are immutable attempt
snapshots: activity after the cutoff is retained by Uroboros, but is never folded
back into an earlier benchmark score.
"""

import argparse
import json
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import urlencode

from api_lab.register import configured_secret
from arsenal.register import _call


STOPPED_STATUSES = frozenset({"done", "failed", "timeout", "paused"})


def _rows(payload, *keys):
    """Return rows from every response shape used by the console APIs."""
    if isinstance(payload, list):
        return payload
    if not isinstance(payload, dict):
        return []
    for key in keys + ("items", "data", "findings", "experiments"):
        value = payload.get(key)
        if isinstance(value, list):
            return value
    return []


def _parse_timestamp(value):
    if not isinstance(value, str) or not value.strip():
        return None
    try:
        return datetime.fromisoformat(value.strip().replace("Z", "+00:00")).astimezone(timezone.utc)
    except ValueError:
        return None


def _at_or_before(row, cutoff):
    """Conservatively exclude undated or post-cutoff evidence from strict scoring."""
    stamp = _parse_timestamp(row.get("created_at") or row.get("ts")) if isinstance(row, dict) else None
    return stamp is not None and stamp <= cutoff


def _collect_findings(call, jwt, task_id, page_size=200):
    """Read authoritative standalone finding rows, filtered by task ID."""
    page = 1
    rows = []
    while True:
        query = urlencode({"task_id": task_id, "page": page, "limit": page_size})
        payload = call("GET", "/api/exploration/findings?" + query, jwt)
        batch = _rows(payload)
        rows.extend(batch)
        total = payload.get("total", len(rows)) if isinstance(payload, dict) else len(rows)
        if not batch or len(rows) >= total or len(batch) < page_size:
            return rows
        page += 1


def _collect_experiments(call, jwt, findings):
    experiments = []
    for finding in findings:
        finding_id = finding.get("finding_id") or finding.get("id")
        if not finding_id:
            continue
        payload = call("GET", "/api/exploration/findings/%s/experiments" % finding_id, jwt)
        for experiment in _rows(payload, "experiments"):
            item = dict(experiment)
            item.setdefault("finding_id", str(finding_id))
            experiments.append(item)
    return experiments


def _score(variant, status_at_cutoff, findings, experiments, cutoff, attempt_valid=True):
    scored_findings = [row for row in findings if _at_or_before(row, cutoff)]
    scored_ids = {str(row.get("finding_id") or row.get("id")) for row in scored_findings}
    scored_experiments = [
        row for row in experiments
        if str(row.get("finding_id")) in scored_ids and _at_or_before(row, cutoff)
    ]

    if not attempt_valid:
        passed = False
        reason = "execution_budget_never_started"
    elif variant == "vulnerable":
        passed = bool(scored_findings and scored_experiments)
        reason = ("finding_and_bound_experiment_before_cutoff" if passed
                  else "missing_finding_or_bound_experiment_before_cutoff")
    else:
        passed = status_at_cutoff == "done" and not scored_findings
        reason = ("completed_without_finding" if passed
                  else "fixed_control_did_not_complete_cleanly")

    return {
        "valid": attempt_valid,
        "passed": passed,
        "reason": reason,
        "status_at_cutoff": status_at_cutoff,
        "finding_count": len(scored_findings),
        "experiment_count": len(scored_experiments),
        "finding_ids": [str(row.get("finding_id") or row.get("id")) for row in scored_findings],
        "experiment_ids": [row.get("id") for row in scored_experiments],
    }


def _write_immutable(path, result):
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("x", encoding="utf-8", newline="\n") as handle:
        json.dump(result, handle, ensure_ascii=True, sort_keys=True, separators=(",", ":"))
        handle.write("\n")


def _goal(base):
    return (
        "API Lab integration test against the disposable local API at " + base
        + "; its OpenAPI is " + base + "/openapi.json. The intended contract is that each note's "
        "private data can only be read or changed by its owner. Configured API Lab identities alice "
        "and bob represent distinct ordinary users, with no sharing or administrator privilege. Use "
        "API Lab catalog and construct your own response-linked experiment to test this contract, "
        "including both users' legitimate own-object controls. Use fresh objects and cleanup; inspect "
        "actual response content and owner readback for mutations. Use only this fixture service; a "
        "full scan is unnecessary. Treat this as one atomic focused intent: do not split controls, read, "
        "and write checks across parallel workers. A single private_read direction with owner and "
        "anonymous controls is sufficient; after one reproducible violation is bound, skip additional "
        "write or reverse-direction experiments. Start with API Lab instead of general asset recon. "
        "Do not read the fixture source, use previous jobs or borrow existing "
        "recipes. Missing preconditions or interrupted tests are inconclusive. Record observations via "
        "existing fact tools. If you observe a security violation, report it with the current intent and "
        "actual proof, then bind_finding_experiment using the returned standalone finding_id and your own "
        "terminal Lab job/reproducer artifact. Do not claim that recording or attaching confirms triage. "
        "If the checks pass, report the tested contract as an observed fact with scope, not a general "
        "claim that the API is secure. End when this focused experiment and evidence handoff finish."
    )


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--variant", choices=("vulnerable", "fixed"), required=True)
    parser.add_argument("--budget-seconds", type=int, default=180)
    parser.add_argument("--queue-timeout-seconds", type=int, default=300)
    parser.add_argument("--poll-seconds", type=float, default=5)
    parser.add_argument("--stop-grace-seconds", type=float, default=20)
    parser.add_argument("--result-dir", type=Path, default=Path("/tmp"))
    args = parser.parse_args()
    if (args.budget_seconds < 1 or args.queue_timeout_seconds < 1
            or args.poll_seconds <= 0 or args.stop_grace_seconds < 0):
        parser.error("budgets and polling intervals must be positive")

    jwt = _call(
        "POST", "/api/auth/login", None,
        {"username": "Uroboros", "password": configured_secret("ADMIN_PASSWORD")},
    )["token"]
    config = _call("GET", "/api/llm", jwt)
    if not config.get("configured"):
        raise SystemExit("console model is not configured")

    started = datetime.now(timezone.utc)
    attempt_id = started.strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:12]
    base = "http://api-lab-" + args.variant + ":9000"
    task = _call(
        "POST", "/api/tasks", jwt,
        {
            "description": "API Lab console workflow - " + args.variant,
            "goal": _goal(base),
            "timeout_seconds": args.budget_seconds,
            "coverage_enabled": False,
        },
    )
    task_id = str(task.get("id") or task.get("task", {}).get("id"))
    if not task_id or task_id == "None":
        raise RuntimeError("task creation did not return an ID")

    print(json.dumps({
        "attempt_id": attempt_id,
        "started_task": task_id,
        "variant": args.variant,
        "model": config.get("model"),
        "budget_seconds": args.budget_seconds,
        "queue_timeout_seconds": args.queue_timeout_seconds,
    }), flush=True)

    queue_started = time.monotonic()
    queue_deadline = queue_started + args.queue_timeout_seconds
    current = task
    status_at_cutoff = current.get("status", "")
    execution_started = None
    execution_started_monotonic = None
    cutoff_reason = "queue_timeout"
    while True:
        current = _call("GET", "/api/tasks/" + task_id, jwt)
        status_at_cutoff = current.get("status", "")
        if status_at_cutoff in STOPPED_STATUSES:
            cutoff_reason = "task_stopped_before_running_observed"
            break
        if status_at_cutoff == "running":
            execution_started = datetime.now(timezone.utc)
            execution_started_monotonic = time.monotonic()
            break
        remaining = queue_deadline - time.monotonic()
        if remaining <= 0:
            break
        time.sleep(min(args.poll_seconds, remaining))

    if execution_started_monotonic is not None:
        deadline = execution_started_monotonic + args.budget_seconds
        cutoff_reason = "budget_expired"
        while True:
            current = _call("GET", "/api/tasks/" + task_id, jwt)
            status_at_cutoff = current.get("status", "")
            if status_at_cutoff in STOPPED_STATUSES:
                cutoff_reason = "task_stopped"
                break
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                break
            time.sleep(min(args.poll_seconds, remaining))

    cutoff_monotonic = time.monotonic()
    cutoff = datetime.now(timezone.utc)
    pause_requested = False
    if status_at_cutoff not in STOPPED_STATUSES:
        _call("POST", "/api/tasks/control/batch", jwt, {"task_ids": [task_id], "action": "pause"})
        pause_requested = True

    stop_deadline = time.monotonic() + args.stop_grace_seconds
    while True:
        current = _call("GET", "/api/tasks/" + task_id, jwt)
        if current.get("status", "") in STOPPED_STATUSES or time.monotonic() >= stop_deadline:
            break
        time.sleep(min(args.poll_seconds, max(0.01, stop_deadline - time.monotonic())))

    findings = _collect_findings(_call, jwt, task_id)
    experiments = _collect_experiments(_call, jwt, findings)
    score = _score(
        args.variant, status_at_cutoff, findings, experiments, cutoff,
        attempt_valid=execution_started is not None,
    )
    finalized = datetime.now(timezone.utc)
    result = {
        "schema_version": 3,
        "record_kind": "strict_console_attempt",
        "attempt_id": attempt_id,
        "immutable": True,
        "continuation_policy": "activity after cutoff_at is excluded; record it as a separate recovery run",
        "task_id": task_id,
        "variant": args.variant,
        "model": config.get("model"),
        "budget_seconds": args.budget_seconds,
        "queue_timeout_seconds": args.queue_timeout_seconds,
        "started_at": started.isoformat(),
        "execution_started_at": execution_started.isoformat() if execution_started else None,
        "queue_wait_seconds": round(
            (execution_started_monotonic or cutoff_monotonic) - queue_started, 3,
        ),
        "execution_elapsed_seconds": (
            round(cutoff_monotonic - execution_started_monotonic, 3)
            if execution_started_monotonic is not None else 0
        ),
        "cutoff_at": cutoff.isoformat(),
        "cutoff_reason": cutoff_reason,
        "finalized_at": finalized.isoformat(),
        "pause_requested": pause_requested,
        "status_after_finalizer": current.get("status", ""),
        "task_snapshot": current,
        "observed": {
            "findings": findings,
            "experiments": experiments,
            "finding_count": len(findings),
            "experiment_count": len(experiments),
        },
        "score": score,
    }
    path = args.result_dir / ("api-lab-console-%s-%s.json" % (args.variant, attempt_id))
    _write_immutable(path, result)
    print(json.dumps({
        "attempt_id": attempt_id,
        "task_id": task_id,
        "status_at_cutoff": status_at_cutoff,
        "status_after_finalizer": current.get("status", ""),
        "finding_count": score["finding_count"],
        "experiment_count": score["experiment_count"],
        "passed": score["passed"],
        "valid": score["valid"],
        "result_file": str(path),
    }), flush=True)


if __name__ == "__main__":
    main()
