"""Offline, content-addressed runtime evidence import contracts.

This module deliberately has no database, MCP, planner, or process execution
integration.  It normalizes bounded, already-produced tool artifacts into an
immutable graph batch.  External tool claims remain observations, derived
candidates, or hypotheses; no adapter can create a confirmed finding.
"""

from __future__ import annotations

import base64
import binascii
import copy
import hashlib
import json
import re
from collections import Counter
from datetime import datetime
from urllib.parse import parse_qsl, urlencode, urljoin, urlsplit, urlunsplit


FORMAT = "uroboros-runtime-evidence-v1"
ADAPTER_VERSION = "1"
REDACTED = "[REDACTED]"
DEFAULT_MAX_BYTES = 16 * 1024 * 1024
DEFAULT_MAX_RECORDS = 10_000
DEFAULT_MAX_LINE_BYTES = 2 * 1024 * 1024

METHODS = {"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "TRACE", "CONNECT"}
EXIT_REASONS = {"completed", "budget_exhausted", "timeout", "cancelled", "interrupted", "error"}
CLASSIFICATIONS = {"observation", "derived_candidate", "hypothesis"}
NODE_KINDS = {
    "tool_run",
    "endpoint",
    "experiment",
    "request_observation",
    "response_observation",
    "coverage_observation",
    "oast_payload",
    "oast_callback",
    "hypothesis",
}
EDGE_KINDS = {
    "derived_from",
    "observed_at",
    "response_to",
    "transitions_to",
    "probes",
    "supports",
    "callback_for",
}
OAST_PROTOCOLS = {"dns", "http", "https", "smtp", "smtps", "ldap"}
SECRET_NAMES = {
    "authorization",
    "proxy_authorization",
    "cookie",
    "set_cookie",
    "password",
    "passwd",
    "token",
    "access_token",
    "refresh_token",
    "api_key",
    "apikey",
    "client_secret",
    "session",
    "sessionid",
    "csrf_token",
    "auth",
}
TRAFFIC_ID_HEADER = "x-uroboros-traffic-id"
SCOPE_GUARD_HEADER = "x-uroboros-scope-guard"
SCOPE_GUARD_REJECTIONS = {
    "invalid_authority": 400,
    "invalid_request": 400,
    "method_not_read_only": 405,
    "outside_authority": 403,
    "outside_origin": 403,
    "transaction_budget": 429,
}
SCHEMATHESIS_STOP_REASONS = {
    "completed": "completed",
    "interrupted": "interrupted",
    "failure_limit": "budget_exhausted",
    "max_time": "budget_exhausted",
    "server_unavailable": "error",
}
HASH = re.compile(r"(?:sha256:)?([0-9a-fA-F]{64})\Z")
REFERENCE = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}\Z")
HTTP_METHOD_TOKEN = re.compile(r"[!#$%&'*+\-.^_`|~0-9A-Za-z]{1,32}\Z")


def _canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True)


def _digest(value):
    return hashlib.sha256(_canonical(value).encode("ascii")).hexdigest()


def _bytes_digest(value):
    return hashlib.sha256(value).hexdigest()


def _hash(value, label):
    if not isinstance(value, str):
        raise ValueError(label + " must be a SHA-256 string")
    match = HASH.fullmatch(value)
    if not match:
        raise ValueError(label + " must be a SHA-256 string")
    return match.group(1).lower()


def _string(value, label, maximum=2048, allow_empty=False):
    if not isinstance(value, str) or (not allow_empty and not value) or len(value) > maximum:
        raise ValueError(label + " must be a bounded string")
    if any(ord(character) < 32 and character not in "\t\r\n" for character in value):
        raise ValueError(label + " contains control characters")
    return value


def _reference(value, label):
    if not isinstance(value, str) or not REFERENCE.fullmatch(value):
        raise ValueError(label + " must be an opaque reference, not a credential")
    return value


def _positive_integer(value, label):
    if not isinstance(value, int) or isinstance(value, bool) or value <= 0:
        raise ValueError(label + " must be a positive integer")
    return value


def _nonnegative_integer(value, label):
    if not isinstance(value, int) or isinstance(value, bool) or value < 0:
        raise ValueError(label + " must be a nonnegative integer")
    return value


def _timestamp(value, label):
    _string(value, label, 64)
    try:
        parsed = datetime.fromisoformat(value[:-1] + "+00:00" if value.endswith("Z") else value)
    except ValueError:
        raise ValueError(label + " must be an RFC 3339 timestamp") from None
    if parsed.tzinfo is None or parsed.utcoffset() is None:
        raise ValueError(label + " must include a timezone")
    return parsed


def _origin(value, label="URL"):
    _string(value, label)
    if any(ord(character) <= 32 or ord(character) == 127 for character in value):
        raise ValueError(label + " contains whitespace or control characters")
    parsed = urlsplit(value)
    if parsed.scheme.lower() not in ("http", "https") or not parsed.hostname or parsed.username or parsed.password:
        raise ValueError(label + " must be an HTTP(S) URL without embedded credentials")
    try:
        port = parsed.port
    except ValueError:
        raise ValueError(label + " contains an invalid port") from None
    host = parsed.hostname.lower()
    if ":" in host and not host.startswith("["):
        host = "[" + host + "]"
    netloc = host + ((":" + str(port)) if port is not None else "")
    return parsed.scheme.lower() + "://" + netloc


def _manifest_origin(value, label):
    selected = _origin(value, label)
    if value.rstrip("/") != selected:
        raise ValueError(label + " must contain only an origin")
    return selected


def _source_url(value):
    _string(value, "tool source", 2048)
    parsed = urlsplit(value)
    if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ValueError("tool source must be a credential-free HTTPS URL")
    return value


def _egress_endpoint(value):
    _string(value, "external egress endpoint", 2048)
    parsed = urlsplit(value)
    if parsed.scheme.lower() not in {"http", "https", "dns", "smtp", "smtps", "ldap"}:
        raise ValueError("external egress endpoint has an unsupported scheme")
    if not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ValueError("external egress endpoint must be credential-free")
    return value


def _json_value(value, label="JSON value", depth=0):
    if depth > 64:
        raise ValueError(label + " exceeds the nesting limit")
    if value is None or isinstance(value, (str, bool, int)):
        return
    if isinstance(value, float):
        if value != value or value in (float("inf"), float("-inf")):
            raise ValueError(label + " contains a non-finite number")
        return
    if isinstance(value, list):
        for item in value:
            _json_value(item, label, depth + 1)
        return
    if isinstance(value, dict):
        for key, item in value.items():
            if not isinstance(key, str):
                raise ValueError(label + " contains a non-string object key")
            _json_value(item, label, depth + 1)
        return
    raise ValueError(label + " contains a non-JSON value")


def _sensitive(name):
    return str(name).lower().replace("-", "_") in SECRET_NAMES


def _assert_redacted(value):
    if isinstance(value, dict):
        for key, item in value.items():
            if _sensitive(key) and item != REDACTED:
                raise ValueError("sensitive graph fields must be redacted: " + key)
            _assert_redacted(item)
    elif isinstance(value, list):
        for item in value:
            _assert_redacted(item)


def _safe_url(value, scope, label="URL"):
    selected = _origin(value, label)
    if selected not in scope:
        raise ValueError(label + " is outside the recorded scope")
    parsed = urlsplit(value)
    query = []
    for name, _ in parse_qsl(parsed.query, keep_blank_values=True):
        # Exact values remain only in the separately retained raw artifact.
        # Redacting every value also covers custom credential parameter names.
        query.append((name, REDACTED))
    return urlunsplit((parsed.scheme.lower(), parsed.netloc.lower(), parsed.path or "/", urlencode(query, doseq=True), ""))


def _endpoint_data(value, method, scope, *, parameters=()):
    safe = _safe_url(value, scope)
    parsed = urlsplit(safe)
    query_names = sorted({name for name, _ in parse_qsl(parsed.query, keep_blank_values=True)})
    return {
        "origin": _origin(safe),
        "path": parsed.path or "/",
        "method": method,
        "query_parameter_names": query_names,
        "declared_parameter_names": sorted(set(parameters)),
    }


def _method(value):
    if not isinstance(value, str) or value.upper() not in METHODS:
        raise ValueError("request method is missing or unsupported")
    return value.upper()


def _attempted_method(value, label):
    if not isinstance(value, str) or not HTTP_METHOD_TOKEN.fullmatch(value):
        raise ValueError(label + " is not a bounded HTTP method token")
    return value.upper()


def _payload_hash(value):
    if isinstance(value, dict) and set(value) == {"$base64"} and isinstance(value["$base64"], str):
        try:
            return _bytes_digest(base64.b64decode(value["$base64"], validate=True))
        except (ValueError, binascii.Error):
            raise ValueError("payload contains invalid base64") from None
    if isinstance(value, str):
        return _bytes_digest(value.encode("utf-8"))
    _json_value(value, "payload")
    return _digest(value)


def _base64_payload_hash(value, label):
    if not isinstance(value, str):
        raise ValueError(label + " must be base64 text")
    try:
        return _bytes_digest(base64.b64decode(value, validate=True))
    except (ValueError, binascii.Error):
        raise ValueError(label + " contains invalid base64") from None


def _bounded_text(value, label, maximum=512):
    if value is None:
        return None
    return _string(value, label, maximum, allow_empty=True)


def make_manifest(
    *,
    tool_name,
    tool_version,
    tool_sha256,
    tool_source,
    license_id,
    run_id,
    target_origin,
    target_revision,
    scope_origins,
    started_at,
    finished_at,
    exit_reason,
    budgets,
    config_sha256,
    corpus_sha256=None,
    browser_version=None,
    identity_refs=(),
    external_egress=(),
    coverage=None,
    truncated=False,
    limitations=(),
):
    """Build and validate provenance required for one isolated tool run."""
    manifest = {
        "run_id": run_id,
        "tool": {
            "name": tool_name,
            "version": tool_version,
            "sha256": _hash(tool_sha256, "tool digest"),
            "source": tool_source,
            "license": license_id,
        },
        "target": {"origin": target_origin, "revision": target_revision},
        "scope": {"origins": list(scope_origins)},
        "started_at": started_at,
        "finished_at": finished_at,
        "exit_reason": exit_reason,
        "budgets": copy.deepcopy(budgets),
        "identity_refs": list(identity_refs),
        "config_sha256": _hash(config_sha256, "config digest"),
        "corpus_sha256": None if corpus_sha256 is None else _hash(corpus_sha256, "corpus digest"),
        "browser_version": browser_version,
        "external_egress": list(external_egress),
        "coverage": copy.deepcopy(coverage or {}),
        "truncated": truncated,
        "limitations": list(limitations),
    }
    return validate_manifest(manifest)


def validate_manifest(manifest, imported=False):
    if not isinstance(manifest, dict):
        raise ValueError("manifest must be an object")
    required = {
        "run_id", "tool", "target", "scope", "started_at", "finished_at", "exit_reason", "budgets",
        "identity_refs", "config_sha256", "corpus_sha256", "browser_version", "external_egress", "coverage",
        "truncated", "limitations",
    }
    if imported:
        required |= {"adapter", "input_artifact"}
    if set(manifest) != required:
        raise ValueError("manifest fields do not match the runtime evidence contract")
    result = copy.deepcopy(manifest)
    result["run_id"] = _reference(result["run_id"], "run_id")
    tool = result["tool"]
    if not isinstance(tool, dict) or set(tool) != {"name", "version", "sha256", "source", "license"}:
        raise ValueError("tool metadata is incomplete")
    tool["name"] = _string(tool["name"], "tool name", 128)
    tool["version"] = _string(tool["version"], "tool version", 128)
    tool["sha256"] = _hash(tool["sha256"], "tool digest")
    tool["source"] = _source_url(tool["source"])
    tool["license"] = _string(tool["license"], "license identifier", 128)
    target = result["target"]
    if not isinstance(target, dict) or set(target) != {"origin", "revision"}:
        raise ValueError("target metadata is incomplete")
    target["origin"] = _manifest_origin(target["origin"], "target origin")
    target["revision"] = _string(target["revision"], "target revision", 512)
    scope = result["scope"]
    if not isinstance(scope, dict) or set(scope) != {"origins"} or not isinstance(scope["origins"], list) or not scope["origins"]:
        raise ValueError("scope requires at least one origin")
    scope["origins"] = sorted({_manifest_origin(item, "scope origin") for item in scope["origins"]})
    if target["origin"] not in scope["origins"]:
        raise ValueError("target origin must be inside scope")
    started = _timestamp(result["started_at"], "started_at")
    finished = _timestamp(result["finished_at"], "finished_at")
    if finished < started:
        raise ValueError("finished_at precedes started_at")
    if result["exit_reason"] not in EXIT_REASONS:
        raise ValueError("unsupported exit reason")
    budgets = result["budgets"]
    if not isinstance(budgets, dict) or set(budgets) != {"max_requests", "max_seconds", "max_concurrency"}:
        raise ValueError("budgets require max_requests, max_seconds and max_concurrency")
    for name in budgets:
        budgets[name] = _positive_integer(budgets[name], "budget " + name)
    if not isinstance(result["identity_refs"], list):
        raise ValueError("identity_refs must be an array")
    result["identity_refs"] = sorted({_reference(item, "identity reference") for item in result["identity_refs"]})
    result["config_sha256"] = _hash(result["config_sha256"], "config digest")
    if result["corpus_sha256"] is not None:
        result["corpus_sha256"] = _hash(result["corpus_sha256"], "corpus digest")
    if result["browser_version"] is not None:
        result["browser_version"] = _string(result["browser_version"], "browser version", 128)
    if not isinstance(result["external_egress"], list):
        raise ValueError("external_egress must be an array")
    result["external_egress"] = sorted({_egress_endpoint(item) for item in result["external_egress"]})
    if not isinstance(result["coverage"], dict):
        raise ValueError("coverage must be an object")
    _json_value(result["coverage"], "coverage")
    if not isinstance(result["truncated"], bool):
        raise ValueError("truncated must be boolean")
    if not isinstance(result["limitations"], list):
        raise ValueError("limitations must be an array")
    result["limitations"] = sorted({_string(item, "limitation", 1024) for item in result["limitations"]})
    if imported:
        adapter = result["adapter"]
        if not isinstance(adapter, dict) or set(adapter) != {"name", "version"}:
            raise ValueError("adapter metadata is incomplete")
        adapter["name"] = _string(adapter["name"], "adapter name", 128)
        adapter["version"] = _string(adapter["version"], "adapter version", 32)
        artifact = result["input_artifact"]
        if not isinstance(artifact, dict) or set(artifact) != {"media_type", "sha256", "bytes", "records"}:
            raise ValueError("input artifact metadata is incomplete")
        artifact["media_type"] = _string(artifact["media_type"], "input media type", 128)
        artifact["sha256"] = _hash(artifact["sha256"], "input artifact digest")
        artifact["bytes"] = _nonnegative_integer(artifact["bytes"], "input artifact size")
        artifact["records"] = _nonnegative_integer(artifact["records"], "input artifact records")
    _assert_redacted(result)
    return result


def _duplicate_rejecting_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON object key")
        result[key] = value
    return result


def _reject_constant(value):
    raise ValueError("non-finite JSON number: " + value)


def _input_bytes(data, max_bytes):
    if isinstance(data, str):
        data = data.encode("utf-8")
    if not isinstance(data, bytes):
        raise ValueError("tool output must be bytes or text")
    if len(data) > max_bytes:
        raise ValueError("tool output exceeds the byte limit")
    return data


def _jsonl(data, label, *, max_bytes, max_records, max_line_bytes):
    raw = _input_bytes(data, max_bytes)
    records = []
    for line_number, line in enumerate(raw.splitlines(keepends=True), 1):
        content = line.rstrip(b"\r\n")
        if not content.strip():
            continue
        if len(content) > max_line_bytes:
            raise ValueError(label + " line exceeds the byte limit")
        if len(records) >= max_records:
            raise ValueError(label + " exceeds the record limit")
        try:
            text = content.decode("utf-8")
            value = json.loads(text, object_pairs_hook=_duplicate_rejecting_object, parse_constant=_reject_constant)
        except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
            raise ValueError("%s line %d is invalid JSON: %s" % (label, line_number, exc)) from None
        if not isinstance(value, dict):
            raise ValueError("%s line %d must contain a JSON object" % (label, line_number))
        _json_value(value, label + " record")
        records.append((line_number, _bytes_digest(content), value))
    if not records:
        raise ValueError(label + " contains no records")
    return raw, records


def _prepare_manifest(manifest, expected_tool, adapter, raw, records, media_type, limitations=()):
    result = validate_manifest(manifest)
    if result["tool"]["name"].lower() != expected_tool:
        raise ValueError("adapter requires tool " + expected_tool)
    result["adapter"] = {"name": adapter, "version": ADAPTER_VERSION}
    result["input_artifact"] = {
        "media_type": media_type,
        "sha256": _bytes_digest(raw),
        "bytes": len(raw),
        "records": len(records),
    }
    result["limitations"] = sorted(set(result["limitations"]) | set(limitations))
    return validate_manifest(result, imported=True)


class _Graph:
    def __init__(self, manifest):
        self.manifest = manifest
        self.nodes = {}
        self.edges = {}
        self.run = self.node("tool_run", "observation", manifest)

    def node(self, kind, classification, data):
        if kind not in NODE_KINDS or classification not in CLASSIFICATIONS:
            raise ValueError("unsupported evidence node")
        _json_value(data, "node data")
        _assert_redacted(data)
        core = {"kind": kind, "classification": classification, "data": copy.deepcopy(data)}
        identifier = "n:" + _digest(core)
        node = {"id": identifier, **core}
        previous = self.nodes.get(identifier)
        if previous is not None and previous != node:
            raise ValueError("evidence node hash collision")
        self.nodes[identifier] = node
        return identifier

    def edge(self, kind, source, target, data=None):
        if kind not in EDGE_KINDS or source not in self.nodes or target not in self.nodes:
            raise ValueError("unsupported or dangling evidence edge")
        core = {"kind": kind, "source": source, "target": target, "data": copy.deepcopy(data or {})}
        _json_value(core["data"], "edge data")
        _assert_redacted(core["data"])
        identifier = "e:" + _digest(core)
        edge = {"id": identifier, **core}
        previous = self.edges.get(identifier)
        if previous is not None and previous != edge:
            raise ValueError("evidence edge hash collision")
        self.edges[identifier] = edge
        return identifier

    def sourced(self, kind, classification, data):
        identifier = self.node(kind, classification, data)
        self.edge("derived_from", identifier, self.run)
        return identifier

    def finish(self, records, ignored_records=0, details=None):
        nodes = sorted(self.nodes.values(), key=lambda item: item["id"])
        edges = sorted(self.edges.values(), key=lambda item: item["id"])
        summary = _summary(nodes, edges, records, ignored_records, details or {})
        bundle = {"format": FORMAT, "manifest": self.manifest, "nodes": nodes, "edges": edges, "summary": summary}
        validate_bundle(bundle)
        return bundle


def _summary(nodes, edges, records, ignored_records, details):
    classifications = Counter(node["classification"] for node in nodes)
    return {
        "records": records,
        "ignored_records": ignored_records,
        "node_counts": dict(sorted(Counter(node["kind"] for node in nodes).items())),
        "edge_counts": dict(sorted(Counter(edge["kind"] for edge in edges).items())),
        "claims": {
            "observations": classifications["observation"],
            "derived_candidates": classifications["derived_candidate"],
            "hypotheses": classifications["hypothesis"],
            "findings": 0,
        },
        "details": copy.deepcopy(details),
    }


def _record_source(line, raw_sha256):
    return {"line": line, "raw_sha256": raw_sha256}


def _header_names(headers):
    if headers is None:
        return []
    if not isinstance(headers, dict):
        raise ValueError("headers must be an object")
    return sorted(_string(name, "header name", 256).lower() for name in headers)


def _header_reference(headers, name, label):
    """Return one case-insensitive opaque header value without copying peers."""
    if headers is None:
        return None
    if not isinstance(headers, dict):
        raise ValueError("headers must be an object")
    matches = [value for key, value in headers.items() if isinstance(key, str) and key.lower() == name]
    if len(matches) > 1:
        raise ValueError(label + " is ambiguous")
    if not matches:
        return None
    value = matches[0]
    if isinstance(value, list):
        if len(value) != 1:
            raise ValueError(label + " must contain exactly one value")
        value = value[0]
    return _reference(value, label)


def _scope_guard_rejection(response, label):
    """Validate and identify a response generated locally by the scope guard."""
    if not isinstance(response, dict):
        raise ValueError(label + " response must be an object")
    reason = _header_reference(
        response.get("headers"),
        SCOPE_GUARD_HEADER,
        label + " scope-guard rejection",
    )
    if reason is None:
        return None
    expected_status = SCOPE_GUARD_REJECTIONS.get(reason)
    if expected_status is None:
        raise ValueError(label + " scope-guard rejection has an unsupported reason")
    if response.get("status_code") != expected_status:
        raise ValueError(label + " scope-guard rejection has an inconsistent status")
    traffic_id = _header_reference(
        response.get("headers"),
        TRAFFIC_ID_HEADER,
        label + " recording-proxy traffic id",
    )
    if traffic_id is not None:
        raise ValueError(label + " response claims both guard rejection and proxy confirmation")
    return reason


def _katana_guard_rejection(response, line):
    return _scope_guard_rejection(response, "Katana on line %d" % line)


def katana_proxy_traffic_ids(
    data,
    *,
    require_all_responses=True,
    max_bytes=DEFAULT_MAX_BYTES,
    max_records=DEFAULT_MAX_RECORDS,
    max_line_bytes=DEFAULT_MAX_LINE_BYTES,
):
    """Extract recording-proxy commit IDs from bounded Katana JSONL.

    The Uroboros proxy emits this header only for an opted-in request and only
    after the exchange was durably recorded.  Shadow execution uses the helper
    as a fail-closed bridge between crawler output and authoritative traffic.
    """
    _, records = _jsonl(
        data,
        "Katana JSONL",
        max_bytes=max_bytes,
        max_records=max_records,
        max_line_bytes=max_line_bytes,
    )
    traffic_ids = []
    responses = 0
    for line, _, record in records:
        response = record.get("response")
        if response is None:
            continue
        if not isinstance(response, dict):
            raise ValueError("Katana response must be an object")
        responses += 1
        if _katana_guard_rejection(response, line) is not None:
            continue
        traffic_id = _header_reference(
            response.get("headers"),
            TRAFFIC_ID_HEADER,
            "Katana recording-proxy traffic id on line %d" % line,
        )
        if traffic_id is None:
            if require_all_responses:
                raise ValueError("Katana response on line %d is not confirmed by the recording proxy" % line)
            continue
        traffic_ids.append(traffic_id)
    if responses == 0:
        raise ValueError("Katana output contains no HTTP responses to confirm")
    if not traffic_ids:
        raise ValueError("Katana output contains no recording-proxy-confirmed HTTP responses")
    if len(set(traffic_ids)) != len(traffic_ids):
        raise ValueError("Katana output reuses a recording-proxy traffic id")
    return tuple(traffic_ids)


def _katana_request(graph, request, scope, source_record, basis, timestamp=None):
    if not isinstance(request, dict):
        raise ValueError("Katana request must be an object")
    method = _method(request.get("method", "GET"))
    endpoint = request.get("endpoint")
    if not isinstance(endpoint, str):
        raise ValueError("Katana request is missing endpoint")
    safe = _safe_url(endpoint, scope, "Katana endpoint")
    source = request.get("source")
    safe_source = _safe_url(source, scope, "Katana discovery source") if source else None
    observation = {
        "method": method,
        "url": safe,
        "identity_ref": "unknown",
        "header_names": _header_names(request.get("headers")),
        "tag": _bounded_text(request.get("tag"), "Katana tag", 256),
        "attribute": _bounded_text(request.get("attribute"), "Katana attribute", 256),
        "discovery_source": safe_source,
        "basis": basis,
        "timestamp": _bounded_text(timestamp, "Katana timestamp", 128),
        "source_record": source_record,
    }
    if "body" in request:
        observation["body_sha256"] = _payload_hash(request["body"])
    if request.get("raw"):
        observation["raw_request_sha256"] = _payload_hash(request["raw"])
    request_id = graph.sourced("request_observation", "observation", observation)
    endpoint_id = graph.node("endpoint", "derived_candidate", _endpoint_data(safe, method, scope))
    graph.edge("observed_at", request_id, endpoint_id, {"authority": "discovery_only"})
    graph.edge("derived_from", endpoint_id, request_id, {"basis": basis})
    return request_id, endpoint_id


def _katana_ignored_discovery(graph, kind, method, endpoint, scope, source_record, timestamp=None):
    """Retain only hashed coverage for a Katana discovery without proxy proof."""
    method = _method(method)
    if not isinstance(endpoint, str):
        raise ValueError("Katana ignored discovery is missing endpoint")
    request_origin = _origin(endpoint, "Katana endpoint")
    reason = "outside_scope" if request_origin not in scope else "not_executed"
    graph.sourced("coverage_observation", "observation", {
        "kind": kind,
        "reason": reason,
        "method": method,
        "attempted_url_sha256": _payload_hash(endpoint),
        "timestamp": _bounded_text(timestamp, "Katana timestamp", 128),
        "source_record": source_record,
    })
    return reason


def _katana_ignored_unconfirmed_request(graph, request, scope, source_record, timestamp=None):
    if not isinstance(request, dict):
        raise ValueError("Katana request must be an object")
    return _katana_ignored_discovery(
        graph,
        "ignored_unconfirmed_request",
        request.get("method", "GET"),
        request.get("endpoint"),
        scope,
        source_record,
        timestamp,
    )


def import_katana_jsonl(
    data,
    manifest,
    *,
    proxy_confirmed_only=False,
    max_bytes=DEFAULT_MAX_BYTES,
    max_records=DEFAULT_MAX_RECORDS,
    max_line_bytes=DEFAULT_MAX_LINE_BYTES,
):
    if not isinstance(proxy_confirmed_only, bool):
        raise ValueError("proxy_confirmed_only must be boolean")
    raw, records = _jsonl(data, "Katana JSONL", max_bytes=max_bytes, max_records=max_records, max_line_bytes=max_line_bytes)
    prepared = _prepare_manifest(
        manifest,
        "katana",
        "katana-jsonl",
        raw,
        records,
        "application/x-ndjson",
        ("Katana discovery is not authoritative HTTP evidence; recording-proxy exchanges must confirm it.",),
    )
    graph = _Graph(prepared)
    scope = set(prepared["scope"]["origins"])
    errors = responses = xhr_count = forms = proxy_confirmed = guard_rejections = ignored_unconfirmed = 0
    ignored_discoveries = 0
    ignored_reasons = Counter()
    ignored_discovery_reasons = Counter()
    for line, raw_hash, record in records:
        provenance = _record_source(line, raw_hash)
        request = record.get("request")
        if request is None:
            if not isinstance(record.get("error"), str):
                raise ValueError("Katana record needs request or error")
            errors += 1
            graph.sourced("coverage_observation", "observation", {
                "kind": "tool_error", "error_sha256": _payload_hash(record["error"]),
                "timestamp": _bounded_text(record.get("timestamp"), "Katana timestamp", 128),
                "source_record": provenance,
            })
            continue
        timestamp = record.get("timestamp")
        response = record.get("response")
        guard_reason = None
        if response is not None:
            guard_reason = _katana_guard_rejection(response, line)
        if guard_reason is not None:
            if not isinstance(request, dict):
                raise ValueError("Katana request must be an object")
            method = _method(request.get("method", "GET"))
            endpoint = request.get("endpoint")
            if not isinstance(endpoint, str):
                raise ValueError("Katana guard rejection is missing endpoint")
            graph.sourced("coverage_observation", "observation", {
                "kind": "scope_guard_rejection",
                "reason": guard_reason,
                "method": method,
                "status": response["status_code"],
                "attempted_url_sha256": _payload_hash(endpoint),
                "timestamp": _bounded_text(timestamp, "Katana timestamp", 128),
                "source_record": provenance,
            })
            guard_rejections += 1
            continue
        traffic_id = None
        if response is not None:
            traffic_id = _header_reference(
                response.get("headers"),
                TRAFFIC_ID_HEADER,
                "Katana recording-proxy traffic id",
            )
        if proxy_confirmed_only and response is None:
            reason = _katana_ignored_unconfirmed_request(graph, request, scope, provenance, timestamp)
            ignored_reasons[reason] += 1
            ignored_unconfirmed += 1
            continue
        if proxy_confirmed_only and traffic_id is None:
            raise ValueError("Katana response on line %d is not confirmed by the recording proxy" % line)
        request_id, _ = _katana_request(graph, request, scope, provenance, "crawl_result", timestamp)
        if response is not None:
            if not isinstance(response, dict):
                raise ValueError("Katana response must be an object")
            status = response.get("status_code")
            if not isinstance(status, int) or isinstance(status, bool) or not 100 <= status <= 599:
                raise ValueError("Katana response needs a valid status_code")
            technologies = response.get("technologies", [])
            if not isinstance(technologies, list) or any(not isinstance(item, str) for item in technologies):
                raise ValueError("Katana technologies must be an array of strings")
            content_length = response.get("content_length")
            if content_length is not None and (
                not isinstance(content_length, int) or isinstance(content_length, bool) or content_length < 0
            ):
                raise ValueError("Katana content_length must be nonnegative")
            response_data = {
                "status": status,
                "header_names": _header_names(response.get("headers")),
                "content_length": content_length,
                "technologies": sorted(technologies)[:128],
                "timestamp": _bounded_text(timestamp, "Katana timestamp", 128),
                "source_record": provenance,
            }
            if traffic_id is not None:
                response_data["traffic_id"] = traffic_id
                proxy_confirmed += 1
            if "body" in response:
                response_data["body_sha256"] = _payload_hash(response["body"])
            if response.get("raw"):
                response_data["raw_response_sha256"] = _payload_hash(response["raw"])
            response_id = graph.sourced("response_observation", "observation", response_data)
            graph.edge("response_to", response_id, request_id)
            responses += 1
            xhr = response.get("xhr_requests", [])
            if not isinstance(xhr, list):
                raise ValueError("Katana xhr_requests must be an array")
            for item in xhr:
                if proxy_confirmed_only:
                    if not isinstance(item, dict):
                        raise ValueError("Katana runtime XHR must be an object")
                    reason = _katana_ignored_discovery(
                        graph,
                        "ignored_runtime_xhr",
                        item.get("method", "GET"),
                        item.get("endpoint"),
                        scope,
                        provenance,
                        timestamp,
                    )
                    ignored_discovery_reasons["runtime_xhr:" + reason] += 1
                    ignored_discoveries += 1
                else:
                    _katana_request(graph, item, scope, provenance, "runtime_xhr", timestamp)
                xhr_count += 1
            form_items = response.get("forms", [])
            if not isinstance(form_items, list):
                raise ValueError("Katana forms must be an array")
            base = request.get("endpoint")
            for form in form_items:
                if not isinstance(form, dict) or not isinstance(form.get("action"), str):
                    raise ValueError("Katana form entries need an action")
                form_method = _method(form.get("method", "GET"))
                action = urljoin(base, form["action"])
                parameters = form.get("parameters", [])
                if not isinstance(parameters, list) or any(not isinstance(item, str) for item in parameters):
                    raise ValueError("Katana form parameters must be strings")
                if proxy_confirmed_only:
                    reason = _katana_ignored_discovery(
                        graph,
                        "ignored_form_action",
                        form_method,
                        action,
                        scope,
                        provenance,
                        timestamp,
                    )
                    ignored_discovery_reasons["html_form:" + reason] += 1
                    ignored_discoveries += 1
                else:
                    endpoint_id = graph.node("endpoint", "derived_candidate", _endpoint_data(action, form_method, scope, parameters=parameters))
                    graph.edge("derived_from", endpoint_id, response_id, {"basis": "html_form"})
                forms += 1
        if record.get("error"):
            errors += 1
    details = {"responses": responses, "runtime_xhr": xhr_count, "forms": forms, "tool_errors": errors}
    if proxy_confirmed:
        details["proxy_confirmed_responses"] = proxy_confirmed
    if guard_rejections:
        details["scope_guard_rejections"] = guard_rejections
    if ignored_unconfirmed:
        details["ignored_unconfirmed_requests"] = ignored_unconfirmed
        details["ignored_unconfirmed_reasons"] = dict(sorted(ignored_reasons.items()))
    if ignored_discoveries:
        details["ignored_discovery_items"] = ignored_discoveries
        details["ignored_discovery_reasons"] = dict(sorted(ignored_discovery_reasons.items()))
    return graph.finish(len(records), ignored_records=ignored_unconfirmed, details=details)


def _semver(value):
    match = re.match(r"^(\d+)\.(\d+)\.(\d+)", value)
    if not match:
        raise ValueError("Schemathesis version is not semantic")
    return tuple(int(item) for item in match.groups())


def schemathesis_exit_reason(value):
    """Map one verified Schemathesis engine stop reason to our manifest taxonomy."""
    if not isinstance(value, str) or value not in SCHEMATHESIS_STOP_REASONS:
        raise ValueError("Schemathesis EngineFinished has an unsupported stop reason")
    return SCHEMATHESIS_STOP_REASONS[value]


def _phase(value):
    if isinstance(value, dict):
        value = value.get("name")
    return _bounded_text(value, "Schemathesis phase", 128)


def _generation_mode(value):
    if not isinstance(value, dict):
        return None
    meta = value.get("meta")
    generation = meta.get("generation") if isinstance(meta, dict) else None
    return _bounded_text(generation.get("mode"), "Schemathesis generation mode", 128) if isinstance(generation, dict) else None


def _case_identity(case_node, allowed):
    if not isinstance(case_node, dict):
        return "unknown"
    value = case_node.get("value", {})
    if not isinstance(value, dict):
        return "unknown"
    identity = value.get("auth_identity")
    if identity is None:
        return "anonymous"
    identity = _reference(identity, "Schemathesis auth identity")
    if identity not in allowed:
        raise ValueError("Schemathesis auth identity is absent from the manifest")
    return identity


def _schemathesis_scenario(
    graph,
    event,
    provenance,
    scope,
    allowed_identities,
    metrics,
    *,
    proxy_confirmed_only=False,
    traffic_ids=None,
):
    if not isinstance(event, dict) or not isinstance(event.get("recorder"), dict):
        raise ValueError("Schemathesis scenario is missing its recorder")
    recorder = event["recorder"]
    cases = recorder.get("cases", {})
    interactions = recorder.get("interactions", {})
    checks = recorder.get("checks", {})
    if not all(isinstance(item, dict) for item in (cases, interactions, checks)):
        raise ValueError("Schemathesis recorder maps are invalid")
    scenario_ref = _digest({"id": event.get("id"), "line": provenance["line"]})
    phase = _phase(event.get("phase"))
    experiments = {}
    requests = {}
    responses = {}
    accepted_cases = set()
    interaction_proofs = {}
    if proxy_confirmed_only:
        if not isinstance(traffic_ids, set):
            raise ValueError("Schemathesis proxy confirmation state is invalid")
        for case_id, interaction in interactions.items():
            _string(case_id, "Schemathesis interaction case id", 256)
            if not isinstance(interaction, dict) or not isinstance(interaction.get("request"), dict):
                raise ValueError("Schemathesis interaction needs a request")
            response = interaction.get("response")
            if response is None:
                interaction_proofs[case_id] = ("unconfirmed", None)
                continue
            label = "Schemathesis case %s on line %d" % (case_id, provenance["line"])
            guard_reason = _scope_guard_rejection(response, label)
            if guard_reason is not None:
                interaction_proofs[case_id] = ("guard", guard_reason)
                continue
            traffic_id = _header_reference(
                response.get("headers"),
                TRAFFIC_ID_HEADER,
                label + " recording-proxy traffic id",
            )
            if traffic_id is None:
                raise ValueError(label + " response is not confirmed by the recording proxy")
            if traffic_id in traffic_ids:
                raise ValueError("Schemathesis output reuses a recording-proxy traffic id")
            traffic_ids.add(traffic_id)
            accepted_cases.add(case_id)
            interaction_proofs[case_id] = ("confirmed", traffic_id)
    for case_id, case_node in cases.items():
        _string(case_id, "Schemathesis case id", 256)
        if not isinstance(case_node, dict):
            raise ValueError("Schemathesis case node must be an object")
        value = case_node.get("value", {})
        if not isinstance(value, dict):
            raise ValueError("Schemathesis case value must be an object")
        identity = _case_identity(case_node, allowed_identities)
        metrics["cases"] += 1
        if proxy_confirmed_only and case_id not in accepted_cases:
            continue
        experiment_data = {
            "runner": "schemathesis",
            "scenario_ref": scenario_ref,
            "case_ref": _digest(case_id),
            "parent_case_ref": _digest(case_node["parent_id"]) if isinstance(case_node.get("parent_id"), str) else None,
            "phase": phase,
            "identity_ref": identity,
            "generation_mode": _generation_mode(value),
            "source_record": provenance,
        }
        experiments[case_id] = graph.sourced("experiment", "observation", experiment_data)
    for case_id, interaction in interactions.items():
        _string(case_id, "Schemathesis interaction case id", 256)
        if not isinstance(interaction, dict) or not isinstance(interaction.get("request"), dict):
            raise ValueError("Schemathesis interaction needs a request")
        metrics["interactions"] += 1
        request = interaction["request"]
        proof, proof_value = interaction_proofs.get(case_id, (None, None))
        method = (
            _attempted_method(request.get("method"), "Schemathesis attempted method")
            if proxy_confirmed_only and proof in {"guard", "unconfirmed"}
            else _method(request.get("method"))
        )
        uri = request.get("uri")
        if not isinstance(uri, str):
            raise ValueError("Schemathesis request is missing uri")
        timestamp = (
            interaction.get("timestamp")
            if isinstance(interaction.get("timestamp"), (int, float))
            and not isinstance(interaction.get("timestamp"), bool)
            else None
        )
        if proxy_confirmed_only and proof == "guard":
            graph.sourced("coverage_observation", "observation", {
                "kind": "scope_guard_rejection",
                "reason": proof_value,
                "method": method,
                "status": interaction["response"]["status_code"],
                "attempted_url_sha256": _payload_hash(uri),
                "timestamp": timestamp,
                "source_record": provenance,
            })
            metrics["scope_guard_rejections"] += 1
            continue
        if proxy_confirmed_only and proof == "unconfirmed":
            request_origin = _origin(uri, "Schemathesis request URI")
            reason = "outside_scope" if request_origin not in scope else "no_response"
            graph.sourced("coverage_observation", "observation", {
                "kind": "ignored_unconfirmed_interaction",
                "reason": reason,
                "method": method,
                "attempted_url_sha256": _payload_hash(uri),
                "timestamp": timestamp,
                "source_record": provenance,
            })
            metrics["ignored_unconfirmed_interactions"] += 1
            metrics["ignored_unconfirmed_" + reason] += 1
            continue
        if case_id not in experiments:
            experiments[case_id] = graph.sourced("experiment", "observation", {
                "runner": "schemathesis", "scenario_ref": scenario_ref, "case_ref": _digest(case_id),
                "parent_case_ref": None, "phase": phase, "identity_ref": "unknown", "generation_mode": None,
                "source_record": provenance,
            })
        safe = _safe_url(uri, scope, "Schemathesis request URI")
        identity = _case_identity(cases.get(case_id), allowed_identities)
        request_data = {
            "method": method,
            "url": safe,
            "header_names": _header_names(request.get("headers")),
            "identity_ref": identity,
            "case_ref": _digest(case_id),
            "timestamp": timestamp,
            "source_record": provenance,
        }
        if "body" in request:
            request_data["body_sha256"] = _payload_hash(request["body"])
        body_size = request.get("body_size")
        if body_size is not None:
            request_data["body_size"] = _nonnegative_integer(body_size, "Schemathesis request body_size")
        request_id = graph.sourced("request_observation", "observation", request_data)
        endpoint_id = graph.node("endpoint", "derived_candidate", _endpoint_data(safe, method, scope))
        graph.edge("observed_at", request_id, endpoint_id, {"authority": "Schemathesis recorded interaction"})
        graph.edge("probes", experiments[case_id], endpoint_id)
        graph.edge("derived_from", request_id, experiments[case_id])
        requests[case_id] = request_id
        response = interaction.get("response")
        if response is not None:
            if not isinstance(response, dict):
                raise ValueError("Schemathesis response must be an object")
            status = response.get("status_code")
            if not isinstance(status, int) or isinstance(status, bool) or not 100 <= status <= 599:
                raise ValueError("Schemathesis response needs a valid status_code")
            response_data = {
                "status": status,
                "header_names": _header_names(response.get("headers")),
                "case_ref": _digest(case_id),
                "elapsed": response.get("elapsed")
                    if isinstance(response.get("elapsed"), (int, float)) and not isinstance(response.get("elapsed"), bool)
                    else None,
                "truncated": "content_size" in response,
                "source_record": provenance,
            }
            if proxy_confirmed_only:
                response_data["traffic_id"] = proof_value
                metrics["proxy_confirmed_responses"] += 1
            if "content" in response:
                response_data["body_sha256"] = _payload_hash(response["content"])
            if "content_size" in response:
                response_data["original_body_size"] = _nonnegative_integer(
                    response["content_size"], "Schemathesis response content_size"
                )
                metrics["payloads_truncated"] += 1
            response_id = graph.sourced("response_observation", "observation", response_data)
            graph.edge("response_to", response_id, request_id)
            responses[case_id] = response_id
    for case_id, case_checks in checks.items():
        if not isinstance(case_checks, list):
            raise ValueError("Schemathesis checks must be arrays")
        for check in case_checks:
            if not isinstance(check, dict):
                raise ValueError("Schemathesis check must be an object")
            status = check.get("status")
            if status not in {"success", "failure", "error", "skip"}:
                raise ValueError("Schemathesis check has an unknown status")
            if proxy_confirmed_only and case_id not in accepted_cases:
                metrics["ignored_unconfirmed_checks"] += 1
                continue
            metrics["checks_" + status] += 1
            if status != "failure":
                continue
            info = check.get("failure_info")
            failure = info.get("failure") if isinstance(info, dict) else None
            if not isinstance(failure, dict):
                raise ValueError("failed Schemathesis check lacks failure details")
            failure_case = failure.get("case_id") if isinstance(failure.get("case_id"), str) else case_id
            message = failure.get("message", "")
            if not isinstance(message, str):
                message = _canonical(message)
            hypothesis = graph.sourced("hypothesis", "hypothesis", {
                "producer": "schemathesis",
                "check": _bounded_text(check.get("name"), "Schemathesis check name", 256),
                "failure_type": _bounded_text(failure.get("type"), "Schemathesis failure type", 256),
                "title": _bounded_text(failure.get("title"), "Schemathesis failure title", 512),
                "severity": _bounded_text(failure.get("severity"), "Schemathesis severity", 64),
                "operation": _bounded_text(failure.get("operation"), "Schemathesis operation", 512),
                "message_sha256": _payload_hash(message),
                "message_length": len(message),
                "scenario_ref": scenario_ref,
                "case_ref": _digest(failure_case),
                "verdict": "requires_api_lab_controls_and_replay",
                "source_record": provenance,
            })
            related = responses.get(failure_case) or requests.get(failure_case) or experiments.get(failure_case)
            if related:
                graph.edge("derived_from", hypothesis, related)
            metrics["hypotheses"] += 1
    for case_id, case_node in cases.items():
        parent = case_node.get("parent_id") if isinstance(case_node, dict) else None
        if isinstance(parent, str) and parent in experiments and case_id in experiments:
            graph.edge("transitions_to", experiments[parent], experiments[case_id])
    filtered = recorder.get("filtered_failures", {})
    if not isinstance(filtered, dict) or any(not isinstance(value, int) or value < 0 for value in filtered.values()):
        raise ValueError("Schemathesis filtered_failures is invalid")
    metrics["filtered_failures"] += sum(filtered.values())


def schemathesis_terminal_exit_reason(
    data,
    *,
    max_bytes=DEFAULT_MAX_BYTES,
    max_records=DEFAULT_MAX_RECORDS,
    max_line_bytes=DEFAULT_MAX_LINE_BYTES,
):
    """Return the mapped reason only for a single, terminal EngineFinished event."""
    _, records = _jsonl(
        data,
        "Schemathesis NDJSON",
        max_bytes=max_bytes,
        max_records=max_records,
        max_line_bytes=max_line_bytes,
    )
    positions = [
        index for index, (_, _, record) in enumerate(records)
        if set(record) == {"EngineFinished"}
    ]
    if positions != [len(records) - 1]:
        raise ValueError("Schemathesis EngineFinished must be the single terminal event")
    event = records[-1][2]["EngineFinished"]
    if not isinstance(event, dict):
        raise ValueError("Schemathesis EngineFinished must be an object")
    return schemathesis_exit_reason(event.get("stop_reason"))


def schemathesis_proxy_traffic_ids(
    data,
    *,
    max_bytes=DEFAULT_MAX_BYTES,
    max_records=DEFAULT_MAX_RECORDS,
    max_line_bytes=DEFAULT_MAX_LINE_BYTES,
):
    """Extract unique recording-proxy commit IDs from Schemathesis interactions."""
    _, records = _jsonl(
        data,
        "Schemathesis NDJSON",
        max_bytes=max_bytes,
        max_records=max_records,
        max_line_bytes=max_line_bytes,
    )
    traffic_ids = []
    responses = 0
    for line, _, record in records:
        if len(record) != 1:
            raise ValueError("Schemathesis event must have one externally tagged field")
        event_type, event = next(iter(record.items()))
        if event_type not in {"ScenarioFinished", "FuzzScenarioFinished"}:
            continue
        if not isinstance(event, dict) or not isinstance(event.get("recorder"), dict):
            raise ValueError("Schemathesis scenario is missing its recorder")
        interactions = event["recorder"].get("interactions", {})
        if not isinstance(interactions, dict):
            raise ValueError("Schemathesis recorder maps are invalid")
        for case_id, interaction in interactions.items():
            _string(case_id, "Schemathesis interaction case id", 256)
            if not isinstance(interaction, dict) or not isinstance(interaction.get("request"), dict):
                raise ValueError("Schemathesis interaction needs a request")
            response = interaction.get("response")
            if response is None:
                continue
            responses += 1
            label = "Schemathesis case %s on line %d" % (case_id, line)
            if _scope_guard_rejection(response, label) is not None:
                continue
            traffic_id = _header_reference(
                response.get("headers"),
                TRAFFIC_ID_HEADER,
                label + " recording-proxy traffic id",
            )
            if traffic_id is None:
                raise ValueError(label + " response is not confirmed by the recording proxy")
            traffic_ids.append(traffic_id)
    if responses == 0:
        raise ValueError("Schemathesis output contains no HTTP responses to confirm")
    if not traffic_ids:
        raise ValueError("Schemathesis output contains no recording-proxy-confirmed HTTP responses")
    if len(set(traffic_ids)) != len(traffic_ids):
        raise ValueError("Schemathesis output reuses a recording-proxy traffic id")
    return tuple(traffic_ids)


def import_schemathesis_ndjson(
    data,
    manifest,
    *,
    proxy_confirmed_only=False,
    max_bytes=DEFAULT_MAX_BYTES,
    max_records=DEFAULT_MAX_RECORDS,
    max_line_bytes=DEFAULT_MAX_LINE_BYTES,
):
    if not isinstance(proxy_confirmed_only, bool):
        raise ValueError("proxy_confirmed_only must be boolean")
    raw, records = _jsonl(data, "Schemathesis NDJSON", max_bytes=max_bytes, max_records=max_records, max_line_bytes=max_line_bytes)
    limitations = [
        "Schemathesis check failures are hypotheses until independent API Lab controls and replay succeed."
    ]
    if proxy_confirmed_only:
        limitations.append(
            "Only recording-proxy-confirmed responses are target evidence; scope-guard responses are coverage only."
        )
    prepared = _prepare_manifest(
        manifest,
        "schemathesis",
        "schemathesis-ndjson-v4.31.1",
        raw,
        records,
        "application/x-ndjson",
        limitations,
    )
    first = records[0][2]
    if set(first) != {"Initialize"} or not isinstance(first["Initialize"], dict):
        raise ValueError("Schemathesis NDJSON must start with Initialize")
    initialized = first["Initialize"]
    version = initialized.get("schemathesis_version")
    if version != prepared["tool"]["version"]:
        raise ValueError("Schemathesis report version differs from the manifest")
    if _semver(version) < (4, 31, 1):
        raise ValueError("Schemathesis adapter requires version 4.31.1 or newer")
    finish_positions = [index for index, (_, _, record) in enumerate(records[1:], 1) if set(record) == {"EngineFinished"}]
    if len(finish_positions) > 1 or finish_positions and finish_positions[0] != len(records) - 1:
        raise ValueError("Schemathesis EngineFinished must be the single terminal event")
    if any(set(record) == {"Initialize"} for _, _, record in records[1:]):
        raise ValueError("Schemathesis stream contains a second Initialize event")
    stream_has_finish = bool(finish_positions)
    if prepared["exit_reason"] == "completed" and not stream_has_finish:
        raise ValueError("completed Schemathesis run is missing EngineFinished")
    if not stream_has_finish and not prepared["truncated"]:
        raise ValueError("incomplete Schemathesis stream must be marked truncated")
    graph = _Graph(prepared)
    scope = set(prepared["scope"]["origins"])
    identities = set(prepared["identity_refs"])
    metrics = Counter()
    command_sha256 = _payload_hash(initialized.get("command", ""))
    seed = initialized.get("seed")
    if seed is not None and (not isinstance(seed, int) or isinstance(seed, bool)):
        raise ValueError("Schemathesis seed must be an integer or null")
    event_types = Counter()
    engine_finished = False
    traffic_ids = set()
    for line, raw_hash, record in records[1:]:
        if len(record) != 1:
            raise ValueError("Schemathesis event must have one externally tagged field")
        event_type, event = next(iter(record.items()))
        event_types[event_type] += 1
        provenance = _record_source(line, raw_hash)
        if event_type in {"ScenarioFinished", "FuzzScenarioFinished"}:
            _schemathesis_scenario(
                graph,
                event,
                provenance,
                scope,
                identities,
                metrics,
                proxy_confirmed_only=proxy_confirmed_only,
                traffic_ids=traffic_ids,
            )
        elif event_type == "EngineFinished":
            if not isinstance(event, dict):
                raise ValueError("Schemathesis EngineFinished must be an object")
            engine_finished = True
            reported_reason = schemathesis_exit_reason(event.get("stop_reason"))
            if reported_reason != prepared["exit_reason"]:
                raise ValueError("Schemathesis stop reason differs from the manifest")
        elif event_type in {"FatalError", "NonFatalError"}:
            metrics["engine_errors"] += 1
    coverage = graph.sourced("coverage_observation", "observation", {
        "producer": "schemathesis",
        "event_types": dict(sorted(event_types.items())),
        "cases": metrics["cases"],
        "interactions": metrics["interactions"],
        "checks": {name: metrics["checks_" + name] for name in ("success", "failure", "error", "skip")},
        "filtered_failures": metrics["filtered_failures"],
        "payloads_truncated": metrics["payloads_truncated"],
        "complete_event_stream": engine_finished,
        "initialize_command_sha256": command_sha256,
        "seed": seed,
    })
    graph.edge("derived_from", coverage, graph.run)
    details = {
        "event_types": dict(sorted(event_types.items())),
        "cases": metrics["cases"],
        "interactions": metrics["interactions"],
        "checks_success": metrics["checks_success"],
        "checks_failure": metrics["checks_failure"],
        "checks_error": metrics["checks_error"],
        "checks_skip": metrics["checks_skip"],
        "filtered_failures": metrics["filtered_failures"],
        "payloads_truncated": metrics["payloads_truncated"],
        "hypotheses": metrics["hypotheses"],
        "engine_finished": engine_finished,
        "initialize_command_sha256": command_sha256,
        "seed": seed,
    }
    if proxy_confirmed_only:
        details.update({
            "proxy_confirmed_responses": metrics["proxy_confirmed_responses"],
            "scope_guard_rejections": metrics["scope_guard_rejections"],
            "ignored_unconfirmed_interactions": metrics["ignored_unconfirmed_interactions"],
            "ignored_unconfirmed_checks": metrics["ignored_unconfirmed_checks"],
            "ignored_unconfirmed_reasons": {
                name.removeprefix("ignored_unconfirmed_"): count
                for name, count in sorted(metrics.items())
                if name.startswith("ignored_unconfirmed_")
                and name not in {"ignored_unconfirmed_interactions", "ignored_unconfirmed_checks"}
            },
        })
        if len(traffic_ids) != metrics["proxy_confirmed_responses"]:
            raise ValueError("Schemathesis evidence lost recording-proxy lineage")
    ignored = sum(count for name, count in event_types.items() if name not in {"ScenarioFinished", "FuzzScenarioFinished", "EngineFinished"})
    return graph.finish(len(records), ignored_records=ignored, details=details)


def _nuclei_target(record, scope):
    for name in ("url", "host", "matched-at"):
        value = record.get(name)
        if isinstance(value, str) and urlsplit(value).scheme in ("http", "https"):
            try:
                return _safe_url(value, scope, "Nuclei target")
            except ValueError:
                if name in ("url", "host"):
                    raise
    raise ValueError("Nuclei result lacks an in-scope HTTP target")


def _raw_method(value):
    if not isinstance(value, str) or not value:
        return None
    candidate = value.splitlines()[0].split(" ", 1)[0].upper()
    return candidate if candidate in METHODS else None


def _raw_status(value):
    if not isinstance(value, str) or not value:
        return None
    match = re.match(r"^HTTP/\S+\s+(\d{3})(?:\s|$)", value.splitlines()[0])
    return int(match.group(1)) if match else None


def _interaction_callback(graph, interaction, source_record, hypothesis=None):
    if not isinstance(interaction, dict):
        raise ValueError("Nuclei interaction must be an object")
    if not graph.manifest["external_egress"]:
        raise ValueError("Nuclei OAST interaction is absent from the external egress manifest")
    protocol_value = interaction.get("protocol")
    if not isinstance(protocol_value, str):
        raise ValueError("Nuclei interaction protocol must be a string")
    protocol = protocol_value.lower()
    if protocol not in OAST_PROTOCOLS:
        raise ValueError("Nuclei interaction uses a disabled OAST protocol")
    unique = interaction.get("unique-id")
    if not isinstance(unique, str) or not unique:
        raise ValueError("Nuclei interaction is not correlated")
    callback = graph.sourced("oast_callback", "observation", {
        "protocol": protocol,
        "correlation_sha256": _payload_hash(unique),
        "full_id_sha256": _payload_hash(interaction["full-id"]) if interaction.get("full-id") else None,
        "raw_request_sha256": _payload_hash(interaction["raw-request"]) if interaction.get("raw-request") else None,
        "raw_response_sha256": _payload_hash(interaction["raw-response"]) if interaction.get("raw-response") else None,
        "remote_address_sha256": _payload_hash(interaction["remote-address"]) if interaction.get("remote-address") else None,
        "q_type": _bounded_text(interaction.get("q-type"), "OAST q-type", 32),
        "timestamp": _bounded_text(interaction.get("timestamp"), "OAST timestamp", 128),
        "source_record": source_record,
    })
    if hypothesis:
        graph.edge("supports", callback, hypothesis, {"meaning": "callback_observed_not_impact_confirmed"})
    return callback


def import_nuclei_jsonl(
    data,
    manifest,
    *,
    max_bytes=DEFAULT_MAX_BYTES,
    max_records=DEFAULT_MAX_RECORDS,
    max_line_bytes=DEFAULT_MAX_LINE_BYTES,
):
    raw, records = _jsonl(data, "Nuclei JSONL", max_bytes=max_bytes, max_records=max_records, max_line_bytes=max_line_bytes)
    prepared = _prepare_manifest(
        manifest,
        "nuclei",
        "nuclei-result-event-jsonl",
        raw,
        records,
        "application/x-ndjson",
        ("A Nuclei template match is a hypothesis until independently replayed.",),
    )
    if prepared["corpus_sha256"] is None:
        raise ValueError("Nuclei imports require a pinned template corpus digest")
    if _semver(prepared["tool"]["version"]) < (3, 10, 0):
        raise ValueError("Nuclei DAST adapter requires version 3.10.0 or newer")
    graph = _Graph(prepared)
    scope = set(prepared["scope"]["origins"])
    matches = failures = interactions = 0
    for line, raw_hash, record in records:
        if "llm" in record:
            raise ValueError("LLM matcher output is not accepted by the deterministic Nuclei adapter")
        template = _string(record.get("template-id"), "Nuclei template id", 512)
        protocol_type = _string(record.get("type"), "Nuclei result type", 128)
        if protocol_type.lower() == "code":
            raise ValueError("Nuclei code templates are disabled")
        status = record.get("matcher-status")
        if not isinstance(status, bool):
            raise ValueError("Nuclei result needs matcher-status")
        target = _nuclei_target(record, scope)
        source_record = _record_source(line, raw_hash)
        request_id = response_id = None
        request = record.get("request")
        method = _raw_method(request) or "GET"
        if request:
            request_id = graph.sourced("request_observation", "observation", {
                "method": method,
                "url": target,
                "identity_ref": "unknown",
                "raw_request_sha256": _payload_hash(request),
                "timestamp": _bounded_text(record.get("timestamp"), "Nuclei timestamp", 128),
                "source_record": source_record,
            })
            endpoint_id = graph.node("endpoint", "derived_candidate", _endpoint_data(target, method, scope))
            graph.edge("observed_at", request_id, endpoint_id, {"authority": "Nuclei emitted request"})
        response = record.get("response")
        if response:
            response_id = graph.sourced("response_observation", "observation", {
                "status": _raw_status(response),
                "raw_response_sha256": _payload_hash(response),
                "timestamp": _bounded_text(record.get("timestamp"), "Nuclei timestamp", 128),
                "source_record": source_record,
            })
            if request_id:
                graph.edge("response_to", response_id, request_id)
        info = record.get("info", {})
        if not isinstance(info, dict):
            raise ValueError("Nuclei info must be an object")
        if status:
            extracted = record.get("extracted-results", [])
            if not isinstance(extracted, list) or any(not isinstance(item, str) for item in extracted):
                raise ValueError("Nuclei extracted-results must be an array of strings")
            hypothesis = graph.sourced("hypothesis", "hypothesis", {
                "producer": "nuclei",
                "template_id": template,
                "template_name": _bounded_text(info.get("name"), "Nuclei template name", 512),
                "severity": _bounded_text(info.get("severity"), "Nuclei severity", 64),
                "protocol": protocol_type,
                "matcher_name": _bounded_text(record.get("matcher-name"), "Nuclei matcher name", 256),
                "target": target,
                "fuzzing": bool(record.get("is_fuzzing_result")),
                "fuzzing_method": _bounded_text(record.get("fuzzing_method"), "Nuclei fuzzing method", 128),
                "fuzzing_parameter": _bounded_text(record.get("fuzzing_parameter"), "Nuclei fuzzing parameter", 256),
                "fuzzing_position": _bounded_text(record.get("fuzzing_position"), "Nuclei fuzzing position", 128),
                "extracted_results_count": len(extracted),
                "extracted_results_sha256": _digest(extracted),
                "embedded_template_sha256": _base64_payload_hash(record["template-encoded"], "Nuclei embedded template")
                    if record.get("template-encoded") else None,
                "curl_command_sha256": _payload_hash(record["curl-command"]) if record.get("curl-command") else None,
                "verdict": "requires_independent_replay",
                "timestamp": _bounded_text(record.get("timestamp"), "Nuclei timestamp", 128),
                "source_record": source_record,
            })
            related = response_id or request_id
            if related:
                graph.edge("derived_from", hypothesis, related)
            matches += 1
            if record.get("interaction") is not None:
                _interaction_callback(graph, record["interaction"], source_record, hypothesis)
                interactions += 1
        else:
            graph.sourced("coverage_observation", "observation", {
                "producer": "nuclei",
                "template_id": template,
                "target": target,
                "matched": False,
                "error_sha256": _payload_hash(record["error"]) if record.get("error") else None,
                "timestamp": _bounded_text(record.get("timestamp"), "Nuclei timestamp", 128),
                "source_record": source_record,
            })
            failures += 1
    return graph.finish(len(records), details={"matches": matches, "nonmatches_or_errors": failures, "oast_interactions": interactions})


def import_interactsh_jsonl(
    data,
    manifest,
    correlations,
    *,
    max_bytes=DEFAULT_MAX_BYTES,
    max_records=DEFAULT_MAX_RECORDS,
    max_line_bytes=DEFAULT_MAX_LINE_BYTES,
):
    raw, records = _jsonl(data, "Interactsh JSONL", max_bytes=max_bytes, max_records=max_records, max_line_bytes=max_line_bytes)
    prepared = _prepare_manifest(
        manifest,
        "interactsh",
        "interactsh-interaction-jsonl",
        raw,
        records,
        "application/x-ndjson",
        ("An OAST callback proves an interaction, not exploit impact or data exfiltration.",),
    )
    if not prepared["external_egress"]:
        raise ValueError("Interactsh endpoint is absent from the external egress manifest")
    if not isinstance(correlations, dict) or not correlations:
        raise ValueError("Interactsh import requires explicit correlation metadata")
    normalized = {}
    for unique, value in correlations.items():
        _string(unique, "Interactsh correlation id", 512)
        if not isinstance(value, dict) or set(value) != {"payload_sha256", "experiment_ref"}:
            raise ValueError("Interactsh correlation metadata is incomplete")
        normalized[unique] = {
            "payload_sha256": _hash(value["payload_sha256"], "OAST payload digest"),
            "experiment_ref": _reference(value["experiment_ref"], "OAST experiment reference"),
        }
    graph = _Graph(prepared)
    for line, raw_hash, record in records:
        protocol_value = record.get("protocol")
        if not isinstance(protocol_value, str):
            raise ValueError("Interactsh callback protocol must be a string")
        protocol = protocol_value.lower()
        if protocol not in OAST_PROTOCOLS:
            raise ValueError("Interactsh callback uses a disabled protocol")
        unique = record.get("unique-id")
        if not isinstance(unique, str) or unique not in normalized:
            raise ValueError("Interactsh callback has no registered correlation")
        source_record = _record_source(line, raw_hash)
        mapping = normalized[unique]
        payload = graph.sourced("oast_payload", "observation", {
            "payload_sha256": mapping["payload_sha256"],
            "experiment_ref": mapping["experiment_ref"],
        })
        callback = graph.sourced("oast_callback", "observation", {
            "protocol": protocol,
            "correlation_sha256": _payload_hash(unique),
            "full_id_sha256": _payload_hash(record["full-id"]) if record.get("full-id") else None,
            "raw_request_sha256": _payload_hash(record["raw-request"]) if record.get("raw-request") else None,
            "raw_response_sha256": _payload_hash(record["raw-response"]) if record.get("raw-response") else None,
            "remote_address_sha256": _payload_hash(record["remote-address"]) if record.get("remote-address") else None,
            "q_type": _bounded_text(record.get("q-type"), "Interactsh q-type", 32),
            "timestamp": _bounded_text(record.get("timestamp"), "Interactsh timestamp", 128),
            "source_record": source_record,
        })
        graph.edge("callback_for", callback, payload)
    return graph.finish(len(records), details={"correlated_callbacks": len(records), "protocols": sorted({record[2]["protocol"].lower() for record in records})})


def validate_bundle(bundle):
    if not isinstance(bundle, dict) or set(bundle) != {"format", "manifest", "nodes", "edges", "summary"}:
        raise ValueError("runtime evidence bundle fields are invalid")
    if bundle["format"] != FORMAT:
        raise ValueError("unsupported runtime evidence format")
    manifest = validate_manifest(bundle["manifest"], imported=True)
    if manifest != bundle["manifest"]:
        raise ValueError("runtime evidence manifest is not normalized")
    nodes = bundle["nodes"]
    edges = bundle["edges"]
    if not isinstance(nodes, list) or not isinstance(edges, list):
        raise ValueError("nodes and edges must be arrays")
    if nodes != sorted(nodes, key=lambda item: item.get("id", "") if isinstance(item, dict) else ""):
        raise ValueError("evidence nodes must use canonical order")
    if edges != sorted(edges, key=lambda item: item.get("id", "") if isinstance(item, dict) else ""):
        raise ValueError("evidence edges must use canonical order")
    identifiers = set()
    for node in nodes:
        if not isinstance(node, dict) or set(node) != {"id", "kind", "classification", "data"}:
            raise ValueError("invalid evidence node")
        if node["kind"] not in NODE_KINDS or node["classification"] not in CLASSIFICATIONS:
            raise ValueError("unsupported evidence node")
        if node["kind"] == "hypothesis" and node["classification"] != "hypothesis":
            raise ValueError("hypothesis nodes must remain hypotheses")
        if node["kind"] != "hypothesis" and node["classification"] == "hypothesis":
            raise ValueError("hypothesis classification is reserved")
        _json_value(node["data"], "node data")
        _assert_redacted(node["data"])
        expected = "n:" + _digest({key: node[key] for key in ("kind", "classification", "data")})
        if node["id"] != expected or node["id"] in identifiers:
            raise ValueError("node identity or uniqueness check failed")
        identifiers.add(node["id"])
    edge_ids = set()
    for edge in edges:
        if not isinstance(edge, dict) or set(edge) != {"id", "kind", "source", "target", "data"}:
            raise ValueError("invalid evidence edge")
        if edge["kind"] not in EDGE_KINDS or edge["source"] not in identifiers or edge["target"] not in identifiers:
            raise ValueError("unsupported or dangling evidence edge")
        _json_value(edge["data"], "edge data")
        _assert_redacted(edge["data"])
        expected = "e:" + _digest({key: edge[key] for key in ("kind", "source", "target", "data")})
        if edge["id"] != expected or edge["id"] in edge_ids:
            raise ValueError("edge identity or uniqueness check failed")
        edge_ids.add(edge["id"])
    summary = bundle["summary"]
    if not isinstance(summary, dict) or set(summary) != {"records", "ignored_records", "node_counts", "edge_counts", "claims", "details"}:
        raise ValueError("runtime evidence summary is invalid")
    _json_value(summary, "runtime evidence summary")
    _assert_redacted(summary)
    _nonnegative_integer(summary["records"], "summary records")
    _nonnegative_integer(summary["ignored_records"], "summary ignored records")
    expected = _summary(nodes, edges, summary["records"], summary["ignored_records"], summary["details"])
    if summary != expected:
        raise ValueError("runtime evidence summary does not match graph contents")
    if summary["claims"]["findings"] != 0:
        raise ValueError("external tool imports cannot create findings")
    tool_runs = [node for node in nodes if node["kind"] == "tool_run"]
    if len(tool_runs) != 1 or tool_runs[0]["data"] != manifest:
        raise ValueError("bundle must contain exactly one matching tool run")
    return copy.deepcopy(bundle)


def dumps_bundle(bundle):
    """Return the canonical archive bytes for a validated immutable batch."""
    validate_bundle(bundle)
    return (_canonical(bundle) + "\n").encode("ascii")


def bundle_sha256(bundle):
    return _bytes_digest(dumps_bundle(bundle))


def loads_bundle(data, expected_sha256=None, *, max_bytes=DEFAULT_MAX_BYTES):
    """Restore only a canonical, hash-matching runtime evidence archive."""
    raw = _input_bytes(data, max_bytes)
    if expected_sha256 is not None and _bytes_digest(raw) != _hash(expected_sha256, "archive digest"):
        raise ValueError("runtime evidence archive digest mismatch")
    try:
        value = json.loads(raw.decode("ascii"), object_pairs_hook=_duplicate_rejecting_object, parse_constant=_reject_constant)
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        raise ValueError("runtime evidence archive is invalid JSON: " + str(exc)) from None
    validate_bundle(value)
    if dumps_bundle(value) != raw:
        raise ValueError("runtime evidence archive is not canonical")
    return value
