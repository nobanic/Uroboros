"""Fail-closed planning and evidence finalization for Schemathesis shadow runs.

This module is pure contract code.  It performs no process, Docker, MCP,
database, planner, or model operations.  The operator runner is the only module
that executes the already-built local image.
"""

from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import urlsplit, urlunsplit

from api_lab.engine import digest
from api_lab.runtime_evidence import (
    import_schemathesis_ndjson,
    make_manifest,
    schemathesis_proxy_traffic_ids,
    schemathesis_terminal_exit_reason,
)
from api_lab.traffic_schema import FORMAT
from api_lab.traffic_schema_projection import loads_executable_traffic_schema


SCHEMATHESIS_VERSION = "4.31.1"
SCHEMATHESIS_SOURCE = "https://github.com/schemathesis/schemathesis"
SCHEMATHESIS_LICENSE = "MIT"
SCHEMATHESIS_IMAGE = "uroboros-api-lab/service:local"
DEFAULT_RECORDING_PROXY = "http://127.0.0.1:8788"
DEFAULT_GUARD_PROXY = "http://127.0.0.1:18789"
REPORT_PATH = "/output/schemathesis.ndjson"
READ_ONLY_METHODS = {"get", "head", "options"}
PHASES = ("coverage", "fuzzing")
CHECKS = (
    "not_a_server_error",
    "status_code_conformance",
    "content_type_conformance",
    "response_schema_conformance",
)
EXPECTED_GUARD_REJECTIONS = {
    "outside_authority",
    "outside_origin",
    "method_not_read_only",
    "transaction_budget",
}
REFERENCE = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}\Z")
CONTAINER_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}\Z")
SHA256 = re.compile(r"(?:sha256:)?([0-9a-fA-F]{64})\Z")


def _canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("ascii")


def _hash(value):
    return hashlib.sha256(value).hexdigest()


def _bounded(value, label, maximum=512):
    if not isinstance(value, str) or not value or len(value) > maximum:
        raise ValueError(label + " must be a non-empty bounded string")
    if any(ord(character) < 32 or ord(character) == 127 for character in value):
        raise ValueError(label + " contains control characters")
    return value


def _reference(value, label):
    if not isinstance(value, str) or not REFERENCE.fullmatch(value):
        raise ValueError(label + " must be an opaque reference")
    return value


def _sha256(value, label):
    if not isinstance(value, str):
        raise ValueError(label + " must be a SHA-256 string")
    match = SHA256.fullmatch(value)
    if not match:
        raise ValueError(label + " must be a SHA-256 string")
    return match.group(1).lower()


def _integer(value, label, lower, upper):
    if not isinstance(value, int) or isinstance(value, bool) or not lower <= value <= upper:
        raise ValueError("%s must be between %d and %d" % (label, lower, upper))
    return value


def _origin(value, label):
    _bounded(value, label, 2048)
    if any(ord(character) <= 32 for character in value):
        raise ValueError(label + " contains whitespace")
    parsed = urlsplit(value)
    if parsed.scheme.lower() not in {"http", "https"} or not parsed.hostname:
        raise ValueError(label + " must be an HTTP(S) origin")
    if parsed.username is not None or parsed.password is not None:
        raise ValueError(label + " must not contain credentials")
    try:
        port = parsed.port
    except ValueError:
        raise ValueError(label + " contains an invalid port") from None
    if parsed.path not in {"", "/"} or parsed.query or parsed.fragment:
        raise ValueError(label + " must contain only an origin")
    host = parsed.hostname.lower()
    if ":" in host:
        host = "[" + host.strip("[]") + "]"
    default_port = 80 if parsed.scheme.lower() == "http" else 443
    netloc = host + ((":" + str(port)) if port is not None and port != default_port else "")
    return urlunsplit((parsed.scheme.lower(), netloc, "", "", ""))


def _loopback_proxy(value, label):
    selected = _origin(value, label)
    parsed = urlsplit(selected)
    if parsed.scheme != "http" or parsed.hostname not in {"127.0.0.1", "::1", "localhost"} or parsed.port is None:
        raise ValueError(label + " must be an explicit loopback HTTP proxy")
    return selected


def _external_references(value):
    if isinstance(value, dict):
        for key, item in value.items():
            if key == "$ref" and (not isinstance(item, str) or not item.startswith("#/")):
                yield item
            yield from _external_references(item)
    elif isinstance(value, list):
        for item in value:
            yield from _external_references(item)


def schema_metadata(document, artifact_sha256):
    """Validate the anonymous read-only execution boundary and return provenance."""
    source_hash = _sha256(artifact_sha256, "schema artifact hash")
    canonical_artifact = _canonical(document) + b"\n"
    if _hash(canonical_artifact) != source_hash:
        raise ValueError("schema artifact hash does not match the canonical document")
    server = document["servers"][0]["url"]
    selected_origin = _origin(server, "schema server")
    if server.rstrip("/") != selected_origin:
        raise ValueError("schema server must use one canonical exact origin")
    if urlsplit(selected_origin).scheme != "http":
        raise ValueError("phase-2 Schemathesis shadow runs require a controlled HTTP target")
    if list(_external_references(document)):
        raise ValueError("executable schema contains an external or invalid $ref")
    operations = [
        (path, method)
        for path, path_item in document["paths"].items()
        for method in path_item
    ]
    write_operations = sorted(
        "%s %s" % (method.upper(), path)
        for path, method in operations
        if method not in READ_ONLY_METHODS
    )
    if write_operations:
        raise ValueError("phase-2 executable schema contains a state-changing operation")
    traffic = document["x-uroboros-traffic"]
    projection = traffic.get("projection")
    result = {
        "artifact_sha256": source_hash,
        "schema_id": digest(document),
        "format": FORMAT,
        "server_origin": selected_origin,
        "paths": len(document["paths"]),
        "operations": len(operations),
        "evidence": len(traffic["evidence"]),
        "dependency_candidates": len(traffic["dependency_candidates"]),
        "projected": projection is not None,
        "projection_source_artifact_sha256": None,
        "projection_source_schema_id": None,
    }
    if projection is not None:
        result["projection_source_artifact_sha256"] = projection["source_artifact_sha256"]
        result["projection_source_schema_id"] = projection["source_schema_id"]
    return result


def load_executable_schema(data, expected_sha256):
    document = loads_executable_traffic_schema(data, expected_sha256)
    return document, schema_metadata(document, expected_sha256)


@dataclass(frozen=True)
class SchemathesisShadowPlan:
    run_id: str
    target_origin: str
    target_revision: str
    scope_ref: str
    network_container: str
    schema_sha256: str
    schema_id: str
    schema_paths: int
    schema_operations: int
    schema_evidence: int
    schema_dependency_candidates: int
    schema_projected: bool
    schema_projection_source_sha256: str | None = None
    schema_projection_source_id: str | None = None
    recording_proxy: str = DEFAULT_RECORDING_PROXY
    max_requests: int = 200
    max_seconds: int = 30
    max_concurrency: int = 1
    rate_limit_per_second: int = 4
    max_examples_per_operation: int = 10
    max_failures: int = 20
    request_timeout: int = 5
    max_response_bytes: int = 1_048_576
    seed: int = 1

    def __post_init__(self):
        if not isinstance(self.run_id, str) or len(self.run_id) > 80 or not CONTAINER_NAME.fullmatch(self.run_id):
            raise ValueError("run_id must be a filename-safe opaque reference")
        selected_origin = _origin(self.target_origin, "target origin")
        if selected_origin != self.target_origin:
            raise ValueError("target origin must be canonical")
        if urlsplit(selected_origin).scheme != "http":
            raise ValueError("phase-2 Schemathesis shadow runs require a controlled HTTP target")
        _bounded(self.target_revision, "target revision")
        _reference(self.scope_ref, "scope reference")
        if not isinstance(self.network_container, str) or not CONTAINER_NAME.fullmatch(self.network_container):
            raise ValueError("network container name is invalid")
        if _sha256(self.schema_sha256, "schema artifact hash") != self.schema_sha256:
            raise ValueError("schema artifact hash must be canonical lowercase")
        if _sha256(self.schema_id, "schema ID") != self.schema_id:
            raise ValueError("schema ID must be canonical lowercase")
        _integer(self.schema_paths, "schema paths", 1, 10000)
        _integer(self.schema_operations, "schema operations", 1, 10000)
        _integer(self.schema_evidence, "schema evidence", 1, 1_000_000)
        _integer(self.schema_dependency_candidates, "schema dependency candidates", 0, 1_000_000)
        if not isinstance(self.schema_projected, bool):
            raise ValueError("schema_projected must be boolean")
        if self.schema_projected:
            if _sha256(self.schema_projection_source_sha256, "projection source artifact hash") != self.schema_projection_source_sha256:
                raise ValueError("projection source artifact hash must be canonical lowercase")
            if _sha256(self.schema_projection_source_id, "projection source schema ID") != self.schema_projection_source_id:
                raise ValueError("projection source schema ID must be canonical lowercase")
        elif self.schema_projection_source_sha256 is not None or self.schema_projection_source_id is not None:
            raise ValueError("unprojected schema must not claim projection provenance")
        _loopback_proxy(self.recording_proxy, "recording proxy")
        _integer(self.max_requests, "max_requests", 20, 1000)
        _integer(self.max_seconds, "max_seconds", 5, 120)
        _integer(self.max_concurrency, "max_concurrency", 1, 4)
        _integer(self.rate_limit_per_second, "rate_limit_per_second", 1, 20)
        _integer(self.max_examples_per_operation, "max_examples_per_operation", 1, 100)
        _integer(self.max_failures, "max_failures", 1, 100)
        _integer(self.request_timeout, "request_timeout", 1, 15)
        _integer(self.max_response_bytes, "max_response_bytes", 1024, 4 * 1024 * 1024)
        _integer(self.seed, "seed", 0, 2**32 - 1)
        if self.request_timeout > self.max_seconds:
            raise ValueError("request_timeout must not exceed max_seconds")
        # max-examples is per operation, not a global cap.  The rate/time bound
        # is therefore kept below the network guard's hard transaction budget.
        if self.rate_limit_per_second * self.max_seconds + self.request_margin > self.max_requests:
            raise ValueError("rate/time envelope plus safety margin exceeds max_requests")

    @property
    def request_margin(self):
        return max(8, self.max_concurrency * 2)

    @property
    def expected_request_upper_bound(self):
        return self.rate_limit_per_second * self.max_seconds + self.request_margin

    @property
    def origin(self):
        """Compatibility with the shared exact-origin scope-guard launcher."""
        return self.target_origin


def make_plan(*, document, schema_sha256, target_origin, **values):
    metadata = schema_metadata(document, schema_sha256)
    selected_target = _origin(target_origin, "target origin")
    if selected_target != target_origin or selected_target != metadata["server_origin"]:
        raise ValueError("target origin must exactly match the schema server")
    return SchemathesisShadowPlan(
        target_origin=selected_target,
        schema_sha256=metadata["artifact_sha256"],
        schema_id=metadata["schema_id"],
        schema_paths=metadata["paths"],
        schema_operations=metadata["operations"],
        schema_evidence=metadata["evidence"],
        schema_dependency_candidates=metadata["dependency_candidates"],
        schema_projected=metadata["projected"],
        schema_projection_source_sha256=metadata["projection_source_artifact_sha256"],
        schema_projection_source_id=metadata["projection_source_schema_id"],
        **values,
    )


def schemathesis_argv(plan: SchemathesisShadowPlan, guard_proxy=DEFAULT_GUARD_PROXY):
    proxy = _loopback_proxy(guard_proxy, "scope guard proxy")
    return [
        "schemathesis", "run", "/input/openapi.json",
        "--url", plan.target_origin,
        "--phases", ",".join(PHASES),
        "--checks", ",".join(CHECKS),
        "--workers", str(plan.max_concurrency),
        "--max-time", str(plan.max_seconds),
        "--max-examples", str(plan.max_examples_per_operation),
        "--max-failures", str(plan.max_failures),
        "--proxy", proxy,
        "--rate-limit", "%d/s" % plan.rate_limit_per_second,
        "--max-redirects", "0",
        "--request-timeout", str(plan.request_timeout),
        "--request-retries", "0",
        "--seed", str(plan.seed),
        "--no-shrink",
        "--generation-deterministic",
        "--generation-with-security-parameters", "false",
        "--report", "ndjson",
        "--report-dir", "/output",
        "--report-ndjson-path", REPORT_PATH,
        "--output-sanitize", "true",
        "--output-truncate", "true",
        "--warnings", "off",
        "--no-color",
    ]


def plan_document(plan: SchemathesisShadowPlan, guard_proxy=DEFAULT_GUARD_PROXY):
    return {
        "contract": "uroboros-schemathesis-shadow-v1",
        "run_id": plan.run_id,
        "target_origin": plan.target_origin,
        "target_revision": plan.target_revision,
        "scope_ref": plan.scope_ref,
        "network_container": plan.network_container,
        "recording_proxy": _loopback_proxy(plan.recording_proxy, "recording proxy"),
        "scope_guard_proxy": _loopback_proxy(guard_proxy, "scope guard proxy"),
        "schema": {
            "artifact_sha256": plan.schema_sha256,
            "schema_id": plan.schema_id,
            "format": FORMAT,
            "paths": plan.schema_paths,
            "operations": plan.schema_operations,
            "evidence": plan.schema_evidence,
            "dependency_candidates": plan.schema_dependency_candidates,
            "projected": plan.schema_projected,
            "projection_source_artifact_sha256": plan.schema_projection_source_sha256,
            "projection_source_schema_id": plan.schema_projection_source_id,
        },
        "budgets": {
            "max_requests": plan.max_requests,
            "max_seconds": plan.max_seconds,
            "max_concurrency": plan.max_concurrency,
            "rate_limit_per_second": plan.rate_limit_per_second,
            "request_margin": plan.request_margin,
            "expected_request_upper_bound": plan.expected_request_upper_bound,
            "max_examples_per_operation": plan.max_examples_per_operation,
            "max_failures": plan.max_failures,
            "request_timeout": plan.request_timeout,
            "max_response_bytes": plan.max_response_bytes,
        },
        "schemathesis": {
            "version": SCHEMATHESIS_VERSION,
            "image": SCHEMATHESIS_IMAGE,
            "phases": list(PHASES),
            "checks": list(CHECKS),
            "seed": plan.seed,
            "argv": schemathesis_argv(plan, guard_proxy),
        },
        "disabled_integrations": sorted([
            "api_lab_database",
            "authentication",
            "mcp_registration",
            "model_calls",
            "planner",
            "remote_schema_fetch",
            "stateful_phase",
            "vvah",
            "wfc_auth",
            "wfc_report",
            "write_operations",
        ]),
    }


def plan_sha256(plan: SchemathesisShadowPlan, guard_proxy=DEFAULT_GUARD_PROXY):
    return _hash(_canonical(plan_document(plan, guard_proxy)))


def _assert_schema_binding(plan, document, artifact_sha256):
    metadata = schema_metadata(document, artifact_sha256)
    expected = {
        "artifact_sha256": plan.schema_sha256,
        "schema_id": plan.schema_id,
        "server_origin": plan.target_origin,
        "paths": plan.schema_paths,
        "operations": plan.schema_operations,
        "evidence": plan.schema_evidence,
        "dependency_candidates": plan.schema_dependency_candidates,
        "projected": plan.schema_projected,
        "projection_source_artifact_sha256": plan.schema_projection_source_sha256,
        "projection_source_schema_id": plan.schema_projection_source_id,
    }
    if any(metadata[key] != value for key, value in expected.items()):
        raise ValueError("executable schema no longer matches the shadow plan")


def schemathesis_docker_argv(
    plan: SchemathesisShadowPlan,
    *,
    container_name,
    schema_path,
    output_dir,
    image_ref=SCHEMATHESIS_IMAGE,
    guard_proxy=DEFAULT_GUARD_PROXY,
):
    """Build a hardened one-shot Docker argv for already staged schema bytes."""
    if not isinstance(container_name, str) or not CONTAINER_NAME.fullmatch(container_name):
        raise ValueError("Schemathesis container name is invalid")
    _bounded(image_ref, "Schemathesis image reference", 512)
    selected_schema = Path(schema_path).resolve()
    selected_output = Path(output_dir).resolve()
    if not selected_schema.is_file():
        raise ValueError("staged schema file does not exist")
    if not selected_output.is_dir():
        raise ValueError("staged report directory does not exist")
    raw = selected_schema.read_bytes()
    document = loads_executable_traffic_schema(raw, plan.schema_sha256)
    _assert_schema_binding(plan, document, plan.schema_sha256)
    argv = [
        "docker", "run", "--rm", "--pull", "never",
        "--name", container_name,
        "--network", "container:" + plan.network_container,
        "--read-only",
        "--tmpfs", "/tmp:rw,nosuid,nodev,size=256m",
        "--cap-drop", "ALL",
        "--security-opt", "no-new-privileges:true",
        "--pids-limit", "128",
        "--memory", "768m",
        "--cpus", "1.0",
        "--ulimit", "nofile=1024:1024",
        "--user", "65532:65532",
        "--workdir", "/tmp",
        "--env", "HOME=/tmp",
        "--env", "XDG_CACHE_HOME=/tmp/.cache",
        "--env", "XDG_CONFIG_HOME=/tmp/.config",
        "--env", "HYPOTHESIS_STORAGE_DIRECTORY=/tmp/.hypothesis",
        "--env", "NO_COLOR=1",
        "--volume", str(selected_schema) + ":/input/openapi.json:ro",
        "--volume", str(selected_output) + ":/output:rw",
        image_ref,
    ]
    argv.extend(schemathesis_argv(plan, guard_proxy))
    return argv


def _validate_guard_coverage(plan, coverage):
    if not isinstance(coverage, dict):
        raise ValueError("Schemathesis shadow evidence requires scope-guard coverage")
    for key in ("allowed", "rejected", "upstream_errors"):
        value = coverage.get(key)
        if not isinstance(value, int) or isinstance(value, bool) or value < 0:
            raise ValueError("scope-guard coverage counter is invalid: " + key)
    reasons = coverage.get("rejected_reasons")
    if not isinstance(reasons, dict) or any(
        not isinstance(reason, str)
        or not isinstance(count, int)
        or isinstance(count, bool)
        or count <= 0
        for reason, count in reasons.items()
    ):
        raise ValueError("scope-guard rejection reasons are invalid")
    unexpected = sorted(set(reasons) - EXPECTED_GUARD_REJECTIONS)
    if unexpected:
        raise ValueError("scope-guard rejection reasons are unsupported: " + ", ".join(unexpected))
    if sum(reasons.values()) != coverage["rejected"]:
        raise ValueError("scope-guard rejection reasons do not match the rejection counter")
    transactions = coverage.get("transactions")
    if not isinstance(transactions, int) or isinstance(transactions, bool) or transactions <= 0:
        raise ValueError("scope-guard transaction counter is invalid")
    if transactions != coverage["allowed"] + coverage["rejected"]:
        raise ValueError("scope-guard transaction counter does not match allowed and rejected traffic")
    budget_rejections = reasons.get("transaction_budget", 0)
    if budget_rejections:
        if transactions <= plan.max_requests:
            raise ValueError("transaction-budget rejection does not exceed the configured budget")
    elif transactions > plan.max_requests:
        raise ValueError("scope-guard transactions exceed max_requests without a budget rejection")
    if coverage["upstream_errors"] != 0:
        raise ValueError("scope-guard coverage contains an upstream error")
    return budget_rejections


def finalize_evidence(
    raw_ndjson,
    plan: SchemathesisShadowPlan,
    *,
    started_at,
    finished_at,
    tool_sha256,
    guard_coverage,
    exit_reason,
    truncated,
    guard_proxy=DEFAULT_GUARD_PROXY,
):
    """Create evidence only from a terminal report and proxy-confirmed responses."""
    mapped_reason = schemathesis_terminal_exit_reason(raw_ndjson)
    if mapped_reason != exit_reason:
        raise ValueError("Schemathesis terminal reason differs from evidence metadata")
    if not isinstance(truncated, bool) or truncated != (exit_reason != "completed"):
        raise ValueError("Schemathesis truncated state is inconsistent with its terminal reason")
    traffic_ids = schemathesis_proxy_traffic_ids(raw_ndjson)
    if len(traffic_ids) > plan.max_requests:
        raise ValueError("Schemathesis proxy-confirmed responses exceed max_requests")
    budget_rejections = _validate_guard_coverage(plan, guard_coverage)
    if budget_rejections and (exit_reason != "budget_exhausted" or truncated is not True):
        raise ValueError("transaction-budget evidence must be marked budget_exhausted and truncated")
    if guard_coverage["allowed"] < len(traffic_ids):
        raise ValueError("scope-guard coverage cannot account for proxy-confirmed responses")
    coverage = {
        "schema": plan_document(plan, guard_proxy)["schema"],
        "scope_ref": plan.scope_ref,
        "execution": {
            "phases": list(PHASES),
            "checks": list(CHECKS),
            "anonymous": True,
            "deterministic_generation": True,
            "stateful": False,
            "wfc": False,
        },
        "recording_proxy": {
            "capture_request_header": "x-uroboros-capture-request",
            "traffic_id_header": "x-uroboros-traffic-id",
            "confirmed_responses": len(traffic_ids),
            "traffic_ids": sorted(traffic_ids),
        },
        "scope_guard": guard_coverage,
    }
    limitations = [
        "Anonymous read-only shadow run only; authentication and WFC are disabled in phase 2.",
        "The source schema is inferred from selected captured traffic and is not a declared API contract.",
        "Schemathesis failures remain hypotheses until API Lab controls and independent replay succeed.",
        "Shadow evidence is not registered with MCP and is invisible to the planner and agents.",
        "VVAH is disabled and is neither invoked nor required.",
    ]
    if truncated:
        limitations.append("The run stopped before exhaustive completion and cannot support absence or full-coverage claims.")
    manifest = make_manifest(
        tool_name="schemathesis",
        tool_version=SCHEMATHESIS_VERSION,
        tool_sha256=_sha256(tool_sha256, "Schemathesis image ID"),
        tool_source=SCHEMATHESIS_SOURCE,
        license_id=SCHEMATHESIS_LICENSE,
        run_id=plan.run_id,
        target_origin=plan.target_origin,
        target_revision=plan.target_revision,
        scope_origins=[plan.target_origin],
        started_at=started_at,
        finished_at=finished_at,
        exit_reason=exit_reason,
        budgets={
            "max_requests": plan.max_requests,
            "max_seconds": plan.max_seconds,
            "max_concurrency": plan.max_concurrency,
        },
        config_sha256=plan_sha256(plan, guard_proxy),
        identity_refs=[],
        coverage=coverage,
        truncated=truncated,
        limitations=limitations,
    )
    bundle = import_schemathesis_ndjson(
        raw_ndjson,
        manifest,
        proxy_confirmed_only=True,
    )
    observed_ids = sorted(
        node["data"]["traffic_id"]
        for node in bundle["nodes"]
        if node["kind"] == "response_observation" and "traffic_id" in node["data"]
    )
    if observed_ids != sorted(traffic_ids):
        raise ValueError("Schemathesis evidence lost recording-proxy lineage")
    imported_rejections = bundle["summary"]["details"].get("scope_guard_rejections", 0)
    if guard_coverage["rejected"] < imported_rejections:
        raise ValueError("scope-guard coverage lost a Schemathesis rejection record")
    if bundle["summary"]["claims"]["findings"] != 0:
        raise ValueError("Schemathesis shadow adapter attempted to promote a finding")
    return bundle
