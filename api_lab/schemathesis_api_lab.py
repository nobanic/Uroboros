"""Offline Schemathesis failure to API Lab draft-plan bridge.

The bridge deliberately handles only anonymous, proxy-confirmed, query-only
GET cases that produced a 5xx response.  It never executes the resulting plan,
stores it in the API Lab database, registers an MCP tool, or creates a finding.
"""

from __future__ import annotations

import hashlib
import json
import re
from collections import Counter, defaultdict
from urllib.parse import parse_qsl, urlsplit

from api_lab.engine import digest, origin, validate_plan
from api_lab.runtime_evidence import loads_bundle
from api_lab.schemathesis_shadow import SCHEMATHESIS_VERSION
from api_lab.traffic_schema_projection import loads_executable_traffic_schema


CONTRACT = "uroboros-schemathesis-api-lab-proposals-v1"
DEFAULT_MAX_REPORT_BYTES = 128 * 1024 * 1024
DEFAULT_MAX_RECORDS = 100_000
DEFAULT_MAX_LINE_BYTES = 16 * 1024 * 1024
MAX_PROPOSALS = 100
SHA256 = re.compile(r"[0-9a-fA-F]{64}\Z")
SENSITIVE_HEADER_NAMES = {"authorization", "cookie", "proxy-authorization"}
SENSITIVE_PARAMETER_PARTS = {
    "authorization", "cookie", "csrf", "key", "password", "passwd",
    "secret", "session", "token",
}
BOUNDARIES = {
    "network_requests": 0,
    "model_used": False,
    "automatic_execution": False,
    "database_write": False,
    "mcp_registration": False,
    "planner_visibility": False,
    "vvah_used": False,
}


def _canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("ascii") + b"\n"


def _sha256(value, label):
    if not isinstance(value, str) or not SHA256.fullmatch(value):
        raise ValueError(label + " must be a canonical SHA-256 string")
    return value.lower()


def _bytes(data, label, maximum):
    if not isinstance(data, (bytes, bytearray)):
        raise ValueError(label + " must be bytes")
    raw = bytes(data)
    if not raw or len(raw) > maximum:
        raise ValueError(label + " is empty or exceeds its byte limit")
    return raw


def _duplicate_rejecting_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("Schemathesis report contains a duplicate JSON key")
        result[key] = value
    return result


def _reject_constant(value):
    raise ValueError("Schemathesis report contains a non-finite JSON number: " + value)


def _records(data, max_records=DEFAULT_MAX_RECORDS, max_line_bytes=DEFAULT_MAX_LINE_BYTES):
    records = []
    for line_number, line in enumerate(data.splitlines(keepends=True), 1):
        content = line.rstrip(b"\r\n")
        if not content.strip():
            continue
        if len(content) > max_line_bytes:
            raise ValueError("Schemathesis report line exceeds the byte limit")
        if len(records) >= max_records:
            raise ValueError("Schemathesis report exceeds the record limit")
        try:
            record = json.loads(
                content.decode("utf-8"),
                object_pairs_hook=_duplicate_rejecting_object,
                parse_constant=_reject_constant,
            )
        except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
            raise ValueError("Schemathesis report line %d is invalid JSON: %s" % (line_number, exc)) from None
        if not isinstance(record, dict):
            raise ValueError("Schemathesis report line %d is not an object" % line_number)
        records.append((line_number, hashlib.sha256(content).hexdigest(), record))
    if not records:
        raise ValueError("Schemathesis report contains no records")
    return records


def _header_value(headers, name, label):
    if not isinstance(headers, dict):
        raise ValueError(label + " headers are invalid")
    values = [value for key, value in headers.items() if isinstance(key, str) and key.lower() == name]
    if len(values) != 1:
        raise ValueError(label + " must contain exactly one " + name + " header")
    value = values[0]
    if isinstance(value, list):
        if len(value) != 1:
            raise ValueError(label + " header is ambiguous")
        value = value[0]
    if not isinstance(value, str) or not value or len(value) > 256:
        raise ValueError(label + " header is invalid")
    return value


def _sensitive_parameter(name):
    if not isinstance(name, str) or not name or len(name) > 256:
        return True
    lowered = name.lower()
    if lowered in {"apikey", "api-key", "api_key"}:
        return True
    parts = {part for part in re.split(r"[^a-z0-9]+", lowered) if part}
    return bool(parts & SENSITIVE_PARAMETER_PARTS)


def _query_strings(value):
    if isinstance(value, str) and len(value) <= 4096:
        return value
    if (
        isinstance(value, list)
        and 1 <= len(value) <= 32
        and all(isinstance(item, str) and len(item) <= 4096 for item in value)
    ):
        return list(value)
    raise ValueError("query value is not a bounded string or string array")


def _control_value(parameter):
    schema = parameter.get("schema")
    if not isinstance(schema, dict) or schema.get("type") != "string":
        raise ValueError("query control supports string schemas only")
    if any(key in schema for key in ("default", "enum", "example", "examples", "pattern", "format")):
        raise ValueError("query schema contains a value-bearing or unsupported control keyword")
    minimum = schema.get("minLength", 0)
    maximum = schema.get("maxLength", 4096)
    if (
        not isinstance(minimum, int)
        or isinstance(minimum, bool)
        or not isinstance(maximum, int)
        or isinstance(maximum, bool)
        or minimum < 0
        or maximum < minimum
        or maximum > 4096
    ):
        raise ValueError("query string length constraints are invalid")
    value = "uroboros-control"
    if len(value) < minimum:
        value += "a" * (minimum - len(value))
    if len(value) > maximum:
        value = "a" * max(minimum, 1)
    if not minimum <= len(value) <= maximum:
        raise ValueError("query string cannot receive a deterministic control")
    return value


def _operation_index(document):
    evidence_statuses = defaultdict(Counter)
    for item in document["x-uroboros-traffic"]["evidence"]:
        if 200 <= item["status"] <= 299:
            evidence_statuses[item["operation_id"]][item["status"]] += 1
    result = {}
    for path, path_item in document["paths"].items():
        for method, operation in path_item.items():
            if method != "get" or "{" in path or "}" in path:
                continue
            parameters = {}
            valid = True
            for parameter in operation.get("parameters", []):
                if (
                    not isinstance(parameter, dict)
                    or parameter.get("in") != "query"
                    or not isinstance(parameter.get("name"), str)
                    or parameter["name"] in parameters
                ):
                    valid = False
                    break
                parameters[parameter["name"]] = parameter
            statuses = evidence_statuses[operation["operationId"]]
            declared = {
                int(status)
                for status in operation.get("responses", {})
                if isinstance(status, str) and status.isdigit() and 200 <= int(status) <= 299
            }
            eligible = [status for status in statuses if status in declared]
            if valid and parameters and eligible:
                expected = sorted(eligible, key=lambda status: (-statuses[status], status))[0]
                result[(path, "GET")] = {
                    "operation_id": operation["operationId"],
                    "parameters": parameters,
                    "expected_status": expected,
                }
    return result


def _evidence_index(bundle, report_sha256, schema_sha256, schema_id, target_origin):
    manifest = bundle["manifest"]
    if manifest["tool"]["name"].lower() != "schemathesis" or manifest["tool"]["version"] != SCHEMATHESIS_VERSION:
        raise ValueError("runtime evidence is not from the pinned Schemathesis version")
    if manifest["input_artifact"]["sha256"] != report_sha256:
        raise ValueError("runtime evidence is not bound to the supplied Schemathesis report")
    if manifest["target"]["origin"] != target_origin or manifest["scope"]["origins"] != [target_origin]:
        raise ValueError("runtime evidence target differs from the Traffic-to-spec schema")
    coverage = manifest["coverage"]
    schema = coverage.get("schema") if isinstance(coverage, dict) else None
    execution = coverage.get("execution") if isinstance(coverage, dict) else None
    if (
        not isinstance(schema, dict)
        or schema.get("artifact_sha256") != schema_sha256
        or schema.get("schema_id") != schema_id
    ):
        raise ValueError("runtime evidence schema binding is missing or inconsistent")
    if (
        not isinstance(execution, dict)
        or execution.get("anonymous") is not True
        or execution.get("stateful") is not False
        or execution.get("wfc") is not False
        or manifest["identity_refs"] != []
    ):
        raise ValueError("v1 bridge accepts only anonymous non-stateful evidence")

    hypotheses = defaultdict(list)
    responses = defaultdict(list)
    source_hypotheses = 0
    for node in bundle["nodes"]:
        data = node["data"]
        if node["kind"] == "hypothesis" and data.get("producer") == "schemathesis":
            source_hypotheses += 1
            if (
                data.get("check") == "not_a_server_error"
                and data.get("failure_type") == "ServerError"
                and data.get("verdict") == "requires_api_lab_controls_and_replay"
            ):
                hypotheses[data.get("case_ref")].append(data)
        elif node["kind"] == "response_observation" and "traffic_id" in data:
            responses[data.get("case_ref")].append(data)

    eligible = {}
    for case_ref, failures in hypotheses.items():
        matching = [response for response in responses.get(case_ref, []) if 500 <= response.get("status", 0) <= 599]
        if len(matching) != 1:
            raise ValueError("eligible Schemathesis hypothesis lacks one unambiguous proxy-confirmed 5xx response")
        eligible[case_ref] = {"failures": failures, "response": matching[0]}
    return source_hypotheses, eligible


def _failure_matches(checks, case_id):
    if not isinstance(checks, list):
        raise ValueError("Schemathesis checks are invalid")
    for check in checks:
        if not isinstance(check, dict) or check.get("status") != "failure":
            continue
        info = check.get("failure_info")
        failure = info.get("failure") if isinstance(info, dict) else None
        if not isinstance(failure, dict):
            continue
        failure_case = failure.get("case_id") if isinstance(failure.get("case_id"), str) else case_id
        if (
            failure_case == case_id
            and check.get("name") == "not_a_server_error"
            and failure.get("type") == "ServerError"
        ):
            return True
    return False


def _proposal(case_id, case_ref, case_node, interaction, checks, source, operation, target_origin):
    if not isinstance(case_node, dict) or not isinstance(case_node.get("value"), dict):
        raise ValueError("Schemathesis case is invalid")
    value = case_node["value"]
    request = interaction.get("request") if isinstance(interaction, dict) else None
    response = interaction.get("response") if isinstance(interaction, dict) else None
    if not isinstance(request, dict) or not isinstance(response, dict):
        raise ValueError("Schemathesis interaction is incomplete")
    if value.get("auth_identity") not in (None, "anonymous"):
        raise ValueError("authenticated case is outside the v1 bridge")
    if value.get("method") != "GET" or request.get("method") != "GET":
        raise ValueError("non-GET case is outside the v1 bridge")
    if "body" in request or request.get("body_size") not in (None, 0):
        raise ValueError("request body is outside the v1 bridge")
    headers = request.get("headers", {})
    if not isinstance(headers, dict) or any(
        isinstance(name, str) and name.lower() in SENSITIVE_HEADER_NAMES for name in headers
    ):
        raise ValueError("credential-bearing request is outside the v1 bridge")
    path = value.get("path")
    uri = request.get("uri")
    if not isinstance(path, str) or not isinstance(uri, str):
        raise ValueError("Schemathesis request path is invalid")
    parsed = urlsplit(uri)
    if origin(uri) != target_origin or parsed.path != path or parsed.fragment:
        raise ValueError("Schemathesis request does not match the selected literal operation")
    if response.get("status_code") != source["response"]["status"]:
        raise ValueError("Schemathesis response status differs from runtime evidence")
    traffic_id = _header_value(response.get("headers"), "x-uroboros-traffic-id", "Schemathesis response")
    if traffic_id != source["response"]["traffic_id"]:
        raise ValueError("Schemathesis response traffic ID differs from runtime evidence")
    if not _failure_matches(checks, case_id):
        raise ValueError("Schemathesis raw case lacks its indexed server-error failure")

    query = value.get("query")
    if not isinstance(query, dict) or not query:
        raise ValueError("server-error case has no structured query input")
    if len(query) > 256:
        raise ValueError("server-error case exceeds the query parameter limit")
    declared = operation["parameters"]
    selected_names = sorted(set(query) & set(declared))
    if not selected_names:
        raise ValueError("server-error case has no schema-declared query input")
    if any(_sensitive_parameter(name) for name in selected_names):
        raise ValueError("server-error case uses a sensitive query parameter")

    candidate_query = {}
    control_query = {}
    uri_pairs = parse_qsl(
        parsed.query,
        keep_blank_values=True,
        strict_parsing=False,
        max_num_fields=1024,
    )
    for name in selected_names:
        candidate = _query_strings(query[name])
        expected_values = candidate if isinstance(candidate, list) else [candidate]
        actual_values = [item for key, item in uri_pairs if key == name]
        if actual_values != expected_values:
            raise ValueError("structured query differs from the recorded request URI")
        candidate_query[name] = candidate
        control_query[name] = _control_value(declared[name])
    if candidate_query == control_query:
        raise ValueError("candidate query does not differ from its deterministic control")

    suffix = case_ref[:12]
    plan = {
        "steps": [
            {
                "name": "positive_control_" + suffix,
                "operation_id": operation["operation_id"],
                "identity": "anonymous",
                "query": control_query,
                "expected_status": operation["expected_status"],
            },
            {
                "name": "server_error_candidate_" + suffix,
                "operation_id": operation["operation_id"],
                "identity": "anonymous",
                "query": candidate_query,
            },
        ],
        "checks": [{
            "name": "schemathesis_server_error_" + suffix,
            "kind": "status",
            "claim": "A schema-declared query variation must not produce a 5xx response when its deterministic positive control succeeds",
            "policy_source": "schemathesis_server_error_with_paired_runtime_control",
            "step": "server_error_candidate_" + suffix,
            "control_step": "positive_control_" + suffix,
            "expected": operation["expected_status"],
        }],
    }
    validate_plan(plan)
    unknown_names = sorted(set(query) - set(declared))
    core = {
        "classification": "draft_experiment",
        "verdict": "requires_operator_review_and_independent_api_lab_replay",
        "security_impact": "unproven_input_handling_or_availability_hypothesis",
        "source": {
            "case_ref": case_ref,
            "traffic_id": traffic_id,
            "status": source["response"]["status"],
            "source_record": source["source_record"],
            "failure_checks": sorted({failure["check"] for failure in source["failures"]}),
            "failure_types": sorted({failure["failure_type"] for failure in source["failures"]}),
            "phase": value.get("meta", {}).get("phase", {}).get("name") if isinstance(value.get("meta"), dict) else None,
        },
        "normalization": {
            "mode": "schema_declared_query_parameters_only",
            "selected_query_parameters": selected_names,
            "undeclared_query_parameters_dropped": len(unknown_names),
            "dropped_parameter_names_sha256": digest(unknown_names),
            "runtime_minimized": False,
        },
        "plan": plan,
        "limitations": [
            "This is an offline draft and has not been executed by API Lab.",
            "The deterministic positive control is generated from the inferred schema and is not a policy oracle.",
            "Dropping undeclared query parameters is a proposed reduction, not a verified minimal reproducer.",
            "A reproducible 5xx response alone does not establish security impact or a confirmed finding.",
        ],
    }
    return {"proposal_id": digest(core), **core}


def build_proposals(
    schema_data,
    schema_sha256,
    report_data,
    report_sha256,
    evidence_data,
    evidence_sha256,
):
    """Build deterministic API Lab draft plans without network or state changes."""
    schema_hash = _sha256(schema_sha256, "schema artifact hash")
    report_hash = _sha256(report_sha256, "Schemathesis report hash")
    evidence_hash = _sha256(evidence_sha256, "runtime evidence hash")
    report = _bytes(report_data, "Schemathesis report", DEFAULT_MAX_REPORT_BYTES)
    if hashlib.sha256(report).hexdigest() != report_hash:
        raise ValueError("Schemathesis report digest mismatch")
    document = loads_executable_traffic_schema(schema_data, schema_hash)
    schema_id = digest(document)
    target_origin = origin(document["servers"][0]["url"])
    evidence = loads_bundle(evidence_data, evidence_hash)
    source_hypotheses, eligible = _evidence_index(
        evidence, report_hash, schema_hash, schema_id, target_origin
    )
    operations = _operation_index(document)

    proposals = []
    ignored = Counter()
    seen = set()
    records = _records(report)
    for line_number, line_hash, record in records:
        event = record.get("ScenarioFinished")
        if not isinstance(event, dict) or not isinstance(event.get("recorder"), dict):
            continue
        recorder = event["recorder"]
        cases = recorder.get("cases", {})
        interactions = recorder.get("interactions", {})
        checks = recorder.get("checks", {})
        if not all(isinstance(item, dict) for item in (cases, interactions, checks)):
            raise ValueError("Schemathesis scenario recorder maps are invalid")
        for case_id, case_node in cases.items():
            if not isinstance(case_id, str):
                raise ValueError("Schemathesis case ID is invalid")
            case_ref = digest(case_id)
            if case_ref not in eligible:
                continue
            if case_ref in seen:
                raise ValueError("eligible Schemathesis case appears more than once")
            seen.add(case_ref)
            value = case_node.get("value") if isinstance(case_node, dict) else None
            path = value.get("path") if isinstance(value, dict) else None
            method = value.get("method") if isinstance(value, dict) else None
            operation = operations.get((path, method))
            if operation is None:
                ignored["unsupported_or_dynamic_operation"] += 1
                continue
            try:
                proposals.append(_proposal(
                    case_id,
                    case_ref,
                    case_node,
                    interactions.get(case_id),
                    checks.get(case_id),
                    {
                        **eligible[case_ref],
                        "source_record": {"line": line_number, "raw_sha256": line_hash},
                    },
                    operation,
                    target_origin,
                ))
            except ValueError as exc:
                reason = str(exc)
                allowed = {
                    "authenticated case is outside the v1 bridge": "authenticated_case",
                    "non-GET case is outside the v1 bridge": "non_get_case",
                    "request body is outside the v1 bridge": "request_body",
                    "credential-bearing request is outside the v1 bridge": "credential_headers",
                    "server-error case has no structured query input": "missing_query",
                    "server-error case exceeds the query parameter limit": "query_parameter_limit",
                    "server-error case has no schema-declared query input": "no_declared_query",
                    "server-error case uses a sensitive query parameter": "sensitive_query_parameter",
                    "query control supports string schemas only": "unsupported_query_schema",
                    "query schema contains a value-bearing or unsupported control keyword": "unsupported_query_schema",
                    "query string length constraints are invalid": "unsupported_query_schema",
                    "query string cannot receive a deterministic control": "unsupported_query_schema",
                    "query value is not a bounded string or string array": "unsupported_query_value",
                    "candidate query does not differ from its deterministic control": "no_control_difference",
                }
                if reason not in allowed:
                    raise
                ignored[allowed[reason]] += 1
    missing = set(eligible) - seen
    if missing:
        raise ValueError("runtime evidence refers to a server-error case absent from the raw report")
    if len(proposals) > MAX_PROPOSALS:
        raise ValueError("Schemathesis report exceeds the proposal limit")
    proposals.sort(key=lambda item: item["proposal_id"])
    result = {
        "contract": CONTRACT,
        "source": {
            "run_id": evidence["manifest"]["run_id"],
            "target_origin": target_origin,
            "target_revision": evidence["manifest"]["target"]["revision"],
            "schema_artifact_sha256": schema_hash,
            "schema_id": schema_id,
            "schemathesis_report_sha256": report_hash,
            "runtime_evidence_sha256": evidence_hash,
            "schemathesis_version": SCHEMATHESIS_VERSION,
        },
        "entries": proposals,
        "summary": {
            "source_hypotheses": source_hypotheses,
            "eligible_server_error_cases": len(eligible),
            "draft_experiments": len(proposals),
            "ignored_reasons": dict(sorted(ignored.items())),
            "claims": {"findings": 0, "confirmed_vulnerabilities": 0},
        },
        "boundaries": dict(BOUNDARIES),
    }
    return result


def dumps_proposals(value):
    if (
        not isinstance(value, dict)
        or set(value) != {"contract", "source", "entries", "summary", "boundaries"}
        or value.get("contract") != CONTRACT
    ):
        raise ValueError("Schemathesis API Lab proposal bundle is invalid")
    if value.get("boundaries") != BOUNDARIES:
        raise ValueError("proposal bundle execution boundaries are invalid")
    source = value.get("source")
    if not isinstance(source, dict) or set(source) != {
        "run_id", "target_origin", "target_revision", "schema_artifact_sha256",
        "schema_id", "schemathesis_report_sha256", "runtime_evidence_sha256",
        "schemathesis_version",
    }:
        raise ValueError("proposal bundle source is invalid")
    if (
        not isinstance(source["run_id"], str)
        or not source["run_id"]
        or not isinstance(source["target_revision"], str)
        or not source["target_revision"]
        or origin(source["target_origin"]) != source["target_origin"]
        or source["schemathesis_version"] != SCHEMATHESIS_VERSION
    ):
        raise ValueError("proposal bundle source values are invalid")
    for key in (
        "schema_artifact_sha256", "schema_id", "schemathesis_report_sha256",
        "runtime_evidence_sha256",
    ):
        if _sha256(source[key], "proposal source " + key) != source[key]:
            raise ValueError("proposal source hash is not canonical")

    summary = value.get("summary")
    if not isinstance(summary, dict) or set(summary) != {
        "source_hypotheses", "eligible_server_error_cases", "draft_experiments",
        "ignored_reasons", "claims",
    }:
        raise ValueError("proposal bundle summary is invalid")
    if summary.get("claims") != {"findings": 0, "confirmed_vulnerabilities": 0}:
        raise ValueError("proposal bundle attempted to promote a claim")
    for key in ("source_hypotheses", "eligible_server_error_cases", "draft_experiments"):
        count = summary.get(key)
        if not isinstance(count, int) or isinstance(count, bool) or count < 0:
            raise ValueError("proposal bundle summary count is invalid")
    ignored = summary.get("ignored_reasons")
    if not isinstance(ignored, dict) or any(
        not isinstance(reason, str)
        or not reason
        or not isinstance(count, int)
        or isinstance(count, bool)
        or count <= 0
        for reason, count in ignored.items()
    ):
        raise ValueError("proposal bundle ignored reasons are invalid")
    entries = value.get("entries")
    if not isinstance(entries, list) or entries != sorted(entries, key=lambda item: item.get("proposal_id", "")):
        raise ValueError("proposal entries are not canonically ordered")
    if (
        summary["source_hypotheses"] < summary["eligible_server_error_cases"]
        or summary["draft_experiments"] != len(entries)
        or summary["eligible_server_error_cases"] != len(entries) + sum(ignored.values())
    ):
        raise ValueError("proposal bundle summary does not match its entries")
    identifiers = set()
    for entry in entries:
        if (
            not isinstance(entry, dict)
            or set(entry) != {
                "proposal_id", "classification", "verdict", "security_impact",
                "source", "normalization", "plan", "limitations",
            }
            or entry.get("classification") != "draft_experiment"
            or entry.get("verdict") != "requires_operator_review_and_independent_api_lab_replay"
            or entry.get("security_impact") != "unproven_input_handling_or_availability_hypothesis"
        ):
            raise ValueError("proposal entry classification is invalid")
        core = {key: item for key, item in entry.items() if key != "proposal_id"}
        if entry.get("proposal_id") != digest(core) or entry["proposal_id"] in identifiers:
            raise ValueError("proposal entry identity is invalid")
        identifiers.add(entry["proposal_id"])
        validate_plan(entry.get("plan"))
    return _canonical(value)


def proposals_sha256(value):
    return hashlib.sha256(dumps_proposals(value)).hexdigest()
