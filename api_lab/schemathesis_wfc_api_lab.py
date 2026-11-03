"""Offline WFC BOLA hypothesis to API Lab private-read draft bridge.

The bridge consumes hash-bound Traffic-to-spec, Schemathesis NDJSON, runtime
evidence, and an explicit operator policy.  It performs no requests and never
submits a plan.  Request bodies and credential values from the WFC report are
inspected only for lineage and are never copied to the output.
"""

from __future__ import annotations

import base64
import binascii
import hashlib
import json
import re
from collections import Counter, defaultdict
from urllib.parse import unquote, urlsplit

from api_lab.engine import digest, origin, validate_plan
from api_lab.runtime_evidence import loads_bundle
from api_lab.schemathesis_shadow import SCHEMATHESIS_VERSION
from api_lab.traffic_schema_projection import loads_executable_traffic_schema


CONTRACT = "uroboros-schemathesis-wfc-api-lab-proposals-v1"
POLICY_CONTRACT = "uroboros-api-lab-private-read-policy-v1"
DEFAULT_MAX_REPORT_BYTES = 128 * 1024 * 1024
DEFAULT_MAX_POLICY_BYTES = 1024 * 1024
DEFAULT_MAX_RECORDS = 100_000
DEFAULT_MAX_LINE_BYTES = 16 * 1024 * 1024
MAX_PROPOSALS = 100
MAX_BODY_BYTES = 1024 * 1024
SHA256 = re.compile(r"[0-9a-fA-F]{64}\Z")
REFERENCE = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}\Z")
TOKEN = re.compile(r"[A-Za-z_][A-Za-z0-9_.-]{0,127}\Z")
BOUNDARIES = {
    "network_requests": 0,
    "model_used": False,
    "automatic_execution": False,
    "database_write": False,
    "mcp_registration": False,
    "planner_visibility": False,
    "vvah_used": False,
}


class _Unsupported(Exception):
    pass


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
            raise ValueError("input contains a duplicate JSON key")
        result[key] = value
    return result


def _reject_constant(value):
    raise ValueError("input contains a non-finite JSON number: " + value)


def _json(data, label):
    try:
        value = json.loads(
            data.decode("utf-8"),
            object_pairs_hook=_duplicate_rejecting_object,
            parse_constant=_reject_constant,
        )
    except UnicodeDecodeError:
        raise ValueError(label + " is not UTF-8") from None
    except json.JSONDecodeError:
        raise ValueError(label + " is invalid JSON") from None
    return value


def _bounded(value, label, maximum=1024):
    if (
        not isinstance(value, str)
        or not value
        or len(value) > maximum
        or any(ord(character) < 32 for character in value)
    ):
        raise ValueError(label + " is invalid")
    return value


def _reference(value, label):
    if not isinstance(value, str) or not REFERENCE.fullmatch(value):
        raise ValueError(label + " is not a safe reference")
    return value


def _escape_pointer_part(value):
    return value.replace("~", "~0").replace("/", "~1")


def _top_level_pointer(value, label):
    if not isinstance(value, str) or not value.startswith("/") or value.count("/") != 1:
        raise ValueError(label + " must be a top-level JSON pointer")
    encoded = value[1:]
    decoded = encoded.replace("~1", "/").replace("~0", "~")
    if (
        not decoded
        or len(decoded) > 128
        or any(ord(character) < 32 for character in decoded)
        or "/" + _escape_pointer_part(decoded) != value
    ):
        raise ValueError(label + " is not a canonical top-level JSON pointer")
    return value


def _pointer(value, selected, label):
    pointer_value = _top_level_pointer(selected, label)
    if not isinstance(value, dict):
        raise ValueError(label + " cannot be read from a non-object response")
    key = pointer_value[1:].replace("~1", "/").replace("~0", "~")
    if key not in value:
        raise ValueError(label + " is absent from the response")
    return value[key]


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


def load_policy(data, expected_sha256, target_origin):
    """Load an explicit, hash-bound private-read policy without inferring policy."""
    raw = _bytes(data, "private-read policy", DEFAULT_MAX_POLICY_BYTES)
    artifact_hash = _sha256(expected_sha256, "private-read policy hash")
    if hashlib.sha256(raw).hexdigest() != artifact_hash:
        raise ValueError("private-read policy digest mismatch")
    document = _json(raw, "private-read policy")
    if not isinstance(document, dict) or set(document) != {"contract", "target_origin", "rules"}:
        raise ValueError("private-read policy has unsupported top-level fields")
    if document.get("contract") != POLICY_CONTRACT:
        raise ValueError("private-read policy contract is unsupported")
    selected_origin = origin(document.get("target_origin"))
    if document["target_origin"].rstrip("/") != selected_origin or selected_origin != target_origin:
        raise ValueError("private-read policy target differs from the schema target")
    rules = document.get("rules")
    if not isinstance(rules, list) or not 1 <= len(rules) <= MAX_PROPOSALS:
        raise ValueError("private-read policy needs between one and %d rules" % MAX_PROPOSALS)

    normalized = []
    identifiers = set()
    bindings = set()
    expected_fields = {
        "rule_id", "producer_operation_id", "read_operation_id", "path_parameter",
        "object_pointer", "principal_pointer", "private_fields", "claim", "policy_source",
    }
    for rule in rules:
        if not isinstance(rule, dict) or set(rule) != expected_fields:
            raise ValueError("private-read policy rule fields are invalid")
        rule_id = _reference(rule["rule_id"], "policy rule ID")
        if rule_id in identifiers:
            raise ValueError("private-read policy rule IDs must be unique")
        identifiers.add(rule_id)
        producer = _bounded(rule["producer_operation_id"], "producer operation ID", 256)
        reader = _bounded(rule["read_operation_id"], "read operation ID", 256)
        parameter = rule["path_parameter"]
        if not isinstance(parameter, str) or not TOKEN.fullmatch(parameter):
            raise ValueError("private-read path parameter is invalid")
        object_pointer = _top_level_pointer(rule["object_pointer"], "object pointer")
        principal_pointer = _top_level_pointer(rule["principal_pointer"], "principal pointer")
        private_fields = rule["private_fields"]
        if (
            not isinstance(private_fields, list)
            or not 1 <= len(private_fields) <= 16
        ):
            raise ValueError("private_fields must contain between one and sixteen pointers")
        selected_private = tuple(sorted({_top_level_pointer(item, "private field") for item in private_fields}))
        if len(selected_private) != len(private_fields):
            raise ValueError("private_fields must not contain duplicates")
        if object_pointer == principal_pointer or object_pointer in selected_private or principal_pointer in selected_private:
            raise ValueError("object, principal, and private pointers must be distinct")
        claim = _bounded(rule["claim"], "private-read claim", 1024)
        policy_source = _bounded(rule["policy_source"], "private-read policy source", 512)
        if policy_source == "model_assumption":
            raise ValueError("private-read policy source must be operator-owned evidence")
        binding = (producer, reader, parameter, object_pointer)
        if binding in bindings:
            raise ValueError("private-read policy contains an ambiguous operation binding")
        bindings.add(binding)
        normalized.append({
            "rule_id": rule_id,
            "producer_operation_id": producer,
            "read_operation_id": reader,
            "path_parameter": parameter,
            "object_pointer": object_pointer,
            "principal_pointer": principal_pointer,
            "private_fields": list(selected_private),
            "claim": claim,
            "policy_source": policy_source,
        })
    normalized.sort(key=lambda item: item["rule_id"])
    semantic = {"contract": POLICY_CONTRACT, "target_origin": selected_origin, "rules": normalized}
    return {
        "artifact_sha256": artifact_hash,
        "policy_id": digest(semantic),
        **semantic,
    }


def _operation_indexes(document):
    by_id = {}
    by_path = {}
    for path, path_item in document["paths"].items():
        for method, operation in path_item.items():
            if method.lower() not in {"get", "post", "put", "patch", "delete", "head", "options"}:
                continue
            operation_id = operation["operationId"]
            descriptor = {
                "operation_id": operation_id,
                "path": path,
                "method": method.upper(),
                "operation": operation,
            }
            if operation_id in by_id or (path, method.upper()) in by_path:
                raise ValueError("schema contains an ambiguous operation")
            by_id[operation_id] = descriptor
            by_path[(path, method.upper())] = descriptor
    return by_id, by_path


def _response_schema(operation, status, label):
    response = operation.get("responses", {}).get(str(status))
    content = response.get("content") if isinstance(response, dict) else None
    media = content.get("application/json") if isinstance(content, dict) else None
    schema = media.get("schema") if isinstance(media, dict) else None
    if not isinstance(schema, dict) or schema.get("type") != "object" or not isinstance(schema.get("properties"), dict):
        raise _Unsupported(label + "_response_schema")
    return schema


def _schema_property(schema, selected, label):
    pointer_value = _top_level_pointer(selected, label)
    key = pointer_value[1:].replace("~1", "/").replace("~0", "~")
    value = schema["properties"].get(key)
    if not isinstance(value, dict) or not isinstance(value.get("type"), str):
        raise ValueError(label + " is not declared in the selected response schema")
    return value


def _string_control(name, schema, role):
    if schema.get("type") != "string" or any(
        key in schema for key in ("default", "enum", "example", "examples", "pattern", "format")
    ):
        raise _Unsupported("unsupported_setup_schema")
    minimum = schema.get("minLength", 0)
    maximum = schema.get("maxLength", 256)
    if (
        not isinstance(minimum, int)
        or isinstance(minimum, bool)
        or not isinstance(maximum, int)
        or isinstance(maximum, bool)
        or minimum < 0
        or maximum < max(minimum, 1)
        or maximum > 4096
    ):
        raise _Unsupported("unsupported_setup_schema")
    marker = "o" if role == "owner" else "p"
    safe_name = re.sub(r"[^A-Za-z0-9]+", "-", name).strip("-").lower() or "field"
    value = "uroboros-%s-%s" % (role, safe_name)
    if len(value) < max(minimum, 1):
        value += marker * (max(minimum, 1) - len(value))
    if len(value) > maximum:
        value = marker * max(minimum, 1)
    if not max(minimum, 1) <= len(value) <= maximum:
        raise _Unsupported("unsupported_setup_schema")
    return value


def _synthetic_bodies(operation):
    request_body = operation.get("requestBody")
    content = request_body.get("content") if isinstance(request_body, dict) else None
    media = content.get("application/json") if isinstance(content, dict) else None
    schema = media.get("schema") if isinstance(media, dict) else None
    properties = schema.get("properties") if isinstance(schema, dict) and schema.get("type") == "object" else None
    if not isinstance(properties, dict) or not 1 <= len(properties) <= 32:
        raise _Unsupported("unsupported_setup_schema")
    owner = {}
    peer = {}
    for name in sorted(properties):
        if not isinstance(name, str) or not name or len(name) > 128 or not isinstance(properties[name], dict):
            raise _Unsupported("unsupported_setup_schema")
        owner[name] = _string_control(name, properties[name], "owner")
        peer[name] = _string_control(name, properties[name], "peer")
    if owner == peer:
        raise _Unsupported("no_distinct_setup_controls")
    return owner, peer


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
    return _reference(value, label + " header")


def _decoded_json_body(content, label):
    if not isinstance(content, dict) or set(content) != {"$base64"} or not isinstance(content["$base64"], str):
        raise ValueError(label + " does not contain one encoded JSON body")
    try:
        raw = base64.b64decode(content["$base64"], validate=True)
    except (binascii.Error, ValueError):
        raise ValueError(label + " body encoding is invalid") from None
    if not raw or len(raw) > MAX_BODY_BYTES:
        raise ValueError(label + " body is empty or exceeds the byte limit")
    value = _json(raw, label + " body")
    if not isinstance(value, dict):
        raise ValueError(label + " body is not a JSON object")
    return value


def _case_identity(case_node):
    value = case_node.get("value") if isinstance(case_node, dict) else None
    if not isinstance(value, dict):
        raise ValueError("Schemathesis case value is invalid")
    identity = value.get("auth_identity")
    return "anonymous" if identity is None else _reference(identity, "Schemathesis identity")


def _bola_failure(checks, case_id):
    matches = []
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
            and check.get("name") == "object_level_authorization"
            and failure.get("type") == "ObjectLevelAuthorizationViolation"
        ):
            matches.append(failure)
    if len(matches) != 1:
        raise ValueError("eligible WFC case lacks one unambiguous BOLA failure")
    return matches[0]


def _single(index, key, label):
    values = index.get(key, [])
    if len(values) != 1:
        raise ValueError(label + " is absent or ambiguous in runtime evidence")
    return values[0]


def _evidence_index(bundle, report_sha256, schema_sha256, schema_id, target_origin):
    manifest = bundle["manifest"]
    if manifest["tool"]["name"].lower() != "schemathesis" or manifest["tool"]["version"] != SCHEMATHESIS_VERSION:
        raise ValueError("runtime evidence is not from the pinned Schemathesis version")
    if manifest["input_artifact"]["sha256"] != report_sha256:
        raise ValueError("runtime evidence is not bound to the supplied Schemathesis report")
    if manifest["target"]["origin"] != target_origin or manifest["scope"]["origins"] != [target_origin]:
        raise ValueError("runtime evidence target differs from the Traffic-to-spec schema")
    coverage = manifest.get("coverage")
    schema = coverage.get("schema") if isinstance(coverage, dict) else None
    execution = coverage.get("execution") if isinstance(coverage, dict) else None
    identities = coverage.get("identities") if isinstance(coverage, dict) else None
    recorder = coverage.get("recording_proxy") if isinstance(coverage, dict) else None
    guard = coverage.get("scope_guard") if isinstance(coverage, dict) else None
    if not isinstance(schema, dict) or schema.get("artifact_sha256") != schema_sha256 or schema.get("schema_id") != schema_id:
        raise ValueError("runtime evidence schema binding is missing or inconsistent")
    if (
        not isinstance(execution, dict)
        or execution.get("anonymous") is not False
        or execution.get("stateful") is not True
        or execution.get("wfc") is not True
        or execution.get("phases") != ["stateful"]
        or "object_level_authorization" not in execution.get("checks", [])
        or execution.get("positive_generation_only") is not True
    ):
        raise ValueError("bridge accepts only authenticated positive stateful WFC evidence")
    identity_refs = manifest.get("identity_refs")
    if (
        not isinstance(identity_refs, list)
        or not 2 <= len(identity_refs) <= 8
        or len(set(identity_refs)) != len(identity_refs)
        or "anonymous" in identity_refs
        or not isinstance(identities, dict)
        or identities.get("refs") != identity_refs
        or identities.get("credentials_archived") is not False
        or not isinstance(identities.get("source_sha256"), str)
    ):
        raise ValueError("runtime evidence identity binding is missing or inconsistent")
    identity_hash = _sha256(identities["source_sha256"], "identity configuration hash")
    if not isinstance(recorder, dict) or recorder.get("confirmed_responses", 0) <= 0:
        raise ValueError("runtime evidence lacks recording-proxy confirmation")
    if (
        not isinstance(guard, dict)
        or guard.get("ready") is not True
        or guard.get("exact_origin") is not True
        or not isinstance(guard.get("mutation_methods"), list)
        or not guard["mutation_methods"]
    ):
        raise ValueError("runtime evidence lacks the state-changing scope guard")
    for key in ("scope_ref", "mutation_scope_ref", "reset_ref"):
        _reference(coverage.get(key), "runtime evidence " + key)
    if bundle["summary"]["claims"].get("findings") != 0:
        raise ValueError("WFC evidence attempted to promote a finding")

    experiments = defaultdict(list)
    requests = defaultdict(list)
    responses = defaultdict(list)
    hypotheses = defaultdict(list)
    source_hypotheses = 0
    nodes = {node["id"]: node for node in bundle["nodes"]}
    for node in bundle["nodes"]:
        data = node["data"]
        case_ref = data.get("case_ref")
        if node["kind"] == "experiment" and isinstance(case_ref, str):
            experiments[case_ref].append(node)
        elif node["kind"] == "request_observation" and isinstance(case_ref, str):
            requests[case_ref].append(node)
        elif node["kind"] == "response_observation" and isinstance(case_ref, str) and "traffic_id" in data:
            responses[case_ref].append(node)
        elif node["kind"] == "hypothesis" and data.get("producer") == "schemathesis":
            source_hypotheses += 1
            if (
                data.get("check") == "object_level_authorization"
                and data.get("failure_type") == "ObjectLevelAuthorizationViolation"
                and data.get("verdict") == "requires_api_lab_controls_and_replay"
                and isinstance(case_ref, str)
            ):
                hypotheses[case_ref].append(node)

    eligible = {}
    for case_ref, failures in hypotheses.items():
        if len(failures) != 1:
            raise ValueError("WFC BOLA hypothesis is ambiguous")
        response = _single(responses, case_ref, "WFC BOLA response")
        if not 200 <= response["data"].get("status", 0) <= 299:
            raise ValueError("WFC BOLA hypothesis is not bound to a successful response")
        linked = any(
            edge["kind"] == "derived_from"
            and edge["source"] == failures[0]["id"]
            and edge["target"] == response["id"]
            for edge in bundle["edges"]
        )
        if not linked:
            raise ValueError("WFC BOLA hypothesis lacks response lineage")
        eligible[case_ref] = {"hypothesis": failures[0], "response": response}
    return {
        "source_hypotheses": source_hypotheses,
        "eligible": eligible,
        "experiments": experiments,
        "requests": requests,
        "responses": responses,
        "identity_refs": tuple(identity_refs),
        "identity_config_sha256": identity_hash,
        "scope_ref": coverage["scope_ref"],
        "mutation_scope_ref": coverage["mutation_scope_ref"],
        "reset_ref": coverage["reset_ref"],
        "exit_reason": manifest["exit_reason"],
        "truncated": manifest["truncated"],
        "target_revision": manifest["target"]["revision"],
        "run_id": manifest["run_id"],
        "nodes": nodes,
    }


def _interaction(recorder, case_id, label):
    interactions = recorder.get("interactions")
    interaction = interactions.get(case_id) if isinstance(interactions, dict) else None
    request = interaction.get("request") if isinstance(interaction, dict) else None
    response = interaction.get("response") if isinstance(interaction, dict) else None
    if not isinstance(request, dict) or not isinstance(response, dict):
        raise ValueError(label + " interaction is incomplete")
    return interaction, request, response


def _bind_case_evidence(case_id, case_node, request, response, source_record, evidence, label):
    case_ref = digest(case_id)
    experiment = _single(evidence["experiments"], case_ref, label + " experiment")
    request_node = _single(evidence["requests"], case_ref, label + " request")
    response_node = _single(evidence["responses"], case_ref, label + " response")
    identity = _case_identity(case_node)
    method = request.get("method")
    status = response.get("status_code")
    traffic_id = _header_value(response.get("headers"), "x-uroboros-traffic-id", label + " response")
    if (
        experiment["data"].get("identity_ref") != identity
        or request_node["data"].get("identity_ref") != identity
        or request_node["data"].get("method") != method
        or response_node["data"].get("status") != status
        or response_node["data"].get("traffic_id") != traffic_id
    ):
        raise ValueError(label + " differs from runtime evidence")
    for node in (experiment, request_node, response_node):
        if node["data"].get("source_record") != source_record:
            raise ValueError(label + " source record differs from runtime evidence")
    return {
        "case_ref": case_ref,
        "identity": identity,
        "method": method,
        "status": status,
        "traffic_id": traffic_id,
        "experiment": experiment,
    }


def _validate_uri(uri, target_origin, expected_path=None):
    if not isinstance(uri, str):
        raise ValueError("Schemathesis request URI is invalid")
    parsed = urlsplit(uri)
    if origin(uri) != target_origin or parsed.query or parsed.fragment:
        raise _Unsupported("query_or_fragment_not_supported")
    if expected_path is not None and parsed.path != expected_path:
        raise ValueError("Schemathesis request path differs from the selected operation")
    return parsed.path


def _path_parameter_value(template, actual, parameter):
    marker = "{" + parameter + "}"
    if template.count(marker) != 1 or template.count("{") != 1 or template.count("}") != 1:
        return None
    prefix, suffix = template.split(marker)
    if not actual.startswith(prefix) or not actual.endswith(suffix) or len(actual) <= len(prefix) + len(suffix):
        return None
    encoded = actual[len(prefix):len(actual) - len(suffix) if suffix else None]
    if "/" in encoded:
        return None
    return unquote(encoded)


def _matching_rule(policy, producer_id, read_id, parameter, object_pointer):
    matches = [
        rule for rule in policy["rules"]
        if rule["producer_operation_id"] == producer_id
        and rule["read_operation_id"] == read_id
        and rule["path_parameter"] == parameter
        and rule["object_pointer"] == object_pointer
    ]
    if not matches:
        raise _Unsupported("missing_policy_rule")
    if len(matches) != 1:
        raise ValueError("private-read policy binding is ambiguous")
    return matches[0]


def _proposal(case_id, case_node, recorder, source_record, source, evidence, document,
              operations_by_id, operations_by_path, policy, target_origin):
    cases = recorder.get("cases")
    checks = recorder.get("checks")
    if not isinstance(cases, dict) or not isinstance(checks, dict):
        raise ValueError("Schemathesis scenario recorder maps are invalid")
    failure = _bola_failure(checks.get(case_id), case_id)
    attack_value = case_node.get("value") if isinstance(case_node, dict) else None
    attack_path = attack_value.get("path") if isinstance(attack_value, dict) else None
    if not isinstance(attack_value, dict) or attack_value.get("method") != "GET" or not isinstance(attack_path, str):
        raise _Unsupported("unsupported_read_operation")
    read_descriptor = operations_by_path.get((attack_path, "GET"))
    if read_descriptor is None:
        raise _Unsupported("unsupported_read_operation")

    owner_id = case_node.get("parent_id")
    owner_case = cases.get(owner_id) if isinstance(owner_id, str) else None
    producer_id = owner_case.get("parent_id") if isinstance(owner_case, dict) else None
    producer_case = cases.get(producer_id) if isinstance(producer_id, str) else None
    if not isinstance(owner_case, dict) or not isinstance(producer_case, dict):
        raise _Unsupported("unsupported_stateful_sequence")
    owner_value = owner_case.get("value")
    producer_value = producer_case.get("value")
    producer_path = producer_value.get("path") if isinstance(producer_value, dict) else None
    if (
        not isinstance(owner_value, dict)
        or owner_value.get("method") != "GET"
        or owner_value.get("path") != attack_path
        or not isinstance(producer_path, str)
        or producer_value.get("method") != "POST"
    ):
        raise _Unsupported("unsupported_stateful_sequence")
    producer_descriptor = operations_by_path.get((producer_path, "POST"))
    if producer_descriptor is None:
        raise _Unsupported("unsupported_producer_operation")

    dependencies = [
        item for item in document["x-uroboros-traffic"]["dependency_candidates"]
        if item["producer_operation_id"] == producer_descriptor["operation_id"]
        and item["consumer_operation_id"] == read_descriptor["operation_id"]
        and item["location"] == "path"
    ]
    if len(dependencies) != 1:
        raise _Unsupported("ambiguous_schema_dependency")
    dependency = dependencies[0]
    parameter_pointer = _top_level_pointer(dependency["parameter_pointer"], "dependency parameter pointer")
    parameter = parameter_pointer[1:].replace("~1", "/").replace("~0", "~")
    object_pointer = _top_level_pointer(dependency["response_pointer"], "dependency response pointer")
    if object_pointer != parameter_pointer or dependency.get("verified") is not False:
        raise _Unsupported("nonsemantic_schema_dependency")
    rule = _matching_rule(
        policy,
        producer_descriptor["operation_id"],
        read_descriptor["operation_id"],
        parameter,
        object_pointer,
    )

    read_parameters = read_descriptor["operation"].get("parameters", [])
    if (
        len(read_parameters) != 1
        or read_parameters[0].get("in") != "path"
        or read_parameters[0].get("name") != parameter
        or read_parameters[0].get("required") is not True
        or producer_descriptor["operation"].get("parameters", [])
    ):
        raise _Unsupported("unsupported_operation_parameters")

    _, producer_request, producer_response = _interaction(recorder, producer_id, "producer")
    _, owner_request, owner_response = _interaction(recorder, owner_id, "owner")
    _, attack_request, attack_response = _interaction(recorder, case_id, "peer")
    producer_bound = _bind_case_evidence(
        producer_id, producer_case, producer_request, producer_response, source_record, evidence, "producer"
    )
    owner_bound = _bind_case_evidence(
        owner_id, owner_case, owner_request, owner_response, source_record, evidence, "owner"
    )
    attack_bound = _bind_case_evidence(
        case_id, case_node, attack_request, attack_response, source_record, evidence, "peer"
    )
    if attack_bound["case_ref"] != source["response"]["data"]["case_ref"]:
        raise ValueError("raw WFC BOLA case differs from runtime evidence")
    if attack_bound["traffic_id"] != source["response"]["data"]["traffic_id"]:
        raise ValueError("raw WFC BOLA traffic ID differs from runtime evidence")
    owner_identity = owner_bound["identity"]
    peer_identity = attack_bound["identity"]
    if (
        producer_bound["identity"] != owner_identity
        or owner_identity not in evidence["identity_refs"]
        or peer_identity not in evidence["identity_refs"]
        or owner_identity == peer_identity
    ):
        raise ValueError("WFC owner and peer identities are not distinct bound identities")
    if (
        producer_bound["method"] != "POST"
        or owner_bound["method"] != "GET"
        or attack_bound["method"] != "GET"
        or not 200 <= producer_bound["status"] <= 299
        or not 200 <= owner_bound["status"] <= 299
        or not 200 <= attack_bound["status"] <= 299
    ):
        raise ValueError("WFC BOLA sequence lacks successful producer, owner, or peer responses")
    if (
        owner_bound["experiment"]["data"].get("parent_case_ref") != producer_bound["case_ref"]
        or attack_bound["experiment"]["data"].get("parent_case_ref") != owner_bound["case_ref"]
    ):
        raise ValueError("WFC parent transition lineage differs from runtime evidence")

    producer_actual = _validate_uri(producer_request.get("uri"), target_origin, producer_descriptor["path"])
    owner_actual = _validate_uri(owner_request.get("uri"), target_origin)
    attack_actual = _validate_uri(attack_request.get("uri"), target_origin)
    selected_path_value = _path_parameter_value(read_descriptor["path"], owner_actual, parameter)
    if (
        selected_path_value is None
        or owner_actual != attack_actual
        or producer_actual != producer_descriptor["path"]
    ):
        raise ValueError("WFC owner and peer requests do not address the same derived object")
    if "body" in owner_request or "body" in attack_request:
        raise _Unsupported("read_request_body_not_supported")

    anonymous_matches = []
    for anonymous_id, anonymous_case in cases.items():
        if (
            not isinstance(anonymous_case, dict)
            or anonymous_case.get("parent_id") != owner_id
            or _case_identity(anonymous_case) != "anonymous"
        ):
            continue
        value = anonymous_case.get("value")
        if not isinstance(value, dict) or value.get("method") != "GET" or value.get("path") != attack_path:
            continue
        _, anonymous_request, anonymous_response = _interaction(recorder, anonymous_id, "anonymous")
        if anonymous_request.get("uri") == attack_request.get("uri"):
            anonymous_matches.append((anonymous_id, anonymous_case, anonymous_request, anonymous_response))
    if len(anonymous_matches) != 1:
        raise _Unsupported("missing_anonymous_denial_control")
    anonymous_id, anonymous_case, anonymous_request, anonymous_response = anonymous_matches[0]
    anonymous_bound = _bind_case_evidence(
        anonymous_id, anonymous_case, anonymous_request, anonymous_response, source_record, evidence, "anonymous"
    )
    if anonymous_bound["status"] not in {401, 403, 404}:
        raise _Unsupported("anonymous_control_not_denied")

    producer_body = _decoded_json_body(producer_response.get("content"), "producer response")
    owner_body = _decoded_json_body(owner_response.get("content"), "owner response")
    attack_body = _decoded_json_body(attack_response.get("content"), "peer response")
    object_value = _pointer(owner_body, rule["object_pointer"], "owner object pointer")
    if isinstance(object_value, (dict, list, bool)) or str(object_value) != selected_path_value:
        raise ValueError("WFC request path does not match the response object identifier")
    selected_pointers = [rule["object_pointer"], rule["principal_pointer"], *rule["private_fields"]]
    for selected in selected_pointers:
        expected = _pointer(owner_body, selected, "owner response pointer")
        if expected in (None, ""):
            raise _Unsupported("empty_policy_field")
        if _pointer(attack_body, selected, "peer response pointer") != expected:
            raise ValueError("WFC BOLA response does not expose the configured object field")
        if _pointer(producer_body, selected, "producer response pointer") != expected:
            raise ValueError("WFC producer response does not match the owner object")

    producer_schema = _response_schema(
        producer_descriptor["operation"], producer_bound["status"], "producer"
    )
    read_schema = _response_schema(read_descriptor["operation"], owner_bound["status"], "read")
    object_property = _schema_property(read_schema, rule["object_pointer"], "object pointer")
    _schema_property(producer_schema, rule["object_pointer"], "producer object pointer")
    _schema_property(read_schema, rule["principal_pointer"], "principal pointer")
    for private_field in rule["private_fields"]:
        _schema_property(read_schema, private_field, "private field")
    parameter_schema = read_parameters[0].get("schema")
    if not isinstance(parameter_schema, dict) or parameter_schema.get("type") != object_property.get("type"):
        raise ValueError("object pointer type differs from the path parameter type")
    owner_setup_body, peer_setup_body = _synthetic_bodies(producer_descriptor["operation"])

    suffix = attack_bound["case_ref"][:12]
    owner_create = "owner_create_" + suffix
    peer_create = "peer_create_" + suffix
    owner_read = "owner_read_" + suffix
    peer_read = "peer_control_read_" + suffix
    plan = {
        "steps": [
            {
                "name": owner_create,
                "operation_id": producer_descriptor["operation_id"],
                "identity": owner_identity,
                "body": owner_setup_body,
                "expected_status": producer_bound["status"],
            },
            {
                "name": peer_create,
                "operation_id": producer_descriptor["operation_id"],
                "identity": peer_identity,
                "body": peer_setup_body,
                "expected_status": producer_bound["status"],
            },
            {
                "name": owner_read,
                "operation_id": read_descriptor["operation_id"],
                "identity": owner_identity,
                "path_parameters": {parameter: {"$from": owner_create + "#" + rule["object_pointer"]}},
                "expected_status": owner_bound["status"],
            },
            {
                "name": peer_read,
                "operation_id": read_descriptor["operation_id"],
                "identity": peer_identity,
                "path_parameters": {parameter: {"$from": peer_create + "#" + rule["object_pointer"]}},
                "expected_status": owner_bound["status"],
            },
        ],
        "checks": [{
            "name": "wfc_private_read_" + suffix,
            "kind": "private_read",
            "claim": rule["claim"],
            "policy_source": rule["policy_source"],
            "owner_step": owner_read,
            "peer_control_step": peer_read,
            "peer_identity": peer_identity,
            "object_pointer": rule["object_pointer"],
            "principal_pointer": rule["principal_pointer"],
            "private_fields": list(rule["private_fields"]),
        }],
    }
    validate_plan(plan)
    if failure.get("operation") != "GET " + read_descriptor["path"]:
        raise ValueError("WFC BOLA failure operation differs from the schema operation")
    core = {
        "classification": "draft_experiment",
        "verdict": "requires_operator_review_and_independent_api_lab_replay",
        "security_impact": "unproven_object_level_authorization_hypothesis",
        "source": {
            "case_ref": attack_bound["case_ref"],
            "owner_case_ref": owner_bound["case_ref"],
            "producer_case_ref": producer_bound["case_ref"],
            "anonymous_case_ref": anonymous_bound["case_ref"],
            "traffic_ids": {
                "producer": producer_bound["traffic_id"],
                "owner": owner_bound["traffic_id"],
                "peer": attack_bound["traffic_id"],
                "anonymous": anonymous_bound["traffic_id"],
            },
            "identities": {"owner": owner_identity, "peer": peer_identity},
            "failure_check": "object_level_authorization",
            "failure_type": "ObjectLevelAuthorizationViolation",
            "source_record": source_record,
        },
        "policy": dict(rule),
        "normalization": {
            "mode": "wfc_create_owner_read_peer_read_to_private_read_controls",
            "producer_operation_id": producer_descriptor["operation_id"],
            "read_operation_id": read_descriptor["operation_id"],
            "dependency_sha256": digest(dependency),
            "schema_dependency_verified": dependency["verified"],
            "wfc_transition_observed": True,
            "source_request_body_copied": False,
            "source_credential_values_copied": False,
            "runtime_minimized": False,
        },
        "plan": plan,
        "limitations": [
            "This is an offline draft and has not been executed by API Lab.",
            "The owner, principal, and private-field policy comes only from the hash-bound operator policy document.",
            "Setup request values are synthetic controls derived from schema shapes; no WFC request value is copied.",
            "The schema dependency remains unverified globally even though this WFC run observed one concrete transition.",
            "The target must be reset or disposable before executing this state-changing draft.",
            "A WFC BOLA signal is not a confirmed finding until API Lab controls and independent replay succeed.",
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
    policy_data,
    policy_sha256,
):
    """Build deterministic private-read API Lab drafts without network or state changes."""
    schema_hash = _sha256(schema_sha256, "schema artifact hash")
    report_hash = _sha256(report_sha256, "Schemathesis report hash")
    evidence_hash = _sha256(evidence_sha256, "runtime evidence hash")
    report = _bytes(report_data, "Schemathesis report", DEFAULT_MAX_REPORT_BYTES)
    if hashlib.sha256(report).hexdigest() != report_hash:
        raise ValueError("Schemathesis report digest mismatch")
    document = loads_executable_traffic_schema(schema_data, schema_hash)
    schema_id = digest(document)
    target_origin = origin(document["servers"][0]["url"])
    policy = load_policy(policy_data, policy_sha256, target_origin)
    evidence_bundle = loads_bundle(evidence_data, evidence_hash)
    evidence = _evidence_index(
        evidence_bundle, report_hash, schema_hash, schema_id, target_origin
    )
    operations_by_id, operations_by_path = _operation_indexes(document)
    for rule in policy["rules"]:
        if rule["producer_operation_id"] not in operations_by_id or rule["read_operation_id"] not in operations_by_id:
            raise ValueError("private-read policy names an operation absent from the schema")

    proposals = []
    ignored = Counter()
    seen = set()
    for line_number, line_hash, record in _records(report):
        event = record.get("ScenarioFinished", record.get("FuzzScenarioFinished"))
        if not isinstance(event, dict) or not isinstance(event.get("recorder"), dict):
            continue
        recorder = event["recorder"]
        cases = recorder.get("cases", {})
        if not isinstance(cases, dict):
            raise ValueError("Schemathesis scenario cases are invalid")
        source_record = {"line": line_number, "raw_sha256": line_hash}
        for case_id, case_node in cases.items():
            if not isinstance(case_id, str):
                raise ValueError("Schemathesis case ID is invalid")
            case_ref = digest(case_id)
            source = evidence["eligible"].get(case_ref)
            if source is None:
                continue
            if case_ref in seen:
                raise ValueError("eligible WFC BOLA case appears more than once")
            seen.add(case_ref)
            try:
                proposals.append(_proposal(
                    case_id,
                    case_node,
                    recorder,
                    source_record,
                    source,
                    evidence,
                    document,
                    operations_by_id,
                    operations_by_path,
                    policy,
                    target_origin,
                ))
            except _Unsupported as exc:
                ignored[str(exc)] += 1
    missing = set(evidence["eligible"]) - seen
    if missing:
        raise ValueError("runtime evidence refers to a WFC BOLA case absent from the raw report")
    if len(proposals) > MAX_PROPOSALS:
        raise ValueError("Schemathesis report exceeds the proposal limit")
    proposals.sort(key=lambda item: item["proposal_id"])
    result = {
        "contract": CONTRACT,
        "source": {
            "run_id": evidence["run_id"],
            "target_origin": target_origin,
            "target_revision": evidence["target_revision"],
            "scope_ref": evidence["scope_ref"],
            "mutation_scope_ref": evidence["mutation_scope_ref"],
            "reset_ref": evidence["reset_ref"],
            "schema_artifact_sha256": schema_hash,
            "schema_id": schema_id,
            "schemathesis_report_sha256": report_hash,
            "runtime_evidence_sha256": evidence_hash,
            "policy_artifact_sha256": policy["artifact_sha256"],
            "policy_id": policy["policy_id"],
            "identity_config_sha256": evidence["identity_config_sha256"],
            "identity_refs": list(evidence["identity_refs"]),
            "schemathesis_version": SCHEMATHESIS_VERSION,
            "evidence_exit_reason": evidence["exit_reason"],
            "evidence_truncated": evidence["truncated"],
        },
        "entries": proposals,
        "summary": {
            "source_hypotheses": evidence["source_hypotheses"],
            "eligible_bola_cases": len(evidence["eligible"]),
            "policy_rules": len(policy["rules"]),
            "draft_experiments": len(proposals),
            "ignored_reasons": dict(sorted(ignored.items())),
            "claims": {"findings": 0, "confirmed_vulnerabilities": 0},
        },
        "boundaries": dict(BOUNDARIES),
    }
    return result


def _validate_proposal_entry(entry, bundle_source):
    entry_source = entry.get("source")
    if not isinstance(entry_source, dict) or set(entry_source) != {
        "case_ref", "owner_case_ref", "producer_case_ref", "anonymous_case_ref",
        "traffic_ids", "identities", "failure_check", "failure_type", "source_record",
    }:
        raise ValueError("WFC proposal entry source is invalid")
    case_refs = [
        entry_source[name]
        for name in ("case_ref", "owner_case_ref", "producer_case_ref", "anonymous_case_ref")
    ]
    if len(set(case_refs)) != 4 or any(_sha256(item, "WFC proposal case reference") != item for item in case_refs):
        raise ValueError("WFC proposal case references are invalid")
    traffic_ids = entry_source.get("traffic_ids")
    if not isinstance(traffic_ids, dict) or set(traffic_ids) != {"producer", "owner", "peer", "anonymous"}:
        raise ValueError("WFC proposal traffic lineage is invalid")
    selected_traffic_ids = [_reference(item, "WFC proposal traffic ID") for item in traffic_ids.values()]
    if len(set(selected_traffic_ids)) != 4:
        raise ValueError("WFC proposal traffic IDs must be distinct")
    identities = entry_source.get("identities")
    if not isinstance(identities, dict) or set(identities) != {"owner", "peer"}:
        raise ValueError("WFC proposal identities are invalid")
    owner = _reference(identities["owner"], "WFC proposal owner identity")
    peer = _reference(identities["peer"], "WFC proposal peer identity")
    if owner == peer or owner not in bundle_source["identity_refs"] or peer not in bundle_source["identity_refs"]:
        raise ValueError("WFC proposal identities are not bound to the source run")
    if (
        entry_source.get("failure_check") != "object_level_authorization"
        or entry_source.get("failure_type") != "ObjectLevelAuthorizationViolation"
    ):
        raise ValueError("WFC proposal failure source is invalid")
    source_record = entry_source.get("source_record")
    if (
        not isinstance(source_record, dict)
        or set(source_record) != {"line", "raw_sha256"}
        or not isinstance(source_record["line"], int)
        or isinstance(source_record["line"], bool)
        or source_record["line"] <= 0
        or _sha256(source_record["raw_sha256"], "WFC proposal source record hash") != source_record["raw_sha256"]
    ):
        raise ValueError("WFC proposal source record is invalid")

    policy = entry.get("policy")
    if not isinstance(policy, dict) or set(policy) != {
        "rule_id", "producer_operation_id", "read_operation_id", "path_parameter",
        "object_pointer", "principal_pointer", "private_fields", "claim", "policy_source",
    }:
        raise ValueError("WFC proposal policy is invalid")
    _reference(policy["rule_id"], "WFC proposal policy rule")
    producer_operation = _bounded(policy["producer_operation_id"], "WFC proposal producer operation", 256)
    read_operation = _bounded(policy["read_operation_id"], "WFC proposal read operation", 256)
    parameter = policy["path_parameter"]
    if not isinstance(parameter, str) or not TOKEN.fullmatch(parameter):
        raise ValueError("WFC proposal path parameter is invalid")
    object_pointer = _top_level_pointer(policy["object_pointer"], "WFC proposal object pointer")
    principal_pointer = _top_level_pointer(policy["principal_pointer"], "WFC proposal principal pointer")
    private_fields = policy["private_fields"]
    if not isinstance(private_fields, list) or not 1 <= len(private_fields) <= 16:
        raise ValueError("WFC proposal private fields are invalid")
    checked_private = [_top_level_pointer(item, "WFC proposal private field") for item in private_fields]
    if checked_private != sorted(set(checked_private)):
        raise ValueError("WFC proposal private fields are not canonical")
    if object_pointer == principal_pointer or object_pointer in checked_private or principal_pointer in checked_private:
        raise ValueError("WFC proposal policy pointers overlap")
    claim = _bounded(policy["claim"], "WFC proposal claim", 1024)
    policy_source = _bounded(policy["policy_source"], "WFC proposal policy source", 512)
    if policy_source == "model_assumption":
        raise ValueError("WFC proposal policy source is not operator owned")

    normalization = entry.get("normalization")
    if not isinstance(normalization, dict) or set(normalization) != {
        "mode", "producer_operation_id", "read_operation_id", "dependency_sha256",
        "schema_dependency_verified", "wfc_transition_observed", "source_request_body_copied",
        "source_credential_values_copied", "runtime_minimized",
    }:
        raise ValueError("WFC proposal normalization is invalid")
    if (
        normalization["mode"] != "wfc_create_owner_read_peer_read_to_private_read_controls"
        or normalization["producer_operation_id"] != producer_operation
        or normalization["read_operation_id"] != read_operation
        or normalization["schema_dependency_verified"] is not False
        or normalization["wfc_transition_observed"] is not True
        or normalization["source_request_body_copied"] is not False
        or normalization["source_credential_values_copied"] is not False
        or normalization["runtime_minimized"] is not False
        or _sha256(normalization["dependency_sha256"], "WFC proposal dependency hash") != normalization["dependency_sha256"]
    ):
        raise ValueError("WFC proposal normalization values are invalid")

    plan = entry.get("plan")
    validate_plan(plan)
    steps = plan.get("steps", [])
    checks = plan.get("checks", [])
    if len(steps) != 4 or len(checks) != 1 or checks[0].get("kind") != "private_read" or "cleanup" in plan:
        raise ValueError("WFC proposal must contain one bounded private_read plan")
    owner_create, peer_create, owner_read, peer_read = steps
    if set(owner_create) != {"name", "operation_id", "identity", "body", "expected_status"}:
        raise ValueError("WFC proposal owner setup is invalid")
    if set(peer_create) != {"name", "operation_id", "identity", "body", "expected_status"}:
        raise ValueError("WFC proposal peer setup is invalid")
    if set(owner_read) != {"name", "operation_id", "identity", "path_parameters", "expected_status"}:
        raise ValueError("WFC proposal owner read is invalid")
    if set(peer_read) != {"name", "operation_id", "identity", "path_parameters", "expected_status"}:
        raise ValueError("WFC proposal peer read is invalid")
    if (
        owner_create["operation_id"] != producer_operation
        or peer_create["operation_id"] != producer_operation
        or owner_read["operation_id"] != read_operation
        or peer_read["operation_id"] != read_operation
        or owner_create["identity"] != owner
        or owner_read["identity"] != owner
        or peer_create["identity"] != peer
        or peer_read["identity"] != peer
        or owner_create["expected_status"] != peer_create["expected_status"]
        or owner_read["expected_status"] != peer_read["expected_status"]
        or any(
            not isinstance(status, int) or isinstance(status, bool) or not 200 <= status <= 299
            for status in (owner_create["expected_status"], owner_read["expected_status"])
        )
    ):
        raise ValueError("WFC proposal step bindings are invalid")
    owner_body = owner_create["body"]
    peer_body = peer_create["body"]
    if (
        not isinstance(owner_body, dict)
        or not owner_body
        or not isinstance(peer_body, dict)
        or set(owner_body) != set(peer_body)
        or owner_body == peer_body
        or any(
            not isinstance(item, str)
            or not item
            or not (item.startswith("uroboros-owner-") or set(item) == {"o"})
            for item in owner_body.values()
        )
        or any(
            not isinstance(item, str)
            or not item
            or not (item.startswith("uroboros-peer-") or set(item) == {"p"})
            for item in peer_body.values()
        )
    ):
        raise ValueError("WFC proposal setup bodies are not synthetic controls")
    if owner_read["path_parameters"] != {
        parameter: {"$from": owner_create["name"] + "#" + object_pointer}
    } or peer_read["path_parameters"] != {
        parameter: {"$from": peer_create["name"] + "#" + object_pointer}
    }:
        raise ValueError("WFC proposal path references are invalid")
    check = checks[0]
    if set(check) != {
        "name", "kind", "claim", "policy_source", "owner_step", "peer_control_step",
        "peer_identity", "object_pointer", "principal_pointer", "private_fields",
    } or (
        check["claim"] != claim
        or check["policy_source"] != policy_source
        or check["owner_step"] != owner_read["name"]
        or check["peer_control_step"] != peer_read["name"]
        or check["peer_identity"] != peer
        or check["object_pointer"] != object_pointer
        or check["principal_pointer"] != principal_pointer
        or check["private_fields"] != checked_private
    ):
        raise ValueError("WFC proposal private_read check differs from policy")
    limitations = entry.get("limitations")
    if not isinstance(limitations, list) or not limitations or any(
        not isinstance(item, str) or not item or len(item) > 1024 for item in limitations
    ):
        raise ValueError("WFC proposal limitations are invalid")


def dumps_proposals(value):
    if (
        not isinstance(value, dict)
        or set(value) != {"contract", "source", "entries", "summary", "boundaries"}
        or value.get("contract") != CONTRACT
        or value.get("boundaries") != BOUNDARIES
    ):
        raise ValueError("WFC API Lab proposal bundle is invalid")
    source = value.get("source")
    expected_source = {
        "run_id", "target_origin", "target_revision", "scope_ref", "mutation_scope_ref", "reset_ref",
        "schema_artifact_sha256", "schema_id", "schemathesis_report_sha256", "runtime_evidence_sha256",
        "policy_artifact_sha256", "policy_id", "identity_config_sha256", "identity_refs",
        "schemathesis_version", "evidence_exit_reason", "evidence_truncated",
    }
    if not isinstance(source, dict) or set(source) != expected_source:
        raise ValueError("WFC proposal source is invalid")
    if (
        origin(source["target_origin"]) != source["target_origin"]
        or source["schemathesis_version"] != SCHEMATHESIS_VERSION
        or not isinstance(source["identity_refs"], list)
        or not 2 <= len(source["identity_refs"]) <= 8
        or len(set(source["identity_refs"])) != len(source["identity_refs"])
        or not isinstance(source["evidence_truncated"], bool)
    ):
        raise ValueError("WFC proposal source values are invalid")
    for identity in source["identity_refs"]:
        _reference(identity, "WFC proposal identity reference")
    for key in ("run_id", "scope_ref", "mutation_scope_ref", "reset_ref"):
        _reference(source[key], "WFC proposal " + key)
    _bounded(source["target_revision"], "WFC proposal target revision")
    _bounded(source["evidence_exit_reason"], "WFC proposal exit reason", 64)
    for key in (
        "schema_artifact_sha256", "schema_id", "schemathesis_report_sha256", "runtime_evidence_sha256",
        "policy_artifact_sha256", "policy_id", "identity_config_sha256",
    ):
        if _sha256(source[key], "WFC proposal " + key) != source[key]:
            raise ValueError("WFC proposal source hash is not canonical")

    summary = value.get("summary")
    if not isinstance(summary, dict) or set(summary) != {
        "source_hypotheses", "eligible_bola_cases", "policy_rules", "draft_experiments",
        "ignored_reasons", "claims",
    }:
        raise ValueError("WFC proposal summary is invalid")
    if summary.get("claims") != {"findings": 0, "confirmed_vulnerabilities": 0}:
        raise ValueError("WFC proposal bundle attempted to promote a claim")
    for key in ("source_hypotheses", "eligible_bola_cases", "policy_rules", "draft_experiments"):
        count = summary.get(key)
        if not isinstance(count, int) or isinstance(count, bool) or count < 0:
            raise ValueError("WFC proposal summary count is invalid")
    ignored = summary.get("ignored_reasons")
    if not isinstance(ignored, dict) or any(
        not isinstance(reason, str)
        or not reason
        or not isinstance(count, int)
        or isinstance(count, bool)
        or count <= 0
        for reason, count in ignored.items()
    ):
        raise ValueError("WFC proposal ignored reasons are invalid")
    entries = value.get("entries")
    if not isinstance(entries, list) or entries != sorted(entries, key=lambda item: item.get("proposal_id", "")):
        raise ValueError("WFC proposal entries are not canonically ordered")
    if (
        summary["source_hypotheses"] < summary["eligible_bola_cases"]
        or summary["draft_experiments"] != len(entries)
        or summary["eligible_bola_cases"] != len(entries) + sum(ignored.values())
    ):
        raise ValueError("WFC proposal summary does not match its entries")
    identifiers = set()
    for entry in entries:
        if (
            not isinstance(entry, dict)
            or set(entry) != {
                "proposal_id", "classification", "verdict", "security_impact", "source",
                "policy", "normalization", "plan", "limitations",
            }
            or entry.get("classification") != "draft_experiment"
            or entry.get("verdict") != "requires_operator_review_and_independent_api_lab_replay"
            or entry.get("security_impact") != "unproven_object_level_authorization_hypothesis"
        ):
            raise ValueError("WFC proposal entry classification is invalid")
        core = {key: item for key, item in entry.items() if key != "proposal_id"}
        if entry.get("proposal_id") != digest(core) or entry["proposal_id"] in identifiers:
            raise ValueError("WFC proposal entry identity is invalid")
        identifiers.add(entry["proposal_id"])
        _validate_proposal_entry(entry, source)
    return _canonical(value)


def proposals_sha256(value):
    return hashlib.sha256(dumps_proposals(value)).hexdigest()
