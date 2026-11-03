"""Model-free live smoke gate for the pinned Katana shadow integration."""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import time
import uuid
from pathlib import Path

from api_lab.katana_shadow import KatanaShadowPlan
from api_lab.run_katana_shadow import DEFAULT_GUARD_IMAGE, execute
from api_lab.runtime_evidence import loads_bundle


DEFAULT_FIXTURE_IMAGE = "uroboros-katana/fixture:local"


def _docker(arguments, *, check=True, timeout=30):
    completed = subprocess.run(
        ["docker", *arguments], capture_output=True, text=True, check=False, timeout=timeout
    )
    if check and completed.returncode:
        detail = (completed.stderr or completed.stdout).strip()[:1000]
        raise RuntimeError("docker %s failed: %s" % (" ".join(arguments[:4]), detail))
    return completed


def _wait_ready(name):
    deadline = time.monotonic() + 8
    while time.monotonic() < deadline:
        logs = _docker(["logs", name], check=False)
        if '"event":"ready"' in logs.stdout:
            return
        running = _docker(["inspect", "--format", "{{.State.Running}}", name], check=False)
        if running.returncode or running.stdout.strip().lower() != "true":
            raise RuntimeError(name + " exited before readiness: " + (logs.stderr or logs.stdout).strip()[:500])
        time.sleep(0.1)
    raise RuntimeError(name + " did not become ready")


def _remove(name):
    exists = _docker(["container", "inspect", name], check=False)
    if exists.returncode:
        return b""
    _docker(["container", "stop", "--time", "2", name], check=False, timeout=10)
    logs = _docker(["logs", name], check=False)
    _docker(["container", "rm", "--force", name], check=False)
    return (logs.stdout + logs.stderr).encode("utf-8", errors="replace")


def _run_support(name, network, image, arguments, *, alias=None, namespace=None):
    command = [
        "run", "-d", "--pull", "never", "--name", name,
        "--read-only", "--tmpfs", "/tmp:rw,nosuid,nodev,size=16m",
        "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
        "--pids-limit", "64", "--memory", "96m", "--cpus", "0.5",
        "--user", "65532:65532",
    ]
    if namespace:
        command.extend(["--network", "container:" + namespace])
    else:
        command.extend(["--network", network])
        if alias:
            command.extend(["--network-alias", alias])
    command.extend([image, *arguments])
    _docker(command)
    _wait_ready(name)


def _read_bundle(report, output_dir):
    path = Path(output_dir) / report["evidence"]["name"]
    return loads_bundle(path.read_bytes(), report["evidence"]["bundle_sha256"])


def _write_new(path, value):
    data = json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("ascii") + b"\n"
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "wb") as handle:
        handle.write(data)
    return path


def verify(output_dir, *, fixture_image=DEFAULT_FIXTURE_IMAGE, guard_image=DEFAULT_GUARD_IMAGE):
    destination = Path(output_dir).resolve()
    destination.mkdir(parents=True, exist_ok=True)
    token = uuid.uuid4().hex[:10]
    network = "uroboros-katana-smoke-" + token
    target = "uroboros-katana-target-" + token
    recorder = "uroboros-katana-recorder-" + token
    for image in (fixture_image, guard_image):
        _docker(["image", "inspect", image])
    _docker([
        "network", "create", "--internal",
        "--label", "uroboros.katana-shadow-smoke=" + token,
        network,
    ])
    target_log = recorder_log = b""
    reports = {}
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
            ["recorder", "--listen", "127.0.0.1:8788", "--target-origin", "http://fixture:8080"],
        )
        for mode in ("standard", "headless"):
            selected = KatanaShadowPlan(
                run_id="katana-smoke-%s-%s" % (mode, token),
                target_url="http://fixture:8080/",
                target_revision="synthetic-fixture-v1",
                scope_ref="authorization:synthetic-fixture",
                network_container=recorder,
                mode=mode,
                max_requests=50,
                max_seconds=20,
                max_concurrency=2,
                max_response_bytes=262144,
                depth=3,
            )
            reports[mode] = execute(
                selected,
                destination,
                guard_image=guard_image,
            )
        standard = _read_bundle(reports["standard"], destination)
        headless = _read_bundle(reports["headless"], destination)
        standard_paths = sorted({
            node["data"]["path"] for node in standard["nodes"] if node["kind"] == "endpoint"
        })
        headless_paths = sorted({
            node["data"]["path"] for node in headless["nodes"] if node["kind"] == "endpoint"
        })
        if "/api/runtime" not in headless_paths:
            raise RuntimeError("headless Katana did not observe the runtime-only endpoint")
        if headless["summary"]["claims"]["findings"] != 0:
            raise RuntimeError("shadow crawl unexpectedly promoted a finding")
        if standard["summary"]["claims"]["findings"] != 0:
            raise RuntimeError("standard shadow crawl unexpectedly promoted a finding")
        guard_coverage = headless["manifest"]["coverage"]["scope_guard"]
        if guard_coverage.get("rejected_reasons", {}).get("method_not_read_only", 0) < 1:
            raise RuntimeError("scope guard did not reject the fixture's runtime POST")
        rejected_nodes = [
            node for node in headless["nodes"]
            if node["kind"] == "coverage_observation"
            and node["data"].get("kind") == "scope_guard_rejection"
            and node["data"].get("reason") == "method_not_read_only"
        ]
        if len(rejected_nodes) != 1:
            raise RuntimeError("blocked runtime POST was not isolated as one coverage observation")
        if any(
            node["kind"] == "request_observation" and node["data"].get("method") == "POST"
            for node in headless["nodes"]
        ):
            raise RuntimeError("blocked runtime POST entered request evidence")
        if any(
            node["kind"] == "response_observation" and node["data"].get("status") == 405
            for node in headless["nodes"]
        ):
            raise RuntimeError("scope-guard response entered target response evidence")
        response_nodes = [node for node in headless["nodes"] if node["kind"] == "response_observation"]
        if not response_nodes or any("traffic_id" not in node["data"] for node in response_nodes):
            raise RuntimeError("headless response evidence lacks recording-proxy lineage")
        encoded_evidence = json.dumps(
            {"standard": standard, "headless": headless},
            sort_keys=True,
            separators=(",", ":"),
        )
        for secret in ("fixture-link-secret", "script-secret", "must=stay-blocked", "raw-only"):
            if secret in encoded_evidence:
                raise RuntimeError("redacted evidence contains fixture secret material")
        result = {
            "contract": "uroboros-katana-shadow-smoke-v1",
            "passed": True,
            "network_internal": True,
            "modes": {
                "standard": {
                    "paths": standard_paths,
                    "summary": standard["summary"],
                    "bundle_sha256": reports["standard"]["evidence"]["bundle_sha256"],
                },
                "headless": {
                    "paths": headless_paths,
                    "summary": headless["summary"],
                    "scope_guard": guard_coverage,
                    "bundle_sha256": reports["headless"]["evidence"]["bundle_sha256"],
                },
            },
            "assertions": {
                "runtime_endpoint_observed": True,
                "runtime_post_rejected": True,
                "findings_promoted": 0,
            },
        }
    except Exception as exc:
        error = exc
        result = {"contract": "uroboros-katana-shadow-smoke-v1", "passed": False, "error": str(exc)}
    finally:
        recorder_log = _remove(recorder)
        target_log = _remove(target)
        _docker(["network", "rm", network], check=False)
    mutation_reached = b'"mutation":true' in target_log
    result.setdefault("assertions", {})["mutation_reached_target"] = mutation_reached
    result["support_logs"] = {
        "target_sha256": __import__("hashlib").sha256(target_log).hexdigest(),
        "recorder_sha256": __import__("hashlib").sha256(recorder_log).hexdigest(),
    }
    if mutation_reached:
        result["passed"] = False
        error = RuntimeError("scope guard allowed a mutation to reach the fixture")
        result["error"] = str(error)
    report_path = _write_new(destination / ("katana-smoke-%s.json" % token), result)
    if error is not None:
        raise RuntimeError(str(error) + "; smoke report: " + str(report_path)) from error
    return report_path, result


def main(argv=None):
    parser = argparse.ArgumentParser(description="Run the model-free Katana shadow smoke gate")
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--fixture-image", default=DEFAULT_FIXTURE_IMAGE)
    parser.add_argument("--guard-image", default=DEFAULT_GUARD_IMAGE)
    args = parser.parse_args(argv)
    path, result = verify(args.output_dir, fixture_image=args.fixture_image, guard_image=args.guard_image)
    print(json.dumps({"report": str(path), **result}, sort_keys=True, indent=2))


if __name__ == "__main__":
    main()
