"""Pure contracts for authenticated Schemathesis stateful shadow runs.

This module validates frozen Traffic-to-spec and API Lab identity inputs,
creates an ephemeral WFC fixed-header document, and finalizes proxy-confirmed
evidence. It performs no Docker, network, MCP, database, planner, or model work.
"""

from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import urlsplit

from api_lab.engine import digest
from api_lab.katana_scope_proxy import READ_ONLY_METHODS, SUPPORTED_HTTP_METHODS
from api_lab.runtime_evidence import (
    import_schemathesis_ndjson,
    make_manifest,
    schemathesis_proxy_traffic_ids,
    schemathesis_terminal_exit_reason,
)
from api_lab.schemathesis_shadow import (
    CONTAINER_NAME,
    DEFAULT_GUARD_PROXY,
    DEFAULT_RECORDING_PROXY,
    SCHEMATHESIS_IMAGE,
    SCHEMATHESIS_LICENSE,
    SCHEMATHESIS_SOURCE,
    SCHEMATHESIS_VERSION,
    _bounded,
    _canonical,
    _external_references,
    _hash,
    _integer,
    _loopback_proxy,
    _origin,
    _reference,
    _sha256,
)
from api_lab.traffic_schema import FORMAT
from api_lab.traffic_schema_projection import loads_executable_traffic_schema


CONTRACT = "uroboros-schemathesis-wfc-shadow-v1"
PHASES = ("stateful",)
CHECKS = (
    "not_a_server_error",
    "status_code_conformance",
    "response_schema_conformance",
    "object_level_authorization",
)
REPORT_PATH = "/output/schemathesis.ndjson"
JSON_REPORT_PATH = "/output/schemathesis.json"
WFC_REPORT_PATH = "/output/wfc.json"
WFC_SCRIPT_PATH = "/output/wfc.sh"
MAX_IDENTITY_BYTES = 1024 * 1024
HEADER_NAME = re.compile(r"[!#$%&'*+.^_`|~0-9A-Za-z-]+\Z")
FORBIDDEN_HEADERS = {
    "connection",
    "content-length",
    "host",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
    "x-uroboros-capture-request",
    "x-uroboros-scope-guard",
    "x-uroboros-traffic-id",
}
EXPECTED_GUARD_REJECTIONS = {
    "outside_authority",
    "outside_origin",
    "method_not_allowed",
    "transaction_budget",
}


def _duplicate_rejecting_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("identity configuration contains a duplicate JSON key")
        result[key] = value
    return result


def _reject_constant(_):
    raise ValueError("identity configuration contains a non-finite JSON number")


def _identity_refs(value):
    if not isinstance(value, (list, tuple)) or not 2 <= len(value) <= 8:
        raise ValueError("WFC requires between two and eight peer identities")
    result = tuple(_reference(item, "peer identity") for item in value)
    if "anonymous" in result or len(set(result)) != len(result):
        raise ValueError("WFC peers must be unique named identities")
    return result


@dataclass(frozen=True, repr=False)
class WfcIdentityMaterial:
    source_sha256: str
    wfc_sha256: str
    identity_refs: tuple[str, ...]
    header_names: tuple[tuple[str, tuple[str, ...]], ...]
    wfc_bytes: bytes
    secrets: tuple[bytes, ...]

    def descriptor(self):
        return [
            {"identity_ref": name, "header_names": list(headers)}
            for name, headers in self.header_names
        ]


def load_identity_material(data, expected_sha256, target_origin, peers, max_bytes=MAX_IDENTITY_BYTES):
    """Validate exact API Lab identity bytes and derive one ephemeral WFC file."""
    if not isinstance(data, (bytes, bytearray)):
        raise ValueError("identity configuration must be bytes")
    raw = bytes(data)
    if not raw or len(raw) > max_bytes:
        raise ValueError("identity configuration is empty or exceeds the byte limit")
    expected = _sha256(expected_sha256, "identity configuration hash")
    if hashlib.sha256(raw).hexdigest() != expected:
        raise ValueError("identity configuration digest mismatch")
    selected_origin = _origin(target_origin, "target origin")
    selected_peers = _identity_refs(peers)
    try:
        document = json.loads(
            raw.decode("utf-8"),
            object_pairs_hook=_duplicate_rejecting_object,
            parse_constant=_reject_constant,
        )
    except UnicodeDecodeError:
        raise ValueError("identity configuration is not UTF-8") from None
    except json.JSONDecodeError:
        raise ValueError("identity configuration is invalid JSON") from None
    if not isinstance(document, dict) or set(document) != {"identities"}:
        raise ValueError("identity configuration has unsupported top-level fields")
    entries = document["identities"]
    if not isinstance(entries, dict):
        raise ValueError("identity configuration identities must be an object")

    auth = []
    descriptors = []
    fingerprints = set()
    secrets = set()
    for name in selected_peers:
        entry = entries.get(name)
        if not isinstance(entry, dict) or set(entry) != {"origins", "headers"}:
            raise ValueError("peer identity is missing or has unsupported fields: " + name)
        origins = entry["origins"]
        if not isinstance(origins, list) or not origins:
            raise ValueError("peer identity needs at least one exact origin: " + name)
        normalized_origins = []
        for value in origins:
            normalized = _origin(value, "identity origin")
            if normalized != value.rstrip("/"):
                raise ValueError("identity origin must be canonical")
            normalized_origins.append(normalized)
        if len(set(normalized_origins)) != len(normalized_origins):
            raise ValueError("peer identity contains duplicate origins: " + name)
        if selected_origin not in normalized_origins:
            raise ValueError("peer identity is not configured for the target origin: " + name)

        headers = entry["headers"]
        if not isinstance(headers, dict) or not 1 <= len(headers) <= 16:
            raise ValueError("peer identity needs between one and sixteen headers: " + name)
        fixed = []
        seen_names = set()
        fingerprint = []
        for header_name, value in headers.items():
            if not isinstance(header_name, str) or not HEADER_NAME.fullmatch(header_name):
                raise ValueError("identity header name is invalid")
            lowered = header_name.lower()
            if lowered in seen_names or lowered in FORBIDDEN_HEADERS:
                raise ValueError("identity header is duplicate or reserved: " + header_name)
            seen_names.add(lowered)
            if (
                not isinstance(value, str)
                or not 1 <= len(value) <= 8192
                or any(ord(character) < 32 or ord(character) > 126 for character in value)
            ):
                raise ValueError("identity header value must be bounded printable ASCII")
            fixed.append({"name": header_name, "value": value})
            fingerprint.append((lowered, value))
            secrets.add(value)
            if value.lower().startswith("bearer ") and value[7:]:
                secrets.add(value[7:])
        identity_fingerprint = json.dumps(sorted(fingerprint), separators=(",", ":"))
        if identity_fingerprint in fingerprints:
            raise ValueError("WFC peer identities must not use identical credentials")
        fingerprints.add(identity_fingerprint)
        fixed.sort(key=lambda item: item["name"].lower())
        auth.append({"name": name, "fixedHeaders": fixed})
        descriptors.append((name, tuple(item["name"] for item in fixed)))

    wfc_bytes = json.dumps(
        {"schemaVersion": "1.0", "auth": auth},
        sort_keys=True,
        separators=(",", ":"),
        ensure_ascii=True,
    ).encode("ascii") + b"\n"
    redactions = set()
    for secret in secrets:
        redactions.add(secret.encode("ascii"))
        redactions.add(json.dumps(secret, ensure_ascii=True)[1:-1].encode("ascii"))
    return WfcIdentityMaterial(
        source_sha256=expected,
        wfc_sha256=hashlib.sha256(wfc_bytes).hexdigest(),
        identity_refs=selected_peers,
        header_names=tuple(descriptors),
        wfc_bytes=wfc_bytes,
        secrets=tuple(sorted(redactions, key=len, reverse=True)),
    )


def redact_identity_secrets(data, material):
    if not isinstance(data, (bytes, bytearray)):
        raise ValueError("artifact to redact must be bytes")
    result = bytes(data)
    for secret in material.secrets:
        if secret:
            result = result.replace(secret, b"[REDACTED]")
    if any(secret and secret in result for secret in material.secrets):
        raise ValueError("identity secret remains after artifact redaction")
    return result


def schema_metadata(document, artifact_sha256):
    """Validate the narrow stateful execution boundary and return provenance."""
    source_hash = _sha256(artifact_sha256, "schema artifact hash")
    if _hash(_canonical(document) + b"\n") != source_hash:
        raise ValueError("schema artifact hash does not match the canonical document")
    server = document["servers"][0]["url"]
    selected_origin = _origin(server, "schema server")
    if server.rstrip("/") != selected_origin or urlsplit(selected_origin).scheme != "http":
        raise ValueError("WFC shadow runs require one canonical controlled HTTP origin")
    if list(_external_references(document)):
        raise ValueError("WFC executable schema contains an external or invalid $ref")
    operations = {
        operation["operationId"]: (path, method)
        for path, path_item in document["paths"].items()
        for method, operation in path_item.items()
    }
    schema_methods = {method.upper() for _, method in operations.values()}
    mutation_methods = schema_methods - READ_ONLY_METHODS
    if not mutation_methods:
        raise ValueError("WFC stateful execution requires an observed state-changing operation")
    if "GET" not in schema_methods:
        raise ValueError("WFC BOLA execution requires an observed GET operation")
    dependencies = document["x-uroboros-traffic"]["dependency_candidates"]
    usable = []
    for item in dependencies:
        consumer = operations.get(item["consumer_operation_id"])
        response_name = item["response_pointer"].rsplit("/", 1)[-1]
        parameter_name = item["parameter_pointer"].rsplit("/", 1)[-1]
        if (
            item["location"] == "path"
            and consumer is not None
            and consumer[1] == "get"
            and "{" + parameter_name + "}" in consumer[0]
            and response_name == parameter_name
        ):
            usable.append(item)
    if not usable:
        raise ValueError("WFC stateful execution requires a semantic response-to-GET path dependency")
    traffic = document["x-uroboros-traffic"]
    projection = traffic.get("projection")
    allowed_methods = tuple(sorted(schema_methods | READ_ONLY_METHODS))
    if not set(allowed_methods) <= SUPPORTED_HTTP_METHODS:
        raise ValueError("WFC schema contains a method unsupported by the scope guard")
    return {
        "artifact_sha256": source_hash,
        "schema_id": digest(document),
        "format": FORMAT,
        "server_origin": selected_origin,
        "paths": len(document["paths"]),
        "operations": len(operations),
        "evidence": len(traffic["evidence"]),
        "dependency_candidates": len(dependencies),
        "usable_stateful_dependencies": len(usable),
        "projected": projection is not None,
        "projection_source_artifact_sha256": projection["source_artifact_sha256"] if projection else None,
        "projection_source_schema_id": projection["source_schema_id"] if projection else None,
        "allowed_methods": allowed_methods,
        "mutation_methods": tuple(sorted(mutation_methods)),
    }


def load_executable_schema(data, expected_sha256):
    document = loads_executable_traffic_schema(data, expected_sha256)
    return document, schema_metadata(document, expected_sha256)


@dataclass(frozen=True)
class SchemathesisWfcPlan:
    run_id: str
    target_origin: str
    target_revision: str
    scope_ref: str
    mutation_scope_ref: str
    reset_ref: str
    network_container: str
    schema_sha256: str
    schema_id: str
    schema_paths: int
    schema_operations: int
    schema_evidence: int
    schema_dependency_candidates: int
    schema_usable_stateful_dependencies: int
    schema_projected: bool
    allowed_methods: tuple[str, ...]
    mutation_methods: tuple[str, ...]
    identity_config_sha256: str
    wfc_auth_sha256: str
    identity_refs: tuple[str, ...]
    identity_header_names: tuple[tuple[str, tuple[str, ...]], ...]
    schema_projection_source_sha256: str | None = None
    schema_projection_source_id: str | None = None
    recording_proxy: str = DEFAULT_RECORDING_PROXY
    max_requests: int = 300
    max_seconds: int = 30
    rate_limit_per_second: int = 4
    max_examples_per_operation: int = 10
    max_failures: int = 20
    request_timeout: int = 5
    max_response_bytes: int = 1_048_576
    max_steps: int = 2
    seed: int = 1

    def __post_init__(self):
        if not isinstance(self.run_id, str) or len(self.run_id) > 80 or not CONTAINER_NAME.fullmatch(self.run_id):
            raise ValueError("run_id must be a filename-safe opaque reference")
        selected_origin = _origin(self.target_origin, "target origin")
        if selected_origin != self.target_origin or urlsplit(selected_origin).scheme != "http":
            raise ValueError("WFC shadow runs require a canonical controlled HTTP origin")
        _bounded(self.target_revision, "target revision")
        _reference(self.scope_ref, "scope reference")
        _reference(self.mutation_scope_ref, "mutation scope reference")
        _reference(self.reset_ref, "target reset reference")
        if not isinstance(self.network_container, str) or not CONTAINER_NAME.fullmatch(self.network_container):
            raise ValueError("network container name is invalid")
        if _sha256(self.schema_sha256, "schema artifact hash") != self.schema_sha256:
            raise ValueError("schema artifact hash must be canonical lowercase")
        if _sha256(self.schema_id, "schema ID") != self.schema_id:
            raise ValueError("schema ID must be canonical lowercase")
        if _sha256(self.identity_config_sha256, "identity configuration hash") != self.identity_config_sha256:
            raise ValueError("identity configuration hash must be canonical lowercase")
        if _sha256(self.wfc_auth_sha256, "WFC auth hash") != self.wfc_auth_sha256:
            raise ValueError("WFC auth hash must be canonical lowercase")
        _integer(self.schema_paths, "schema paths", 1, 10000)
        _integer(self.schema_operations, "schema operations", 2, 10000)
        _integer(self.schema_evidence, "schema evidence", 2, 1_000_000)
        _integer(self.schema_dependency_candidates, "schema dependency candidates", 1, 1_000_000)
        _integer(self.schema_usable_stateful_dependencies, "usable stateful dependencies", 1, 1_000_000)
        if not isinstance(self.schema_projected, bool):
            raise ValueError("schema_projected must be boolean")
        if self.schema_projected:
            if _sha256(self.schema_projection_source_sha256, "projection source artifact hash") != self.schema_projection_source_sha256:
                raise ValueError("projection source artifact hash must be canonical lowercase")
            if _sha256(self.schema_projection_source_id, "projection source schema ID") != self.schema_projection_source_id:
                raise ValueError("projection source schema ID must be canonical lowercase")
        elif self.schema_projection_source_sha256 is not None or self.schema_projection_source_id is not None:
            raise ValueError("unprojected schema must not claim projection provenance")
        if self.allowed_methods != tuple(sorted(set(self.allowed_methods))) or not set(self.allowed_methods) <= SUPPORTED_HTTP_METHODS:
            raise ValueError("allowed methods must be canonical and supported")
        expected_mutations = tuple(sorted(set(self.allowed_methods) - READ_ONLY_METHODS))
        if not expected_mutations or self.mutation_methods != expected_mutations:
            raise ValueError("mutation methods do not match allowed methods")
        selected_refs = _identity_refs(self.identity_refs)
        if selected_refs != self.identity_refs:
            raise ValueError("identity refs must preserve their validated order")
        if tuple(name for name, _ in self.identity_header_names) != self.identity_refs:
            raise ValueError("identity header descriptors disagree with peer refs")
        for _, names in self.identity_header_names:
            if not names or tuple(sorted(names, key=str.lower)) != names:
                raise ValueError("identity header descriptors are not canonical")
        _loopback_proxy(self.recording_proxy, "recording proxy")
        _integer(self.max_requests, "max_requests", 30, 1000)
        _integer(self.max_seconds, "max_seconds", 5, 120)
        _integer(self.rate_limit_per_second, "rate_limit_per_second", 1, 20)
        _integer(self.max_examples_per_operation, "max_examples_per_operation", 1, 100)
        _integer(self.max_failures, "max_failures", 1, 100)
        _integer(self.request_timeout, "request_timeout", 1, 15)
        _integer(self.max_response_bytes, "max_response_bytes", 1024, 4 * 1024 * 1024)
        _integer(self.max_steps, "max_steps", 2, 5)
        _integer(self.seed, "seed", 0, 2**32 - 1)
        if self.request_timeout > self.max_seconds:
            raise ValueError("request_timeout must not exceed max_seconds")
        if self.rate_limit_per_second * self.max_seconds + self.request_margin > self.max_requests:
            raise ValueError("rate/time envelope plus WFC safety margin exceeds max_requests")

    @property
    def request_margin(self):
        return max(16, len(self.identity_refs) * 4)

    @property
    def expected_request_upper_bound(self):
        return self.rate_limit_per_second * self.max_seconds + self.request_margin

    @property
    def origin(self):
        return self.target_origin


def make_plan(*, document, schema_sha256, target_origin, identity_material, **values):
    metadata = schema_metadata(document, schema_sha256)
    selected_target = _origin(target_origin, "target origin")
    if selected_target != target_origin or selected_target != metadata["server_origin"]:
        raise ValueError("target origin must exactly match the schema server")
    if not isinstance(identity_material, WfcIdentityMaterial):
        raise ValueError("validated WFC identity material is required")
    return SchemathesisWfcPlan(
        target_origin=selected_target,
        schema_sha256=metadata["artifact_sha256"],
        schema_id=metadata["schema_id"],
        schema_paths=metadata["paths"],
        schema_operations=metadata["operations"],
        schema_evidence=metadata["evidence"],
        schema_dependency_candidates=metadata["dependency_candidates"],
        schema_usable_stateful_dependencies=metadata["usable_stateful_dependencies"],
        schema_projected=metadata["projected"],
        schema_projection_source_sha256=metadata["projection_source_artifact_sha256"],
        schema_projection_source_id=metadata["projection_source_schema_id"],
        allowed_methods=metadata["allowed_methods"],
        mutation_methods=metadata["mutation_methods"],
        identity_config_sha256=identity_material.source_sha256,
        wfc_auth_sha256=identity_material.wfc_sha256,
        identity_refs=identity_material.identity_refs,
        identity_header_names=identity_material.header_names,
        **values,
    )


def wfc_config_bytes(plan):
    peers = ", ".join(json.dumps(name, ensure_ascii=True) for name in plan.identity_refs)
    return (
        "[auth.wfc]\n"
        "path = \"/input/auth.json\"\n"
        "refresh_interval = 300\n"
        "peers = [" + peers + "]\n\n"
        "[phases.stateful]\n"
        "max-steps = " + str(plan.max_steps) + "\n"
    ).encode("ascii")


def schemathesis_argv(plan, guard_proxy=DEFAULT_GUARD_PROXY):
    proxy = _loopback_proxy(guard_proxy, "scope guard proxy")
    return [
        "schemathesis", "--config-file", "/input/schemathesis.toml",
        "run", "/input/openapi.json",
        "--url", plan.target_origin,
        "--phases", ",".join(PHASES),
        "--checks", ",".join(CHECKS),
        "--mode", "positive",
        "--workers", "1",
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
        "--generation-allow-x00", "false",
        "--generation-with-security-parameters", "false",
        "--report", "ndjson,json,wfc",
        "--report-dir", "/output",
        "--report-ndjson-path", REPORT_PATH,
        "--report-json-path", JSON_REPORT_PATH,
        "--report-wfc-path", WFC_REPORT_PATH,
        "--output-sanitize", "true",
        "--output-truncate", "true",
        "--warnings", "off",
        "--no-color",
    ]


def _schema_descriptor(plan):
    return {
        "artifact_sha256": plan.schema_sha256,
        "schema_id": plan.schema_id,
        "format": FORMAT,
        "paths": plan.schema_paths,
        "operations": plan.schema_operations,
        "evidence": plan.schema_evidence,
        "dependency_candidates": plan.schema_dependency_candidates,
        "usable_stateful_dependencies": plan.schema_usable_stateful_dependencies,
        "projected": plan.schema_projected,
        "projection_source_artifact_sha256": plan.schema_projection_source_sha256,
        "projection_source_schema_id": plan.schema_projection_source_id,
    }


def plan_document(plan, guard_proxy=DEFAULT_GUARD_PROXY):
    config = wfc_config_bytes(plan)
    identities = [
        {"identity_ref": name, "header_names": list(headers)}
        for name, headers in plan.identity_header_names
    ]
    return {
        "contract": CONTRACT,
        "run_id": plan.run_id,
        "target_origin": plan.target_origin,
        "target_revision": plan.target_revision,
        "scope_ref": plan.scope_ref,
        "mutation_scope_ref": plan.mutation_scope_ref,
        "reset_ref": plan.reset_ref,
        "network_container": plan.network_container,
        "recording_proxy": _loopback_proxy(plan.recording_proxy, "recording proxy"),
        "scope_guard_proxy": _loopback_proxy(guard_proxy, "scope guard proxy"),
        "scope_guard": {
            "allowed_methods": list(plan.allowed_methods),
            "mutation_methods": list(plan.mutation_methods),
            "exact_origin": True,
            "https_connect": False,
        },
        "schema": _schema_descriptor(plan),
        "identities": {
            "source_sha256": plan.identity_config_sha256,
            "ephemeral_wfc_sha256": plan.wfc_auth_sha256,
            "refs": list(plan.identity_refs),
            "descriptors": identities,
            "credentials_archived": False,
            "login_endpoint_auth": False,
        },
        "budgets": {
            "max_requests": plan.max_requests,
            "max_seconds": plan.max_seconds,
            "max_concurrency": 1,
            "rate_limit_per_second": plan.rate_limit_per_second,
            "request_margin": plan.request_margin,
            "expected_request_upper_bound": plan.expected_request_upper_bound,
            "max_examples_per_operation": plan.max_examples_per_operation,
            "max_failures": plan.max_failures,
            "request_timeout": plan.request_timeout,
            "max_response_bytes": plan.max_response_bytes,
            "max_steps": plan.max_steps,
        },
        "schemathesis": {
            "version": SCHEMATHESIS_VERSION,
            "image": SCHEMATHESIS_IMAGE,
            "phases": list(PHASES),
            "checks": list(CHECKS),
            "seed": plan.seed,
            "wfc_config_sha256": hashlib.sha256(config).hexdigest(),
            "argv": schemathesis_argv(plan, guard_proxy),
        },
        "disabled_integrations": sorted([
            "api_lab_database",
            "automatic_finding_promotion",
            "external_egress",
            "https_targets",
            "login_endpoint_auth",
            "mcp_registration",
            "model_calls",
            "planner",
            "remote_schema_fetch",
            "vvah",
        ]),
    }


def plan_sha256(plan, guard_proxy=DEFAULT_GUARD_PROXY):
    return _hash(_canonical(plan_document(plan, guard_proxy)))


def _assert_bindings(plan, document, artifact_sha256, identity_material):
    metadata = schema_metadata(document, artifact_sha256)
    expected = {
        "artifact_sha256": plan.schema_sha256,
        "schema_id": plan.schema_id,
        "server_origin": plan.target_origin,
        "paths": plan.schema_paths,
        "operations": plan.schema_operations,
        "evidence": plan.schema_evidence,
        "dependency_candidates": plan.schema_dependency_candidates,
        "usable_stateful_dependencies": plan.schema_usable_stateful_dependencies,
        "projected": plan.schema_projected,
        "projection_source_artifact_sha256": plan.schema_projection_source_sha256,
        "projection_source_schema_id": plan.schema_projection_source_id,
        "allowed_methods": plan.allowed_methods,
        "mutation_methods": plan.mutation_methods,
    }
    if any(metadata[key] != value for key, value in expected.items()):
        raise ValueError("executable schema no longer matches the WFC plan")
    if (
        not isinstance(identity_material, WfcIdentityMaterial)
        or identity_material.source_sha256 != plan.identity_config_sha256
        or identity_material.wfc_sha256 != plan.wfc_auth_sha256
        or identity_material.identity_refs != plan.identity_refs
        or identity_material.header_names != plan.identity_header_names
    ):
        raise ValueError("identity configuration no longer matches the WFC plan")


def schemathesis_docker_argv(
    plan,
    *,
    container_name,
    schema_path,
    auth_path,
    config_path,
    output_dir,
    identity_material,
    image_ref=SCHEMATHESIS_IMAGE,
    guard_proxy=DEFAULT_GUARD_PROXY,
):
    if not isinstance(container_name, str) or not CONTAINER_NAME.fullmatch(container_name):
        raise ValueError("Schemathesis WFC container name is invalid")
    _bounded(image_ref, "Schemathesis image reference", 512)
    selected_schema = Path(schema_path).resolve()
    selected_auth = Path(auth_path).resolve()
    selected_config = Path(config_path).resolve()
    selected_output = Path(output_dir).resolve()
    if not selected_schema.is_file() or not selected_auth.is_file() or not selected_config.is_file():
        raise ValueError("staged WFC input file does not exist")
    if not selected_output.is_dir():
        raise ValueError("staged WFC report directory does not exist")
    schema_raw = selected_schema.read_bytes()
    document = loads_executable_traffic_schema(schema_raw, plan.schema_sha256)
    _assert_bindings(plan, document, plan.schema_sha256, identity_material)
    if selected_auth.read_bytes() != identity_material.wfc_bytes:
        raise ValueError("staged WFC auth file differs from validated identity material")
    if selected_config.read_bytes() != wfc_config_bytes(plan):
        raise ValueError("staged Schemathesis config differs from the WFC plan")
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
        "--volume", str(selected_auth) + ":/input/auth.json:ro",
        "--volume", str(selected_config) + ":/input/schemathesis.toml:ro",
        "--volume", str(selected_output) + ":/output:rw",
        image_ref,
    ]
    argv.extend(schemathesis_argv(plan, guard_proxy))
    return argv


def _validate_guard_coverage(plan, coverage):
    if not isinstance(coverage, dict):
        raise ValueError("Schemathesis WFC evidence requires scope-guard coverage")
    for key in ("allowed", "rejected", "upstream_errors", "transactions"):
        value = coverage.get(key)
        if not isinstance(value, int) or isinstance(value, bool) or value < 0:
            raise ValueError("scope-guard coverage counter is invalid: " + key)
    if coverage["transactions"] <= 0 or coverage["transactions"] != coverage["allowed"] + coverage["rejected"]:
        raise ValueError("scope-guard transaction count is invalid")
    if coverage.get("allowed_methods") != list(plan.allowed_methods):
        raise ValueError("scope-guard allowed methods differ from the WFC plan")
    if coverage.get("mutation_methods") != list(plan.mutation_methods):
        raise ValueError("scope-guard mutation methods differ from the WFC plan")
    reasons = coverage.get("rejected_reasons")
    if not isinstance(reasons, dict) or any(
        not isinstance(reason, str)
        or reason not in EXPECTED_GUARD_REJECTIONS
        or not isinstance(count, int)
        or isinstance(count, bool)
        or count <= 0
        for reason, count in reasons.items()
    ):
        raise ValueError("scope-guard rejection reasons are invalid")
    if sum(reasons.values()) != coverage["rejected"] or coverage["upstream_errors"] != 0:
        raise ValueError("scope-guard rejection or upstream counters are inconsistent")
    budget_rejections = reasons.get("transaction_budget", 0)
    if budget_rejections:
        if coverage["transactions"] <= plan.max_requests:
            raise ValueError("transaction-budget rejection happened within the configured budget")
    elif coverage["transactions"] > plan.max_requests:
        raise ValueError("scope-guard transactions exceed max_requests without a rejection")
    return budget_rejections


def finalize_evidence(
    raw_ndjson,
    plan,
    *,
    started_at,
    finished_at,
    tool_sha256,
    guard_coverage,
    exit_reason,
    truncated,
    guard_proxy=DEFAULT_GUARD_PROXY,
):
    mapped_reason = schemathesis_terminal_exit_reason(raw_ndjson)
    if mapped_reason != exit_reason:
        raise ValueError("Schemathesis terminal reason differs from WFC evidence metadata")
    if not isinstance(truncated, bool) or truncated != (exit_reason != "completed"):
        raise ValueError("Schemathesis WFC truncated state is inconsistent with its terminal reason")
    traffic_ids = schemathesis_proxy_traffic_ids(raw_ndjson)
    if len(traffic_ids) > plan.max_requests:
        raise ValueError("Schemathesis WFC proxy-confirmed responses exceed max_requests")
    budget_rejections = _validate_guard_coverage(plan, guard_coverage)
    if budget_rejections and (exit_reason != "budget_exhausted" or truncated is not True):
        raise ValueError("transaction-budget evidence must be marked budget_exhausted and truncated")
    if guard_coverage["allowed"] < len(traffic_ids):
        raise ValueError("scope-guard coverage cannot account for proxy-confirmed WFC responses")
    coverage = {
        "schema": _schema_descriptor(plan),
        "scope_ref": plan.scope_ref,
        "mutation_scope_ref": plan.mutation_scope_ref,
        "reset_ref": plan.reset_ref,
        "execution": {
            "phases": list(PHASES),
            "checks": list(CHECKS),
            "anonymous": False,
            "deterministic_generation": True,
            "stateful": True,
            "wfc": True,
            "positive_generation_only": True,
        },
        "identities": {
            "source_sha256": plan.identity_config_sha256,
            "ephemeral_wfc_sha256": plan.wfc_auth_sha256,
            "refs": list(plan.identity_refs),
            "credentials_archived": False,
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
        "The source schema is inferred from selected captured traffic and is not a declared API contract.",
        "WFC fixed-header credentials are ephemeral and are never archived in the evidence bundle.",
        "Object-level authorization failures remain hypotheses until API Lab controls and independent replay succeed.",
        "The operator must reset or discard the explicitly authorized target after this state-changing run.",
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
            "max_concurrency": 1,
        },
        config_sha256=plan_sha256(plan, guard_proxy),
        identity_refs=list(plan.identity_refs),
        coverage=coverage,
        truncated=truncated,
        limitations=limitations,
    )
    bundle = import_schemathesis_ndjson(raw_ndjson, manifest, proxy_confirmed_only=True)
    observed_ids = sorted(
        node["data"]["traffic_id"]
        for node in bundle["nodes"]
        if node["kind"] == "response_observation" and "traffic_id" in node["data"]
    )
    if observed_ids != sorted(traffic_ids):
        raise ValueError("Schemathesis WFC evidence lost recording-proxy lineage")
    request_identities = {
        node["data"].get("identity_ref")
        for node in bundle["nodes"]
        if node["kind"] == "request_observation"
    }
    if not request_identities <= set(plan.identity_refs) | {"anonymous"}:
        raise ValueError("Schemathesis WFC evidence contains an unbound identity")
    if bundle["summary"]["claims"]["findings"] != 0:
        raise ValueError("Schemathesis WFC adapter attempted to promote a finding")
    return bundle
