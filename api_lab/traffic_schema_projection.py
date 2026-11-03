"""Offline, authority-preserving projection of a Traffic-to-spec artifact.

The caller supplies the path prefixes that are in scope.  This module does not
guess whether a route is an API, inspect proxy-only traffic, or perform network,
database, MCP, planner, or model operations.
"""

from __future__ import annotations

import copy
import hashlib
import json
import re
from urllib.parse import unquote, urlsplit

from api_lab.engine import digest, origin
from api_lab.traffic_schema import FORMAT, METHODS


CONTRACT = "uroboros-traffic-schema-projection-v1"
DEFAULT_MAX_SCHEMA_BYTES = 64 * 1024 * 1024
SHA256 = re.compile(r"[0-9a-fA-F]{64}\Z")
TOP_LEVEL_FIELDS = {"openapi", "info", "servers", "paths", "x-uroboros-traffic"}
TRAFFIC_FIELDS = {
    "format",
    "inference",
    "policy_inferred",
    "coverage",
    "source",
    "evidence",
    "dependency_candidates",
}
PROJECTION_FIELDS = {
    "contract",
    "selection",
    "include_prefixes",
    "source_artifact_sha256",
    "source_schema_id",
    "source_counts",
    "selected_counts",
    "excluded_counts",
    "authority_unchanged",
}
COUNT_FIELDS = {"paths", "operations", "evidence", "dependency_candidates"}
EVIDENCE_FIELDS = {
    "exchange_id",
    "operation_id",
    "timestamp",
    "status",
    "request_body_sha256",
    "response_body_sha256",
    "request_head_sha256",
    "response_head_sha256",
    "identity_ref",
    "path_basis",
    "json_observed",
}
DEPENDENCY_FIELDS = {
    "producer_exchange_id",
    "producer_operation_id",
    "response_pointer",
    "consumer_exchange_id",
    "consumer_operation_id",
    "location",
    "parameter_pointer",
    "basis",
    "verified",
}


def _duplicate_rejecting_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("Traffic-to-spec artifact contains a duplicate JSON key")
        result[key] = value
    return result


def _reject_constant(_):
    raise ValueError("Traffic-to-spec artifact contains a non-finite JSON number")


def _canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("ascii") + b"\n"


def _sha256(value, label):
    if not isinstance(value, str) or not SHA256.fullmatch(value):
        raise ValueError(label + " is invalid")
    return value.lower()


def _reference(value, label):
    if not isinstance(value, str) or not value or len(value) > 512:
        raise ValueError(label + " is invalid")
    return value


def _unique_references(value, label):
    if not isinstance(value, list) or not value:
        raise ValueError(label + " must be a nonempty list")
    result = [_reference(item, label) for item in value]
    if len(set(result)) != len(result):
        raise ValueError(label + " contains duplicates")
    return result


def _validate_embedded_evidence(value, allowed, label):
    if isinstance(value, dict):
        for key, item in value.items():
            if key == "x-traffic-evidence":
                refs = _unique_references(item, label + " evidence")
                if not set(refs) <= allowed:
                    raise ValueError(label + " contains evidence outside its operation")
            else:
                _validate_embedded_evidence(item, allowed, label)
    elif isinstance(value, list):
        for item in value:
            _validate_embedded_evidence(item, allowed, label)


def _validate_projection(projection, paths, operations, evidence, dependencies):
    if not isinstance(projection, dict) or set(projection) != PROJECTION_FIELDS:
        raise ValueError("Traffic-to-spec projection metadata is invalid")
    if (
        projection["contract"] != CONTRACT
        or projection["selection"] != "operator_explicit_path_prefix_allowlist"
        or projection["authority_unchanged"] is not True
    ):
        raise ValueError("Traffic-to-spec projection contract is invalid")
    prefixes = normalize_prefixes(projection["include_prefixes"])
    if prefixes != projection["include_prefixes"]:
        raise ValueError("Traffic-to-spec projection prefixes are not canonical")
    if any(not _matches(path, prefixes) for path in paths):
        raise ValueError("Traffic-to-spec projection contains a path outside its allowlist")
    if _sha256(projection["source_artifact_sha256"], "projection source artifact hash") != projection["source_artifact_sha256"]:
        raise ValueError("Traffic-to-spec projection source artifact hash is not canonical")
    if _sha256(projection["source_schema_id"], "projection source schema ID") != projection["source_schema_id"]:
        raise ValueError("Traffic-to-spec projection source schema ID is not canonical")

    counts = {}
    for name in ("source_counts", "selected_counts", "excluded_counts"):
        value = projection[name]
        if not isinstance(value, dict) or set(value) != COUNT_FIELDS:
            raise ValueError("Traffic-to-spec projection counts are invalid")
        if any(not isinstance(item, int) or isinstance(item, bool) or item < 0 for item in value.values()):
            raise ValueError("Traffic-to-spec projection counts are invalid")
        counts[name] = value
    actual = {
        "paths": len(paths),
        "operations": len(operations),
        "evidence": len(evidence),
        "dependency_candidates": len(dependencies),
    }
    if counts["selected_counts"] != actual:
        raise ValueError("Traffic-to-spec projection selected counts disagree with the artifact")
    if any(
        counts["source_counts"][key]
        != counts["selected_counts"][key] + counts["excluded_counts"][key]
        for key in COUNT_FIELDS
    ):
        raise ValueError("Traffic-to-spec projection count arithmetic is inconsistent")


def _validate_document(document, *, allow_projection=False):
    if not isinstance(document, dict) or set(document) != TOP_LEVEL_FIELDS:
        raise ValueError("Traffic-to-spec artifact has invalid top-level fields")
    if document["openapi"] != "3.0.3" or not isinstance(document["info"], dict):
        raise ValueError("Traffic-to-spec artifact is not the supported OpenAPI format")
    servers = document["servers"]
    if (
        not isinstance(servers, list)
        or len(servers) != 1
        or not isinstance(servers[0], dict)
        or set(servers[0]) != {"url"}
        or not isinstance(servers[0]["url"], str)
    ):
        raise ValueError("Traffic-to-spec artifact must have one exact server URL")
    server_url = servers[0]["url"]
    parsed_server = urlsplit(server_url)
    if parsed_server.query or parsed_server.fragment or server_url.rstrip("/") != origin(server_url):
        raise ValueError("Traffic-to-spec artifact server URL must be one exact origin")

    traffic = document["x-uroboros-traffic"]
    projected = isinstance(traffic, dict) and set(traffic) == TRAFFIC_FIELDS | {"projection"}
    if not isinstance(traffic, dict) or (
        set(traffic) != TRAFFIC_FIELDS and not (allow_projection and projected)
    ):
        raise ValueError("Traffic-to-spec manifest fields are invalid or already projected")
    expected_coverage = (
        "explicit_subset_of_selected_captured_exchanges"
        if projected
        else "selected_captured_exchanges_only"
    )
    if (
        traffic["format"] != FORMAT
        or traffic["inference"] != "observed_shape_not_declared_contract"
        or traffic["policy_inferred"] is not False
        or traffic["coverage"] != expected_coverage
        or not isinstance(traffic["source"], dict)
    ):
        raise ValueError("Traffic-to-spec manifest contract is invalid")

    paths = document["paths"]
    if not isinstance(paths, dict) or not paths:
        raise ValueError("Traffic-to-spec artifact has no paths")
    operations = {}
    operation_evidence = {}
    for path, path_item in paths.items():
        if (
            not isinstance(path, str)
            or not path.startswith("/")
            or "?" in path
            or "#" in path
            or not isinstance(path_item, dict)
            or not path_item
        ):
            raise ValueError("Traffic-to-spec artifact contains an invalid path")
        for method, operation in path_item.items():
            if method not in METHODS or not isinstance(operation, dict):
                raise ValueError("Traffic-to-spec artifact contains an invalid operation")
            operation_id = _reference(operation.get("operationId"), "operation ID")
            if operation_id in operations:
                raise ValueError("Traffic-to-spec artifact reuses an operation ID")
            refs = _unique_references(operation.get("x-traffic-evidence"), "operation evidence")
            operations[operation_id] = (path, method)
            operation_evidence[operation_id] = set(refs)
            _validate_embedded_evidence(operation, set(refs), operation_id)

    evidence = traffic["evidence"]
    if not isinstance(evidence, list) or not evidence:
        raise ValueError("Traffic-to-spec manifest has no evidence")
    evidence_operations = {}
    grouped = {operation_id: set() for operation_id in operations}
    for item in evidence:
        if not isinstance(item, dict) or set(item) != EVIDENCE_FIELDS:
            raise ValueError("Traffic-to-spec evidence fields are invalid")
        exchange_id = _reference(item["exchange_id"], "exchange ID")
        operation_id = _reference(item["operation_id"], "evidence operation ID")
        if exchange_id in evidence_operations:
            raise ValueError("Traffic-to-spec evidence reuses an exchange ID")
        if operation_id not in operations:
            raise ValueError("Traffic-to-spec evidence names an unknown operation")
        if (
            not isinstance(item["status"], int)
            or isinstance(item["status"], bool)
            or not 100 <= item["status"] <= 599
            or not isinstance(item["json_observed"], dict)
            or set(item["json_observed"]) != {"request", "response"}
            or any(type(value) is not bool for value in item["json_observed"].values())
        ):
            raise ValueError("Traffic-to-spec evidence values are invalid")
        evidence_operations[exchange_id] = operation_id
        grouped[operation_id].add(exchange_id)
    if grouped != operation_evidence:
        raise ValueError("Traffic-to-spec operation/evidence lineage is inconsistent")

    dependencies = traffic["dependency_candidates"]
    if not isinstance(dependencies, list):
        raise ValueError("Traffic-to-spec dependency candidates must be a list")
    seen_dependencies = set()
    for item in dependencies:
        if not isinstance(item, dict) or set(item) != DEPENDENCY_FIELDS:
            raise ValueError("Traffic-to-spec dependency candidate fields are invalid")
        producer_exchange = _reference(item["producer_exchange_id"], "producer exchange ID")
        consumer_exchange = _reference(item["consumer_exchange_id"], "consumer exchange ID")
        producer_operation = _reference(item["producer_operation_id"], "producer operation ID")
        consumer_operation = _reference(item["consumer_operation_id"], "consumer operation ID")
        if (
            evidence_operations.get(producer_exchange) != producer_operation
            or evidence_operations.get(consumer_exchange) != consumer_operation
            or item["basis"] != "observed_value_match"
            or item["verified"] is not False
        ):
            raise ValueError("Traffic-to-spec dependency lineage is inconsistent")
        identity = digest(item)
        if identity in seen_dependencies:
            raise ValueError("Traffic-to-spec dependency candidates contain duplicates")
        seen_dependencies.add(identity)
    if projected:
        _validate_projection(traffic["projection"], paths, operations, evidence, dependencies)
    return operations, evidence_operations


def loads_traffic_schema(data, expected_sha256, max_bytes=DEFAULT_MAX_SCHEMA_BYTES):
    """Restore one canonical, hash-bound, unprojected Traffic-to-spec document."""
    return _loads_traffic_schema(data, expected_sha256, max_bytes=max_bytes, allow_projection=False)


def loads_executable_traffic_schema(data, expected_sha256, max_bytes=DEFAULT_MAX_SCHEMA_BYTES):
    """Restore a canonical source or explicitly projected executable schema."""
    return _loads_traffic_schema(data, expected_sha256, max_bytes=max_bytes, allow_projection=True)


def _loads_traffic_schema(data, expected_sha256, *, max_bytes, allow_projection):
    if not isinstance(max_bytes, int) or isinstance(max_bytes, bool) or max_bytes <= 0:
        raise ValueError("schema byte limit must be a positive integer")
    if not isinstance(data, (bytes, bytearray)):
        raise ValueError("Traffic-to-spec artifact must be bytes")
    raw = bytes(data)
    if not raw:
        raise ValueError("Traffic-to-spec artifact is empty")
    if len(raw) > max_bytes:
        raise ValueError("Traffic-to-spec artifact exceeds the byte limit")
    expected = _sha256(expected_sha256, "Traffic-to-spec artifact hash")
    if hashlib.sha256(raw).hexdigest() != expected:
        raise ValueError("Traffic-to-spec artifact digest mismatch")
    try:
        document = json.loads(
            raw.decode("ascii"),
            object_pairs_hook=_duplicate_rejecting_object,
            parse_constant=_reject_constant,
        )
    except UnicodeDecodeError:
        raise ValueError("Traffic-to-spec artifact is not canonical ASCII JSON") from None
    except json.JSONDecodeError:
        raise ValueError("Traffic-to-spec artifact is invalid JSON") from None
    if raw != _canonical(document):
        raise ValueError("Traffic-to-spec artifact is not canonical")
    _validate_document(document, allow_projection=allow_projection)
    return document


def normalize_prefixes(prefixes):
    if not isinstance(prefixes, (list, tuple)) or not prefixes:
        raise ValueError("at least one include prefix is required")
    result = []
    for value in prefixes:
        if not isinstance(value, str) or not value.startswith("/") or value.startswith("//"):
            raise ValueError("include prefix must be an absolute path")
        if (
            value == "/"
            or "?" in value
            or "#" in value
            or "\\" in value
            or "{" in value
            or "}" in value
            or any(character.isspace() for character in value)
            or "//" in value
        ):
            raise ValueError("include prefix is not a safe non-root literal path")
        normalized = value.rstrip("/")
        segments = [unquote(segment) for segment in normalized.split("/")[1:]]
        if not segments or any(segment in {"", ".", ".."} for segment in segments):
            raise ValueError("include prefix contains an invalid path segment")
        if normalized in result:
            raise ValueError("include prefixes contain duplicates")
        result.append(normalized)
    return result


def _matches(path, prefixes):
    return any(path == prefix or path.startswith(prefix + "/") for prefix in prefixes)


def project(document, prefixes, source_artifact_sha256):
    """Select explicit path-prefix subsets without changing evidence authority."""
    operations, _ = _validate_document(document)
    normalized = normalize_prefixes(prefixes)
    source_hash = _sha256(source_artifact_sha256, "Traffic-to-spec artifact hash")
    selected_paths = {
        path: copy.deepcopy(item)
        for path, item in document["paths"].items()
        if _matches(path, normalized)
    }
    if not selected_paths:
        raise ValueError("include prefixes select no Traffic-to-spec paths")
    selected_operation_ids = {
        operation["operationId"]
        for item in selected_paths.values()
        for operation in item.values()
    }
    traffic = copy.deepcopy(document["x-uroboros-traffic"])
    selected_evidence = [
        item for item in traffic["evidence"] if item["operation_id"] in selected_operation_ids
    ]
    selected_dependencies = [
        item
        for item in traffic["dependency_candidates"]
        if item["producer_operation_id"] in selected_operation_ids
        and item["consumer_operation_id"] in selected_operation_ids
    ]
    source_counts = {
        "paths": len(document["paths"]),
        "operations": len(operations),
        "evidence": len(traffic["evidence"]),
        "dependency_candidates": len(traffic["dependency_candidates"]),
    }
    selected_counts = {
        "paths": len(selected_paths),
        "operations": len(selected_operation_ids),
        "evidence": len(selected_evidence),
        "dependency_candidates": len(selected_dependencies),
    }
    traffic["coverage"] = "explicit_subset_of_selected_captured_exchanges"
    traffic["evidence"] = selected_evidence
    traffic["dependency_candidates"] = selected_dependencies
    traffic["projection"] = {
        "contract": CONTRACT,
        "selection": "operator_explicit_path_prefix_allowlist",
        "include_prefixes": normalized,
        "source_artifact_sha256": source_hash,
        "source_schema_id": digest(document),
        "source_counts": source_counts,
        "selected_counts": selected_counts,
        "excluded_counts": {
            key: source_counts[key] - selected_counts[key] for key in source_counts
        },
        "authority_unchanged": True,
    }
    result = copy.deepcopy(document)
    result["paths"] = selected_paths
    result["x-uroboros-traffic"] = traffic
    return result


def project_bytes(data, expected_sha256, prefixes, max_bytes=DEFAULT_MAX_SCHEMA_BYTES):
    document = loads_traffic_schema(data, expected_sha256, max_bytes=max_bytes)
    return project(document, prefixes, expected_sha256)
