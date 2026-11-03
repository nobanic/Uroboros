"""Offline bridge from proxy-confirmed Katana evidence to Traffic-to-spec.

Katana output supplies discovery provenance and the recording-proxy traffic
identifiers that are safe to select. Request and response bodies come only
from a separate, bounded recorder export. The bridge performs no network,
database, MCP, planner, or model operation.
"""

from __future__ import annotations

import base64
import binascii
import hashlib
import json
import re
from urllib.parse import parse_qsl, urlsplit

from api_lab.engine import origin
from api_lab.runtime_evidence import bundle_sha256, loads_bundle
from api_lab.traffic_schema import METHODS, infer


CAPTURE_CONTRACT = "uroboros-recorded-exchange-v1"
DEFAULT_MAX_ARCHIVE_BYTES = 64 * 1024 * 1024
DEFAULT_MAX_RECORDS = 10_000
DEFAULT_MAX_LINE_BYTES = 10 * 1024 * 1024
DEFAULT_MAX_BODY_BYTES = 4 * 1024 * 1024
REFERENCE = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}\Z")
SHA256 = re.compile(r"[0-9a-fA-F]{64}\Z")
CAPTURE_FIELDS = {
    "contract",
    "run_id",
    "traffic_id",
    "timestamp",
    "method",
    "url",
    "status",
    "request_head_sha256",
    "response_head_sha256",
    "request_body_b64",
    "response_body_b64",
    "response_content_type",
}


def _input_bytes(value, limit, label):
    if not isinstance(value, (bytes, bytearray)):
        raise ValueError(label + " must be bytes")
    raw = bytes(value)
    if not raw:
        raise ValueError(label + " is empty")
    if len(raw) > limit:
        raise ValueError(label + " exceeds the byte limit")
    return raw


def _object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("recorder export contains a duplicate JSON key")
        result[key] = value
    return result


def _constant(_):
    raise ValueError("recorder export contains a non-finite JSON number")


def _reference(value, label):
    if not isinstance(value, str) or not REFERENCE.fullmatch(value):
        raise ValueError(label + " is invalid")
    return value


def _sha256(value, label):
    if not isinstance(value, str) or not SHA256.fullmatch(value):
        raise ValueError(label + " is invalid")
    return value.lower()


def _decode_body(value, label, max_body_bytes):
    if not isinstance(value, str):
        raise ValueError(label + " must be base64 text")
    try:
        raw = base64.b64decode(value, validate=True)
    except (ValueError, binascii.Error):
        raise ValueError(label + " is invalid base64") from None
    if len(raw) > max_body_bytes:
        raise ValueError(label + " exceeds the body limit")
    return raw


def _json_body(raw):
    try:
        return True, json.loads(raw.decode("utf-8"), parse_constant=_constant)
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError):
        return False, None


def _url_basis(value):
    """Return the URL parts retained by the redacted Katana evidence graph."""
    parsed = urlsplit(value)
    if parsed.fragment:
        raise ValueError("recorder URL must not contain a fragment")
    return (
        origin(value),
        parsed.path or "/",
        tuple(name for name, _ in parse_qsl(parsed.query, keep_blank_values=True)),
    )


def _confirmed_exchanges(evidence, selected_ids):
    nodes = {node["id"]: node for node in evidence["nodes"]}
    responses = {}
    for node in evidence["nodes"]:
        if node["kind"] != "response_observation":
            continue
        traffic_id = node["data"].get("traffic_id")
        if traffic_id not in selected_ids:
            continue
        if traffic_id in responses:
            raise ValueError("Katana evidence reuses a traffic_id across response observations")
        responses[traffic_id] = node
    if set(responses) != selected_ids:
        raise ValueError("Katana response evidence and recording-proxy coverage disagree")
    result = {}
    for traffic_id, response in responses.items():
        targets = [
            edge["target"]
            for edge in evidence["edges"]
            if edge["kind"] == "response_to" and edge["source"] == response["id"]
        ]
        if len(targets) != 1:
            raise ValueError("Katana response evidence has invalid request lineage")
        request = nodes.get(targets[0])
        if request is None or request["kind"] != "request_observation":
            raise ValueError("Katana response evidence has invalid request lineage")
        result[traffic_id] = {
            "method": request["data"].get("method"),
            "url_basis": _url_basis(request["data"].get("url", "")),
            "status": response["data"].get("status"),
        }
    return result


def _parse_recorder_export(
    data,
    *,
    max_archive_bytes=DEFAULT_MAX_ARCHIVE_BYTES,
    max_records=DEFAULT_MAX_RECORDS,
    max_line_bytes=DEFAULT_MAX_LINE_BYTES,
    max_body_bytes=DEFAULT_MAX_BODY_BYTES,
):
    for value, label in (
        (max_archive_bytes, "archive byte limit"),
        (max_records, "record limit"),
        (max_line_bytes, "line byte limit"),
        (max_body_bytes, "body byte limit"),
    ):
        if not isinstance(value, int) or isinstance(value, bool) or value <= 0:
            raise ValueError(label + " must be a positive integer")
    raw = _input_bytes(data, max_archive_bytes, "recorder export")
    records = []
    seen = set()
    for number, line in enumerate(raw.splitlines(), 1):
        if not line.strip():
            continue
        if len(records) >= max_records:
            raise ValueError("recorder export exceeds the record limit")
        if len(line) > max_line_bytes:
            raise ValueError("recorder export line exceeds the byte limit")
        try:
            record = json.loads(
                line.decode("utf-8"),
                object_pairs_hook=_object,
                parse_constant=_constant,
            )
        except UnicodeDecodeError:
            raise ValueError("recorder export is not UTF-8") from None
        except json.JSONDecodeError:
            raise ValueError("recorder export line %d is invalid JSON" % number) from None
        if not isinstance(record, dict) or set(record) != CAPTURE_FIELDS:
            raise ValueError("recorder export line %d has invalid fields" % number)
        if record["contract"] != CAPTURE_CONTRACT:
            raise ValueError("recorder export line %d has an unsupported contract" % number)
        record["run_id"] = _reference(record["run_id"], "recorder run_id")
        record["traffic_id"] = _reference(record["traffic_id"], "recorder traffic_id")
        if record["traffic_id"] in seen:
            raise ValueError("recorder export reuses a traffic_id")
        seen.add(record["traffic_id"])
        if not isinstance(record["timestamp"], str) or not record["timestamp"] or len(record["timestamp"]) > 128:
            raise ValueError("recorder timestamp is invalid")
        if not isinstance(record["method"], str) or record["method"].lower() not in METHODS:
            raise ValueError("recorder method is unsupported")
        if not isinstance(record["url"], str) or len(record["url"]) > 8192:
            raise ValueError("recorder URL is invalid")
        if not isinstance(record["status"], int) or isinstance(record["status"], bool) or not 100 <= record["status"] <= 599:
            raise ValueError("recorder status is invalid")
        record["request_head_sha256"] = _sha256(record["request_head_sha256"], "request head hash")
        record["response_head_sha256"] = _sha256(record["response_head_sha256"], "response head hash")
        if not isinstance(record["response_content_type"], str) or len(record["response_content_type"]) > 512:
            raise ValueError("response content type is invalid")
        record["request_body"] = _decode_body(record.pop("request_body_b64"), "request body", max_body_bytes)
        record["response_body"] = _decode_body(record.pop("response_body_b64"), "response body", max_body_bytes)
        records.append(record)
    if not records:
        raise ValueError("recorder export contains no exchanges")
    return raw, records


def infer_from_katana(
    evidence_data,
    recorder_data,
    *,
    expected_evidence_sha256=None,
    expected_recorder_sha256=None,
    path_templates=None,
    max_archive_bytes=DEFAULT_MAX_ARCHIVE_BYTES,
    max_records=DEFAULT_MAX_RECORDS,
    max_line_bytes=DEFAULT_MAX_LINE_BYTES,
    max_body_bytes=DEFAULT_MAX_BODY_BYTES,
):
    """Infer OpenAPI only from recorder exchanges selected by Katana traffic IDs."""
    if expected_evidence_sha256 is None:
        raise ValueError("expected Katana evidence SHA-256 is required")
    if expected_recorder_sha256 is None:
        raise ValueError("expected recorder export SHA-256 is required")
    expected_recorder_sha256 = _sha256(expected_recorder_sha256, "recorder export hash")
    evidence = loads_bundle(evidence_data, expected_evidence_sha256)
    manifest = evidence["manifest"]
    if manifest["tool"]["name"] != "katana":
        raise ValueError("runtime evidence is not a Katana bundle")
    if manifest["exit_reason"] != "completed" or manifest["truncated"]:
        raise ValueError("Katana evidence must be complete before Traffic-to-spec inference")
    if manifest["scope"]["origins"] != [manifest["target"]["origin"]]:
        raise ValueError("Katana evidence must use one exact target origin")
    if manifest["identity_refs"]:
        raise ValueError("the anonymous Katana bridge does not accept identity references")
    run_id = _reference(manifest["run_id"], "Katana run_id")
    coverage = manifest.get("coverage", {})
    if coverage.get("mode") not in {"standard", "headless"}:
        raise ValueError("Katana evidence mode is invalid")
    recording = coverage.get("recording_proxy")
    if not isinstance(recording, dict):
        raise ValueError("Katana evidence lacks recording-proxy coverage")
    traffic_ids = recording.get("traffic_ids")
    if not isinstance(traffic_ids, list) or not traffic_ids:
        raise ValueError("Katana evidence lacks proxy-confirmed traffic IDs")
    selected_ids = {_reference(value, "Katana traffic_id") for value in traffic_ids}
    confirmed = recording.get("confirmed_responses")
    if (
        len(selected_ids) != len(traffic_ids)
        or not isinstance(confirmed, int)
        or isinstance(confirmed, bool)
        or confirmed != len(selected_ids)
    ):
        raise ValueError("Katana recording-proxy coverage is inconsistent")
    confirmed_exchanges = _confirmed_exchanges(evidence, selected_ids)

    raw, records = _parse_recorder_export(
        recorder_data,
        max_archive_bytes=max_archive_bytes,
        max_records=max_records,
        max_line_bytes=max_line_bytes,
        max_body_bytes=max_body_bytes,
    )
    recorder_sha256 = hashlib.sha256(raw).hexdigest()
    if recorder_sha256 != expected_recorder_sha256:
        raise ValueError("recorder export digest mismatch")
    target_origin = manifest["target"]["origin"]
    selected = {}
    for record in records:
        if record["run_id"] != run_id:
            raise ValueError("recorder export mixes Katana run IDs")
        if origin(record["url"]) != target_origin:
            raise ValueError("recorder export contains an exchange outside the Katana target origin")
        if record["traffic_id"] not in selected_ids:
            continue
        expected = confirmed_exchanges[record["traffic_id"]]
        if (
            record["method"].upper() != expected["method"]
            or record["status"] != expected["status"]
            or _url_basis(record["url"]) != expected["url_basis"]
        ):
            raise ValueError("recorder exchange disagrees with Katana request/response evidence")
        request_json_present, request_json = _json_body(record["request_body"])
        response_json_present, response_json = _json_body(record["response_body"])
        selected[record["traffic_id"]] = {
            "exchange_id": record["traffic_id"],
            "timestamp": record["timestamp"],
            "method": record["method"],
            "url": record["url"],
            "status": record["status"],
            "request_json_present": request_json_present,
            "request_json": request_json,
            "response_json_present": response_json_present,
            "response_json": response_json,
            "response_content_type": record["response_content_type"],
            "request_body_sha256": hashlib.sha256(record["request_body"]).hexdigest(),
            "response_body_sha256": hashlib.sha256(record["response_body"]).hexdigest(),
            "request_head_sha256": record["request_head_sha256"],
            "response_head_sha256": record["response_head_sha256"],
            "identity_ref": "anonymous",
        }
    missing = sorted(selected_ids - set(selected))
    if missing:
        raise ValueError("recorder export is missing %d Katana-confirmed exchanges" % len(missing))
    guard_allowed = coverage.get("scope_guard", {}).get("allowed")
    if not isinstance(guard_allowed, int) or isinstance(guard_allowed, bool) or guard_allowed <= 0:
        raise ValueError("Katana scope-guard allowed count is invalid")
    if len(records) > guard_allowed:
        raise ValueError("recorder export count exceeds scope-guard allowed traffic")
    source = {
        "kind": "katana_recording_proxy_export",
        "selection": "katana_response_traffic_ids",
        "katana_run_id": run_id,
        "katana_mode": coverage["mode"],
        "katana_evidence_sha256": bundle_sha256(evidence),
        "recorder_export_sha256": recorder_sha256,
        "recorder_export_records": len(records),
        "guard_allowed_transactions": guard_allowed,
        "guard_allowed_without_durable_record": guard_allowed - len(records),
        "selected_exchanges": len(selected),
        "ignored_proxy_only_exchanges": len(records) - len(selected),
        "identity_basis": "anonymous_shadow_crawl",
    }
    return infer(target_origin, list(selected.values()), path_templates, source)
