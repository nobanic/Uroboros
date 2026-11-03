#!/usr/bin/env python3
"""One-artifact, broker-fed LIEF + capa + FLOSS static triage worker."""

from __future__ import annotations

import hashlib
import json
import os
import re
import select
import signal
import socket
import stat
import struct
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path
from typing import Any, Callable, Iterable

WORKER_PROTOCOL_VERSION = 3
ANALYZER = "lief+capa+floss"
ANALYZER_VERSION = "1.0.0+9.4.0+3.2.0"
LIEF_VERSION = "1.0.0"
LIEF_SOURCE_COMMIT = "d05b3499b6934137e917c009a4df0a9dc8cb11c5"
CAPA_VERSION = "9.4.0"
CAPA_SOURCE_COMMIT = "7a79f799a70f052e8382cdbeaec8115daba14a5b"
CAPA_RULES_VERSION = "9.4.0"
CAPA_RULES_COMMIT = "2af9fbfc1c9b4634dbeb76b5d34fca9389fa7f80"
CAPA_RULES_MANIFEST_SHA256 = "1910582fdcc7fd0bff198cfe07f4f1e3ceaf3acbd0c840d52d6507009d2bb5e2"
CAPA_SIGNATURES_MANIFEST_SHA256 = "1b28cbecf167dbf458268ba0c282202d1347f663bba3a6089e0846391f3e083e"
FLOSS_VERSION = "3.2.0"
FLOSS_SOURCE_COMMIT = "69e8ea21ca78deb2418abb68f86c838d1d2672c7"
FLOSS_SIGNATURES_MANIFEST_SHA256 = "1b28cbecf167dbf458268ba0c282202d1347f663bba3a6089e0846391f3e083e"
NORMALIZATION_SCHEMA = "binary-projection-v3"
CAPA_NORMALIZATION_SCHEMA = "capa-candidate-v1"
FLOSS_NORMALIZATION_SCHEMA = "floss-decoded-candidate-v1"
PROFILE_REVISION = "standard-v1"
MAX_ASSIGNMENT_BYTES = 64 << 10
MAX_RESULT_BYTES = (2 << 20) + (64 << 10)
MAX_PROJECTION_BYTES = 2 << 20
MAX_LIEF_PROJECTION_BYTES = 1 << 20
MAX_CAPA_PROJECTION_BYTES = 512 << 10
MAX_CAPA_CANDIDATES = 512
MAX_CAPA_LOCATIONS = 64
MAX_CAPA_STDOUT_BYTES = 16 << 20
MAX_CAPA_STDERR_BYTES = 1 << 20
MAX_FLOSS_ARTIFACT_BYTES = 32 << 20
MAX_FLOSS_PROJECTION_BYTES = 384 << 10
MAX_FLOSS_CANDIDATES = 512
MAX_FLOSS_STRING_CHARACTERS = 2048
MAX_FLOSS_STDOUT_BYTES = 16 << 20
MAX_FLOSS_STDERR_BYTES = 1 << 20
CAPA_RUNNER = "/opt/uroboros-binary-worker/capa_runner.py"
FLOSS_RUNNER = "/opt/uroboros-binary-worker/floss_runner.py"
HASH_PATTERN = re.compile(r"^[0-9a-f]{64}$")
IDENTITY_PATTERN = re.compile(r"^sha256:[0-9a-f]{64}$")
JOB_PATTERN = re.compile(r"^binary-job-[0-9a-f]{32}$")
LEASE_PATTERN = re.compile(r"^lease-[0-9a-f]{32}$")
STANDARD_TOOL_RULE_PINS = {
    "lief": LIEF_VERSION,
    "lief_commit": LIEF_SOURCE_COMMIT,
    "capa": CAPA_VERSION,
    "capa_commit": CAPA_SOURCE_COMMIT,
    "capa_rules": CAPA_RULES_VERSION,
    "capa_rules_commit": CAPA_RULES_COMMIT,
    "capa_rules_manifest_sha256": CAPA_RULES_MANIFEST_SHA256,
    "capa_signatures_manifest_sha256": CAPA_SIGNATURES_MANIFEST_SHA256,
    "floss": FLOSS_VERSION,
    "floss_commit": FLOSS_SOURCE_COMMIT,
    "floss_normalization_schema": FLOSS_NORMALIZATION_SCHEMA,
    "floss_signatures_manifest_sha256": FLOSS_SIGNATURES_MANIFEST_SHA256,
    "normalization_schema": NORMALIZATION_SCHEMA,
    "capa_normalization_schema": CAPA_NORMALIZATION_SCHEMA,
}
STANDARD_LIMIT_PROFILE = {
    "artifact_bytes": 128 << 20,
    "capa_candidates": MAX_CAPA_CANDIDATES,
    "capa_locations_per_candidate": MAX_CAPA_LOCATIONS,
    "capa_projection_bytes": MAX_CAPA_PROJECTION_BYTES,
    "capa_stderr_bytes": MAX_CAPA_STDERR_BYTES,
    "capa_stdout_bytes": MAX_CAPA_STDOUT_BYTES,
    "capa_wall_seconds": 240,
    "exports": 4096,
    "imports": 4096,
    "libraries": 512,
    "lief_projection_bytes": MAX_LIEF_PROJECTION_BYTES,
    "lief_wall_seconds": 60,
    "floss_artifact_bytes": MAX_FLOSS_ARTIFACT_BYTES,
    "floss_candidates": MAX_FLOSS_CANDIDATES,
    "floss_projection_bytes": MAX_FLOSS_PROJECTION_BYTES,
    "floss_stderr_bytes": MAX_FLOSS_STDERR_BYTES,
    "floss_stdout_bytes": MAX_FLOSS_STDOUT_BYTES,
    "floss_string_characters": MAX_FLOSS_STRING_CHARACTERS,
    "floss_wall_seconds": 240,
    "projection_bytes": MAX_PROJECTION_BYTES,
    "sections": 512,
    "wall_seconds": 600,
}


class ProtocolError(RuntimeError):
    pass


def canonical_json(value: Any) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("ascii")


def read_exact(connection: socket.socket, size: int) -> bytes:
    if size < 0:
        raise ProtocolError("negative frame size")
    chunks: list[bytes] = []
    remaining = size
    while remaining:
        chunk = connection.recv(min(1 << 20, remaining))
        if not chunk:
            raise ProtocolError("truncated stream")
        chunks.append(chunk)
        remaining -= len(chunk)
    return b"".join(chunks)


def read_frame(connection: socket.socket, maximum: int) -> dict[str, Any]:
    raw_size = read_exact(connection, 4)
    size = struct.unpack(">I", raw_size)[0]
    if size == 0 or size > maximum:
        raise ProtocolError("frame outside bounds")
    try:
        value = json.loads(read_exact(connection, size))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ProtocolError("invalid JSON frame") from exc
    if not isinstance(value, dict):
        raise ProtocolError("frame must be an object")
    return value


def write_frame(connection: socket.socket, value: dict[str, Any], maximum: int) -> None:
    raw = canonical_json(value)
    if not raw or len(raw) > maximum:
        raise ProtocolError("frame outside bounds")
    connection.sendall(struct.pack(">I", len(raw)) + raw)


def safe_text(value: Any, maximum: int = 512) -> str:
    if value is None:
        return ""
    if hasattr(value, "name"):
        value = value.name
    text = str(value).strip()
    clean = "".join(character for character in text if character.isprintable() and character not in "\r\n\t")
    return clean[:maximum]


def safe_int(value: Any) -> int | None:
    try:
        number = int(value)
    except (TypeError, ValueError, OverflowError):
        return None
    if number < 0 or number > (1 << 64) - 1:
        return None
    return number


def attribute(value: Any, name: str, default: Any = None) -> Any:
    try:
        return getattr(value, name, default)
    except Exception:
        return default


def unique_strings(values: Iterable[Any], maximum: int, truncated: set[str], category: str) -> list[str]:
    result: set[str] = set()
    for examined, value in enumerate(values):
        if examined >= maximum * 4:
            truncated.add(category)
            break
        text = safe_text(value)
        if text:
            result.add(text)
        if len(result) > maximum:
            truncated.add(category)
            break
    return sorted(result)[:maximum]


def project_sections(binary: Any, truncated: set[str]) -> list[dict[str, Any]]:
    result: list[dict[str, Any]] = []
    for section in attribute(binary, "sections", ()) or ():
        if len(result) >= 512:
            truncated.add("sections")
            break
        item: dict[str, Any] = {"name": safe_text(attribute(section, "name", ""), 256)}
        for source, destination in (
            ("offset", "file_offset"),
            ("size", "file_size"),
            ("virtual_address", "virtual_address"),
            ("virtual_size", "virtual_size"),
        ):
            number = safe_int(attribute(section, source))
            if number is not None:
                item[destination] = number
        entropy = attribute(section, "entropy")
        if isinstance(entropy, (int, float)) and 0 <= float(entropy) <= 8:
            item["entropy"] = round(float(entropy), 6)
        result.append(item)
    result.sort(key=lambda item: (item.get("virtual_address", 0), item.get("file_offset", 0), item["name"]))
    return result


def import_names(binary: Any, truncated: set[str]) -> list[str]:
    names: list[str] = []
    generic = attribute(binary, "imported_functions", ()) or ()
    for function in generic:
        names.append(attribute(function, "name", function))
        if len(names) >= 4096:
            truncated.add("imports")
            break
    for library in attribute(binary, "imports", ()) or ():
        library_name = safe_text(attribute(library, "name", ""), 256)
        for entry in attribute(library, "entries", ()) or ():
            entry_name = safe_text(attribute(entry, "name", ""), 256)
            if not entry_name:
                ordinal = safe_int(attribute(entry, "ordinal"))
                entry_name = f"ordinal:{ordinal}" if ordinal is not None else ""
            if entry_name:
                names.append(f"{library_name}!{entry_name}" if library_name else entry_name)
            if len(names) > 4096:
                truncated.add("imports")
                break
        if len(names) > 4096:
            break
    return unique_strings(names, 4096, truncated, "imports")


def export_names(binary: Any, truncated: set[str]) -> list[str]:
    names: list[Any] = []
    for function in attribute(binary, "exported_functions", ()) or ():
        names.append(function)
        if len(names) >= 4096:
            truncated.add("exports")
            break
    export = attribute(binary, "export")
    if export is not None:
        for entry in attribute(export, "entries", ()) or ():
            name = attribute(entry, "name", "")
            if name:
                names.append(name)
            if len(names) >= 4096:
                truncated.add("exports")
                break
    return unique_strings(names, 4096, truncated, "exports")


def build_projection(binary: Any, artifact_sha256: str, byte_size: int) -> tuple[bytes, bool]:
    truncated: set[str] = set()
    summary: dict[str, Any] = {"format": safe_text(attribute(binary, "format", type(binary).__name__), 64)}
    for source, destination in (
        ("entrypoint", "entrypoint"),
        ("imagebase", "image_base"),
        ("virtual_size", "virtual_size"),
        ("original_size", "original_size"),
    ):
        number = safe_int(attribute(binary, source))
        if number is not None:
            summary[destination] = number
    header = attribute(binary, "header")
    if header is not None:
        header_projection: dict[str, Any] = {}
        for source, destination in (
            ("machine_type", "machine_type"),
            ("object_type", "object_type"),
            ("endianness", "endianness"),
        ):
            text = safe_text(attribute(header, source), 128)
            if text:
                header_projection[destination] = text
        if header_projection:
            summary["header"] = header_projection
    projection = {
        "artifact": {"byte_size": byte_size, "sha256": artifact_sha256},
        "binary": summary,
        "exports": export_names(binary, truncated),
        "imports": import_names(binary, truncated),
        "libraries": unique_strings(attribute(binary, "libraries", ()) or (), 512, truncated, "libraries"),
        "profile_revision": PROFILE_REVISION,
        "schema_version": 1,
        "sections": project_sections(binary, truncated),
        "truncated_categories": sorted(truncated),
    }
    raw = canonical_json(projection)
    if len(raw) > MAX_LIEF_PROJECTION_BYTES:
        raise ProtocolError("LIEF projection exceeds hard ceiling")
    return raw, bool(truncated)


def default_parse(path: str) -> Any:
    sys.path.insert(0, "/opt/uroboros-binary-worker/site-packages")
    import lief  # type: ignore[import-not-found]

    logging = attribute(lief, "logging")
    disable = attribute(logging, "disable") if logging is not None else None
    if callable(disable):
        disable()
    return lief.parse(path)


def validate_assignment(value: dict[str, Any], identity: str = "") -> dict[str, Any]:
    required = {
        "version", "job_id", "lease_nonce", "fingerprint", "sha256", "expected_size",
        "profile_revision", "tool_rule_pins", "limit_profile",
    }
    if set(value) != required or value.get("version") != WORKER_PROTOCOL_VERSION:
        raise ProtocolError("invalid assignment fields")
    if not JOB_PATTERN.fullmatch(str(value["job_id"])) or not LEASE_PATTERN.fullmatch(str(value["lease_nonce"])):
        raise ProtocolError("invalid assignment identity")
    if not HASH_PATTERN.fullmatch(str(value["fingerprint"])) or not HASH_PATTERN.fullmatch(str(value["sha256"])):
        raise ProtocolError("invalid assignment hash")
    size = value["expected_size"]
    if (
        not isinstance(size, int)
        or isinstance(size, bool)
        or size <= 0
        or size > STANDARD_LIMIT_PROFILE["artifact_bytes"]
    ):
        raise ProtocolError("invalid artifact size")
    if (
        value["profile_revision"] != PROFILE_REVISION
        or value["tool_rule_pins"] != STANDARD_TOOL_RULE_PINS
        or value["limit_profile"] != STANDARD_LIMIT_PROFILE
    ):
        raise ProtocolError("invalid fixed policy")
    fingerprint_payload = {
        "analyzer": ANALYZER,
        "analyzer_version": ANALYZER_VERSION,
        "blob_sha256": value["sha256"],
        "limit_profile": [
            {"key": key, "value": value["limit_profile"][key]}
            for key in sorted(value["limit_profile"])
        ],
        "profile_revision": PROFILE_REVISION,
        "tool_rule_pins": [
            {"key": key, "value": value["tool_rule_pins"][key]}
            for key in sorted(value["tool_rule_pins"])
        ],
        "worker_image_digest": identity,
    }
    expected_fingerprint = hashlib.sha256(canonical_json(fingerprint_payload)).hexdigest()
    if value["fingerprint"] != expected_fingerprint:
        raise ProtocolError("analysis fingerprint mismatch")
    return value


def receive_artifact(connection: socket.socket, destination: Path, expected_size: int, expected_hash: str) -> None:
    digest = hashlib.sha256()
    remaining = expected_size
    with destination.open("xb") as output:
        os.chmod(destination, 0o600)
        while remaining:
            chunk = connection.recv(min(1 << 20, remaining))
            if not chunk:
                raise ProtocolError("truncated artifact stream")
            output.write(chunk)
            digest.update(chunk)
            remaining -= len(chunk)
        output.flush()
    os.chmod(destination, 0o400)
    if digest.hexdigest() != expected_hash or destination.stat().st_size != expected_size:
        raise ProtocolError("artifact integrity mismatch")


def result_base(assignment: dict[str, Any], identity: str) -> dict[str, Any]:
    return {
        "analyzer": ANALYZER,
        "analyzer_version": ANALYZER_VERSION,
        "artifact_sha256": assignment["sha256"],
        "job_id": assignment["job_id"],
        "lease_nonce": assignment["lease_nonce"],
        "worker_identity": identity,
    }


def failed_stage(analyzer: str, error_class: str, exit_class: str, message: str) -> dict[str, Any]:
    versions = {"lief": LIEF_VERSION, "capa": CAPA_VERSION, "floss": FLOSS_VERSION}
    return {
        "analyzer_version": versions[analyzer],
        "error": message,
        "error_class": error_class,
        "exit_class": exit_class,
        "status": "failed",
        "truncated": False,
    }


def analyze_projection(artifact_path: Path, assignment: dict[str, Any], parser: Callable[[str], Any]) -> dict[str, Any]:
    try:
        binary = parser(str(artifact_path))
        if binary is None:
            projection = canonical_json({
                "artifact": {"byte_size": assignment["expected_size"], "sha256": assignment["sha256"]},
                "profile_revision": PROFILE_REVISION,
                "schema_version": 1,
                "unsupported_reason": "lief_parse_none",
            })
            status = "unsupported"
            truncated = False
        else:
            projection, truncated = build_projection(binary, assignment["sha256"], assignment["expected_size"])
            status = "partial" if truncated else "completed"
        projection_hash = hashlib.sha256(projection).hexdigest()
        return {
            "analyzer_version": LIEF_VERSION,
            "projection_hash": projection_hash,
            "raw_result_hash": projection_hash,
            "record": json.loads(projection),
            "status": status,
            "truncated": truncated,
        }
    except Exception:
        return failed_stage("lief", "lief_projection", "analyzer_failure", "LIEF structural projection failed")


def analyze_with_hard_timeout(
    connection: socket.socket,
    artifact_path: Path,
    assignment: dict[str, Any],
    parser: Callable[[str], Any],
) -> dict[str, Any]:
    # Production is Linux. Keeping a direct fallback makes the pure projection
    # contract unit-testable on Windows without weakening the container path.
    if not hasattr(os, "fork"):
        return analyze_projection(artifact_path, assignment, parser)
    result_path = artifact_path.parent / "lief-stage.json"
    child = os.fork()
    if child == 0:
        try:
            connection.close()
            raw = canonical_json(analyze_projection(artifact_path, assignment, parser))
            if len(raw) > MAX_RESULT_BYTES:
                os._exit(2)
            descriptor = os.open(result_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(descriptor, "wb") as output:
                output.write(raw)
                output.flush()
                os.fsync(output.fileno())
            os._exit(0)
        except BaseException:
            os._exit(2)
    limit_profile = assignment["limit_profile"]
    timeout = int(limit_profile.get("lief_wall_seconds", limit_profile.get("wall_seconds", 60)))
    deadline = time.monotonic() + timeout
    status = None
    while time.monotonic() < deadline:
        waited, status = os.waitpid(child, os.WNOHANG)
        if waited == child:
            break
        time.sleep(0.02)
    else:
        try:
            os.kill(child, 9)
        except ProcessLookupError:
            pass
        os.waitpid(child, 0)
        return failed_stage(
            "lief", "wall_timeout", "resource_limit",
            "LIEF structural projection exceeded the fixed wall-time limit",
        )
    if status is None or not os.WIFEXITED(status) or os.WEXITSTATUS(status) != 0:
        return failed_stage("lief", "worker_child", "analyzer_failure", "LIEF structural projection child failed")
    info = result_path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_size <= 0 or info.st_size > MAX_RESULT_BYTES:
        raise ProtocolError("invalid isolated projection result")
    value = json.loads(result_path.read_bytes())
    if not isinstance(value, dict):
        raise ProtocolError("invalid isolated projection payload")
    return value


def normalize_framework_specs(value: Any, fields: tuple[str, ...]) -> list[dict[str, str]]:
    if isinstance(value, dict):
        value = [value]
    if not isinstance(value, list):
        return []
    result: list[dict[str, str]] = []
    for raw in value[:64]:
        if not isinstance(raw, dict):
            continue
        item = {field: safe_text(raw.get(field), 256) for field in fields}
        item = {key: text for key, text in item.items() if text}
        if item:
            result.append(item)
    result.sort(key=canonical_json)
    unique: list[dict[str, str]] = []
    previous = b""
    for item in result:
        encoded = canonical_json(item)
        if encoded != previous:
            unique.append(item)
            previous = encoded
    return unique[:32]


def normalize_address(value: Any) -> dict[str, Any] | None:
    if not isinstance(value, dict):
        return None
    address_type = safe_text(value.get("type"), 32).lower()
    if address_type not in {"absolute", "relative", "file", "token", "token offset", "no address"}:
        return None
    result: dict[str, Any] = {"type": address_type}
    if address_type == "no address":
        return result
    raw_value = value.get("value")
    number = safe_int(raw_value)
    if number is not None:
        result["value"] = number
        return result
    text = safe_text(raw_value, 128)
    if text:
        result["value"] = text
        return result
    return None


def build_capa_candidate(rule_key: Any, raw_rule: Any, artifact_sha256: str) -> tuple[str, dict[str, Any], bool] | None:
    if not isinstance(raw_rule, dict):
        raise ProtocolError("capa rule result is not an object")
    meta = raw_rule.get("meta")
    matches = raw_rule.get("matches")
    if not isinstance(meta, dict) or not isinstance(matches, list):
        raise ProtocolError("capa rule result is malformed")
    raw_name = safe_text(meta.get("name", rule_key), 4096)
    name = raw_name[:512]
    namespace = safe_text(meta.get("namespace"), 256)
    if not name:
        raise ProtocolError("capa rule name is empty")
    scopes_value = meta.get("scopes")
    scopes: dict[str, str] = {}
    if isinstance(scopes_value, dict):
        for key in ("static", "dynamic"):
            text = safe_text(scopes_value.get(key), 64)
            if text:
                scopes[key] = text
    addresses: list[dict[str, Any]] = []
    for match in matches:
        if not isinstance(match, list) or len(match) != 2:
            raise ProtocolError("capa match result is malformed")
        address = normalize_address(match[0])
        if address is not None:
            addresses.append(address)
    encoded_addresses = sorted({canonical_json(address) for address in addresses})
    locations_truncated = len(encoded_addresses) > MAX_CAPA_LOCATIONS
    locations = [json.loads(raw) for raw in encoded_addresses[:MAX_CAPA_LOCATIONS]]
    identity = b"capa-rule-v1\x00" + name.encode("utf-8") + b"\x00" + namespace.encode("utf-8")
    stable_key = f"capa:rule:{hashlib.sha256(identity).hexdigest()}:{PROFILE_REVISION}"
    rule: dict[str, Any] = {
        "attack": normalize_framework_specs(meta.get("attack"), ("id", "tactic", "technique", "subtechnique")),
        "mbc": normalize_framework_specs(meta.get("mbc"), ("id", "objective", "behavior", "method")),
        "name": name,
        "namespace": namespace,
        "scopes": scopes,
    }
    description = safe_text(meta.get("description"), 512)
    if description:
        rule["description"] = description
    record = {
        "artifact_sha256": artifact_sha256,
        "authority": "candidate",
        "match": {
            "count": len(matches),
            "locations": locations,
            "locations_truncated": locations_truncated,
        },
        "profile_revision": PROFILE_REVISION,
        "rule": rule,
        "schema_version": 1,
        "verified": False,
    }
    if len(canonical_json(record)) > 64 << 10:
        raise ProtocolError("capa candidate exceeds per-record ceiling")
    return stable_key, record, locations_truncated


def normalize_capa(raw: bytes, assignment: dict[str, Any], diagnostics_truncated: bool) -> tuple[dict[str, Any], list[dict[str, Any]], bool]:
    try:
        document = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ProtocolError("capa did not emit one JSON document") from exc
    if not isinstance(document, dict) or set(document) != {"meta", "rules"}:
        raise ProtocolError("capa result document has invalid fields")
    meta = document["meta"]
    rules = document["rules"]
    if not isinstance(meta, dict) or not isinstance(rules, dict) or meta.get("version") != CAPA_VERSION:
        raise ProtocolError("capa result identity is invalid")
    sample = meta.get("sample")
    analysis = meta.get("analysis")
    if (
        not isinstance(sample, dict)
        or sample.get("sha256") != assignment["sha256"]
        or meta.get("flavor") != "static"
        or not isinstance(analysis, dict)
    ):
        raise ProtocolError("capa result is not bound to the assigned artifact")

    candidates: list[tuple[str, dict[str, Any], bool]] = []
    excluded_library = 0
    excluded_internal = 0
    for rule_key, raw_rule in rules.items():
        if not isinstance(raw_rule, dict) or not isinstance(raw_rule.get("meta"), dict):
            raise ProtocolError("capa rule metadata is malformed")
        rule_meta = raw_rule["meta"]
        name = safe_text(rule_meta.get("name", rule_key), 4096)
        if rule_meta.get("lib") is True:
            excluded_library += 1
            continue
        if rule_meta.get("is_subscope_rule") is True or name.startswith("(internal)"):
            excluded_internal += 1
            continue
        candidate = build_capa_candidate(rule_key, raw_rule, assignment["sha256"])
        if candidate is not None:
            candidates.append(candidate)
    candidates.sort(key=lambda item: item[0])

    emitted: list[dict[str, Any]] = []
    used = 4096
    truncated = diagnostics_truncated
    for stable_key, record, record_truncated in candidates:
        encoded = canonical_json(record)
        projected_size = used + len(stable_key) + len(encoded) + 64
        if len(emitted) >= MAX_CAPA_CANDIDATES or projected_size > MAX_CAPA_PROJECTION_BYTES:
            truncated = True
            continue
        emitted.append({"record": record, "stable_key": stable_key, "truncated": record_truncated})
        used = projected_size
        truncated = truncated or record_truncated

    summary = {
        "analysis": {
            "arch": safe_text(analysis.get("arch"), 64),
            "extractor": safe_text(analysis.get("extractor"), 128),
            "format": safe_text(analysis.get("format"), 64),
            "os": safe_text(analysis.get("os"), 64),
        },
        "artifact_sha256": assignment["sha256"],
        "authority": "candidate",
        "candidate_count": len(candidates),
        "diagnostics_truncated": diagnostics_truncated,
        "emitted_count": len(emitted),
        "excluded_internal_rule_count": excluded_internal,
        "excluded_library_rule_count": excluded_library,
        "profile_revision": PROFILE_REVISION,
        "raw_rule_match_count": len(rules),
        "schema_version": 1,
        "truncated": truncated,
        "verified": False,
    }
    total = len(canonical_json(summary)) + sum(
        len(item["stable_key"]) + len(canonical_json(item["record"])) + 64 for item in emitted
    )
    if total > MAX_CAPA_PROJECTION_BYTES:
        raise ProtocolError("normalized capa projection exceeds hard ceiling")
    return summary, emitted, truncated


def connection_closed(connection: socket.socket) -> bool:
    readable, _, _ = select.select([connection], [], [], 0)
    if not readable:
        return False
    value = connection.recv(1, socket.MSG_PEEK)
    if value:
        raise ProtocolError("unexpected data received during analysis")
    return True


def kill_process_group(process: subprocess.Popen[bytes]) -> None:
    if process.poll() is not None:
        return
    try:
        if os.name == "posix":
            os.killpg(process.pid, signal.SIGKILL)
        else:  # pragma: no cover - the production worker is Linux
            process.kill()
    except ProcessLookupError:
        pass


def drain_stream(
    stream: Any,
    maximum: int,
    capture: bytearray,
    digest: Any,
    overflow: threading.Event | None,
) -> None:
    try:
        while True:
            chunk = stream.read(64 << 10)
            if not chunk:
                return
            digest.update(chunk)
            remaining = maximum - len(capture)
            if remaining > 0:
                capture.extend(chunk[:remaining])
            if len(chunk) > remaining and overflow is not None:
                overflow.set()
    finally:
        stream.close()


def unsupported_capa_stage(assignment: dict[str, Any], reason_code: str, raw_hash: str) -> dict[str, Any]:
    summary = {
        "artifact_sha256": assignment["sha256"],
        "authority": "candidate",
        "candidate_count": 0,
        "emitted_count": 0,
        "profile_revision": PROFILE_REVISION,
        "reason_code": reason_code,
        "schema_version": 1,
        "truncated": False,
        "verified": False,
    }
    stage_projection = canonical_json({"candidates": [], "summary": summary})
    return {
        "analyzer_version": CAPA_VERSION,
        "candidates": [],
        "projection_hash": hashlib.sha256(stage_projection).hexdigest(),
        "raw_result_hash": raw_hash,
        "status": "unsupported",
        "summary": summary,
        "truncated": False,
    }


def analyze_capa(artifact_path: Path, assignment: dict[str, Any], connection: socket.socket) -> dict[str, Any]:
    stdout_capture = bytearray()
    stderr_capture = bytearray()
    stdout_digest = hashlib.sha256()
    stderr_digest = hashlib.sha256()
    stdout_overflow = threading.Event()
    stderr_overflow = threading.Event()
    environment = {
        "CAPA_SAVE_WORKSPACE": "0",
        "HOME": "/work",
        "LANG": "C.UTF-8",
        "LD_LIBRARY_PATH": "/usr/local/lib",
        "LOGNAME": "uroboros-worker",
        "PYTHONDONTWRITEBYTECODE": "1",
        "PYTHONUNBUFFERED": "1",
        "USER": "uroboros-worker",
        "XDG_CACHE_HOME": str(artifact_path.parent / "cache"),
    }
    try:
        process = subprocess.Popen(
            [sys.executable, "-I", CAPA_RUNNER, str(artifact_path)],
            cwd=artifact_path.parent,
            env=environment,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
            close_fds=True,
        )
    except Exception:
        return failed_stage("capa", "capa_start", "analyzer_failure", "capa process could not start")
    assert process.stdout is not None and process.stderr is not None
    stdout_thread = threading.Thread(
        target=drain_stream,
        args=(process.stdout, MAX_CAPA_STDOUT_BYTES, stdout_capture, stdout_digest, stdout_overflow),
        daemon=True,
    )
    stderr_thread = threading.Thread(
        target=drain_stream,
        args=(process.stderr, MAX_CAPA_STDERR_BYTES, stderr_capture, stderr_digest, stderr_overflow),
        daemon=True,
    )
    stdout_thread.start()
    stderr_thread.start()
    deadline = time.monotonic() + int(assignment["limit_profile"]["capa_wall_seconds"])
    outcome = ""
    try:
        while process.poll() is None:
            if stdout_overflow.is_set():
                outcome = "output_limit"
                kill_process_group(process)
                break
            if connection_closed(connection):
                outcome = "canceled"
                kill_process_group(process)
                break
            if time.monotonic() >= deadline:
                outcome = "timeout"
                kill_process_group(process)
                break
            time.sleep(0.05)
        process.wait(timeout=5)
    except BaseException:
        kill_process_group(process)
        process.wait(timeout=5)
        raise
    finally:
        stdout_thread.join(5)
        stderr_thread.join(5)
    if stdout_thread.is_alive() or stderr_thread.is_alive():
        return failed_stage("capa", "stream_drain", "analyzer_failure", "capa output drain failed")
    if outcome == "canceled":
        raise ProtocolError("broker disconnected during capa analysis")
    if outcome == "timeout":
        return failed_stage("capa", "wall_timeout", "resource_limit", "capa exceeded the fixed wall-time limit")
    if outcome == "output_limit" or stdout_overflow.is_set():
        return failed_stage("capa", "stdout_limit", "resource_limit", "capa exceeded the fixed output limit")

    raw_hash = hashlib.sha256(canonical_json({
        "stderr": stderr_digest.hexdigest(),
        "stdout": stdout_digest.hexdigest(),
    })).hexdigest()
    diagnostics_truncated = stderr_overflow.is_set()
    if process.returncode != 0:
        if process.returncode in {13, 14, 16, 17, 18, 25, 26}:
            return unsupported_capa_stage(assignment, f"capa_exit_{process.returncode}", raw_hash)
        return failed_stage("capa", "capa_exit", "analyzer_failure", "capa static analysis failed")
    try:
        summary, candidates, truncated = normalize_capa(bytes(stdout_capture).strip(), assignment, diagnostics_truncated)
    except Exception:
        return failed_stage("capa", "capa_normalization", "analyzer_failure", "capa result normalization failed")
    stage_projection = canonical_json({"candidates": candidates, "summary": summary})
    return {
        "analyzer_version": CAPA_VERSION,
        "candidates": candidates,
        "projection_hash": hashlib.sha256(stage_projection).hexdigest(),
        "raw_result_hash": raw_hash,
        "status": "partial" if truncated else "completed",
        "summary": summary,
        "truncated": truncated,
    }


def escape_floss_string(value: Any) -> tuple[str, str, int, bool]:
    if not isinstance(value, str):
        raise ProtocolError("FLOSS decoded string is not text")
    pieces: list[str] = []
    for character in value:
        codepoint = ord(character)
        if character == "\\":
            pieces.append("\\\\")
        elif character.isprintable():
            pieces.append(character)
        elif codepoint <= 0xFFFF:
            pieces.append(f"\\u{codepoint:04x}")
        else:
            pieces.append(f"\\U{codepoint:08x}")
    normalized = "".join(pieces)
    emitted: list[str] = []
    used = 0
    for piece in pieces:
        if used + len(piece) > MAX_FLOSS_STRING_CHARACTERS:
            break
        emitted.append(piece)
        used += len(piece)
    projected = "".join(emitted)
    return projected, hashlib.sha256(normalized.encode("utf-8")).hexdigest(), len(value), projected != normalized


def build_floss_candidate(raw: Any, artifact_sha256: str) -> tuple[str, dict[str, Any], bool]:
    if not isinstance(raw, dict) or set(raw) != {
        "address", "address_type", "decoded_at", "decoding_routine", "encoding", "string",
    }:
        raise ProtocolError("FLOSS decoded-string record is malformed")
    address_type = raw.get("address_type")
    encoding = raw.get("encoding")
    if address_type not in {"STACK", "GLOBAL", "HEAP"} or encoding not in {"ASCII", "UTF-16LE", "UTF-8"}:
        raise ProtocolError("FLOSS decoded-string enum is invalid")
    location: dict[str, int | str] = {"address_type": address_type}
    for field in ("address", "decoded_at", "decoding_routine"):
        number = safe_int(raw.get(field))
        if number is None:
            raise ProtocolError("FLOSS decoded-string address is invalid")
        location[field] = number
    value, value_hash, original_length, value_truncated = escape_floss_string(raw.get("string"))
    decoded_string = {
        "encoding": encoding,
        "original_length": original_length,
        "value": value,
        "value_sha256": value_hash,
        "value_truncated": value_truncated,
    }
    record = {
        "artifact_sha256": artifact_sha256,
        "authority": "candidate",
        "decoded_string": decoded_string,
        "location": location,
        "profile_revision": PROFILE_REVISION,
        "schema_version": 1,
        "verified": False,
    }
    identity = "\x00".join((
        "floss-decoded-v1", value_hash, encoding, address_type,
        str(location["address"]), str(location["decoded_at"]), str(location["decoding_routine"]),
    )).encode("ascii")
    stable_key = f"floss:decoded:{hashlib.sha256(identity).hexdigest()}:{PROFILE_REVISION}"
    if len(canonical_json(record)) > 16 << 10:
        raise ProtocolError("FLOSS candidate exceeds per-record ceiling")
    return stable_key, record, value_truncated


def normalize_floss(
    raw: bytes,
    assignment: dict[str, Any],
    diagnostics_truncated: bool,
) -> tuple[dict[str, Any], list[dict[str, Any]], bool]:
    try:
        document = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ProtocolError("FLOSS did not emit one JSON document") from exc
    if not isinstance(document, dict) or set(document) != {"analysis", "metadata", "strings"}:
        raise ProtocolError("FLOSS result document has invalid fields")
    metadata = document["metadata"]
    analysis = document["analysis"]
    strings = document["strings"]
    if (
        not isinstance(metadata, dict)
        or metadata.get("version") != FLOSS_VERSION
        or not isinstance(analysis, dict)
        or analysis.get("enable_decoded_strings") is not True
        or any(analysis.get(field) is not False for field in (
            "enable_static_strings", "enable_stack_strings", "enable_tight_strings",
        ))
        or not isinstance(strings, dict)
        or not isinstance(strings.get("decoded_strings"), list)
    ):
        raise ProtocolError("FLOSS result identity or fixed policy is invalid")
    functions = analysis.get("functions")
    if not isinstance(functions, dict):
        raise ProtocolError("FLOSS function summary is malformed")
    function_counts: dict[str, int] = {}
    for source, target in (
        ("discovered", "functions_discovered"),
        ("library", "functions_library"),
        ("analyzed_decoded_strings", "functions_analyzed_decoded"),
    ):
        number = safe_int(functions.get(source))
        if number is None:
            raise ProtocolError("FLOSS function count is invalid")
        function_counts[target] = number

    raw_strings = strings["decoded_strings"]
    normalized = [build_floss_candidate(item, assignment["sha256"]) for item in raw_strings]
    normalized.sort(key=lambda item: (item[0], canonical_json(item[1])))
    unique: list[tuple[str, dict[str, Any], bool]] = []
    duplicate_count = 0
    for candidate in normalized:
        if unique and candidate[0] == unique[-1][0]:
            if canonical_json(candidate[1]) != canonical_json(unique[-1][1]):
                raise ProtocolError("FLOSS stable-key collision")
            duplicate_count += 1
            continue
        unique.append(candidate)

    emitted: list[dict[str, Any]] = []
    used = 4096
    truncated = diagnostics_truncated
    for stable_key, record, record_truncated in unique:
        encoded = canonical_json(record)
        projected_size = used + len(stable_key) + len(encoded) + 64
        if len(emitted) >= MAX_FLOSS_CANDIDATES or projected_size > MAX_FLOSS_PROJECTION_BYTES:
            truncated = True
            continue
        emitted.append({"record": record, "stable_key": stable_key, "truncated": record_truncated})
        used = projected_size
        truncated = truncated or record_truncated

    summary = {
        "artifact_sha256": assignment["sha256"],
        "authority": "candidate",
        "candidate_count": len(unique),
        "diagnostics_truncated": diagnostics_truncated,
        "duplicate_count": duplicate_count,
        "emitted_count": len(emitted),
        **function_counts,
        "profile_revision": PROFILE_REVISION,
        "raw_decoded_string_count": len(raw_strings),
        "schema_version": 1,
        "truncated": truncated,
        "verified": False,
    }
    total = len(canonical_json(summary)) + sum(
        len(item["stable_key"]) + len(canonical_json(item["record"])) + 64 for item in emitted
    )
    if total > MAX_FLOSS_PROJECTION_BYTES:
        raise ProtocolError("normalized FLOSS projection exceeds hard ceiling")
    return summary, emitted, truncated


def unavailable_floss_stage(
    assignment: dict[str, Any], status: str, reason_code: str, raw_hash: str = "",
) -> dict[str, Any]:
    if status not in {"unsupported", "resource_gated"}:
        raise ProtocolError("invalid FLOSS admission status")
    summary = {
        "artifact_sha256": assignment["sha256"],
        "authority": "candidate",
        "candidate_count": 0,
        "duplicate_count": 0,
        "emitted_count": 0,
        "profile_revision": PROFILE_REVISION,
        "raw_decoded_string_count": 0,
        "reason_code": reason_code,
        "schema_version": 1,
        "truncated": False,
        "verified": False,
    }
    stage_projection = canonical_json({"candidates": [], "summary": summary})
    return {
        "analyzer_version": FLOSS_VERSION,
        "candidates": [],
        "projection_hash": hashlib.sha256(stage_projection).hexdigest(),
        "raw_result_hash": raw_hash or hashlib.sha256(canonical_json({"admission": reason_code})).hexdigest(),
        "status": status,
        "summary": summary,
        "truncated": False,
    }


def empty_floss_stage(assignment: dict[str, Any], raw_hash: str, diagnostics_truncated: bool) -> dict[str, Any]:
    summary = {
        "artifact_sha256": assignment["sha256"],
        "authority": "candidate",
        "candidate_count": 0,
        "diagnostics_truncated": diagnostics_truncated,
        "duplicate_count": 0,
        "emitted_count": 0,
        "functions_analyzed_decoded": 0,
        "functions_discovered": 0,
        "functions_library": 0,
        "profile_revision": PROFILE_REVISION,
        "raw_decoded_string_count": 0,
        "schema_version": 1,
        "truncated": diagnostics_truncated,
        "verified": False,
    }
    stage_projection = canonical_json({"candidates": [], "summary": summary})
    return {
        "analyzer_version": FLOSS_VERSION,
        "candidates": [],
        "projection_hash": hashlib.sha256(stage_projection).hexdigest(),
        "raw_result_hash": raw_hash,
        "status": "partial" if diagnostics_truncated else "completed",
        "summary": summary,
        "truncated": diagnostics_truncated,
    }


def detect_floss_format(artifact_path: Path) -> str:
    with artifact_path.open("rb") as artifact:
        magic = artifact.read(4)
    if magic.startswith(b"MZ"):
        return "pe"
    if magic == b"\x7fELF":
        return "elf"
    return "unsupported"


def analyze_floss(artifact_path: Path, assignment: dict[str, Any], connection: socket.socket) -> dict[str, Any]:
    if assignment["expected_size"] > assignment["limit_profile"]["floss_artifact_bytes"]:
        return unavailable_floss_stage(assignment, "resource_gated", "artifact_too_large")
    try:
        artifact_format = detect_floss_format(artifact_path)
    except OSError:
        return failed_stage("floss", "artifact_read", "analyzer_failure", "FLOSS admission could not read artifact")
    if artifact_format == "unsupported":
        return unavailable_floss_stage(assignment, "unsupported", "unsupported_format")

    stdout_capture = bytearray()
    stderr_capture = bytearray()
    stdout_digest = hashlib.sha256()
    stderr_digest = hashlib.sha256()
    stdout_overflow = threading.Event()
    stderr_overflow = threading.Event()
    environment = {
        "FLOSS_LOAD_VIV_WORKSPACE": "0",
        "FLOSS_SAVE_WORKSPACE": "0",
        "HOME": "/work",
        "LANG": "C.UTF-8",
        "LD_LIBRARY_PATH": "/usr/local/lib",
        "LOGNAME": "uroboros-worker",
        "PYTHONDONTWRITEBYTECODE": "1",
        "PYTHONUNBUFFERED": "1",
        "USER": "uroboros-worker",
        "XDG_CACHE_HOME": str(artifact_path.parent / "floss-cache"),
    }
    try:
        process = subprocess.Popen(
            [sys.executable, "-I", FLOSS_RUNNER, str(artifact_path)],
            cwd=artifact_path.parent,
            env=environment,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
            close_fds=True,
        )
    except Exception:
        return failed_stage("floss", "floss_start", "analyzer_failure", "FLOSS process could not start")
    assert process.stdout is not None and process.stderr is not None
    stdout_thread = threading.Thread(
        target=drain_stream,
        args=(process.stdout, MAX_FLOSS_STDOUT_BYTES, stdout_capture, stdout_digest, stdout_overflow),
        daemon=True,
    )
    stderr_thread = threading.Thread(
        target=drain_stream,
        args=(process.stderr, MAX_FLOSS_STDERR_BYTES, stderr_capture, stderr_digest, stderr_overflow),
        daemon=True,
    )
    stdout_thread.start()
    stderr_thread.start()
    deadline = time.monotonic() + int(assignment["limit_profile"]["floss_wall_seconds"])
    outcome = ""
    try:
        while process.poll() is None:
            if stdout_overflow.is_set():
                outcome = "output_limit"
                kill_process_group(process)
                break
            if connection_closed(connection):
                outcome = "canceled"
                kill_process_group(process)
                break
            if time.monotonic() >= deadline:
                outcome = "timeout"
                kill_process_group(process)
                break
            time.sleep(0.05)
        process.wait(timeout=5)
    except BaseException:
        kill_process_group(process)
        process.wait(timeout=5)
        raise
    finally:
        stdout_thread.join(5)
        stderr_thread.join(5)
    if stdout_thread.is_alive() or stderr_thread.is_alive():
        return failed_stage("floss", "stream_drain", "analyzer_failure", "FLOSS output drain failed")
    if outcome == "canceled":
        raise ProtocolError("broker disconnected during FLOSS analysis")
    if outcome == "timeout":
        return failed_stage("floss", "wall_timeout", "resource_limit", "FLOSS exceeded the fixed wall-time limit")
    if outcome == "output_limit" or stdout_overflow.is_set():
        return failed_stage("floss", "stdout_limit", "resource_limit", "FLOSS exceeded the fixed output limit")

    raw_hash = hashlib.sha256(canonical_json({
        "stderr": stderr_digest.hexdigest(),
        "stdout": stdout_digest.hexdigest(),
    })).hexdigest()
    diagnostics_truncated = stderr_overflow.is_set()
    if process.returncode != 0:
        return failed_stage("floss", "floss_exit", "analyzer_failure", "FLOSS decoded-string analysis failed")
    if not bytes(stdout_capture).strip():
        return empty_floss_stage(assignment, raw_hash, diagnostics_truncated)
    try:
        summary, candidates, truncated = normalize_floss(
            bytes(stdout_capture).strip(), assignment, diagnostics_truncated,
        )
    except Exception:
        return failed_stage("floss", "floss_normalization", "analyzer_failure", "FLOSS result normalization failed")
    stage_projection = canonical_json({"candidates": candidates, "summary": summary})
    return {
        "analyzer_version": FLOSS_VERSION,
        "candidates": candidates,
        "projection_hash": hashlib.sha256(stage_projection).hexdigest(),
        "raw_result_hash": raw_hash,
        "status": "partial" if truncated else "completed",
        "summary": summary,
        "truncated": truncated,
    }


def public_lief_stage(stage: dict[str, Any]) -> dict[str, Any]:
    result = {
        "analyzer_version": LIEF_VERSION,
        "status": stage["status"],
        "truncated": bool(stage.get("truncated", False)),
    }
    if stage["status"] != "failed":
        result["projection_hash"] = stage["projection_hash"]
        result["record"] = stage["record"]
    return result


def public_capa_stage(stage: dict[str, Any]) -> dict[str, Any]:
    result = {
        "analyzer_version": CAPA_VERSION,
        "candidates": stage.get("candidates", []),
        "status": stage["status"],
        "truncated": bool(stage.get("truncated", False)),
    }
    if stage["status"] != "failed":
        result["projection_hash"] = stage["projection_hash"]
        result["summary"] = stage["summary"]
    return result


def public_floss_stage(stage: dict[str, Any]) -> dict[str, Any]:
    result = {
        "analyzer_version": FLOSS_VERSION,
        "candidates": stage.get("candidates", []),
        "status": stage["status"],
        "truncated": bool(stage.get("truncated", False)),
    }
    if stage["status"] != "failed":
        result["projection_hash"] = stage["projection_hash"]
        result["summary"] = stage["summary"]
    return result


def combine_stages(
    assignment: dict[str, Any],
    lief_stage: dict[str, Any],
    capa_stage: dict[str, Any],
    floss_stage: dict[str, Any],
) -> dict[str, Any]:
    stages = (lief_stage, capa_stage, floss_stage)
    usable = [stage for stage in stages if stage["status"] != "failed"]
    if not usable:
        preferred = next((stage for stage in stages if stage.get("exit_class") == "resource_limit"), lief_stage)
        return {
            "error": "all static analyzers failed",
            "error_class": safe_text(preferred.get("error_class"), 128),
            "exit_class": safe_text(preferred.get("exit_class"), 128),
            "status": "failed",
            "truncated": False,
        }
    statuses = {stage["status"] for stage in stages}
    if statuses == {"completed"}:
        status = "completed"
    elif statuses <= {"unsupported", "resource_gated"}:
        status = "unsupported"
    else:
        status = "partial"
    envelope = {
        "artifact": {"byte_size": assignment["expected_size"], "sha256": assignment["sha256"]},
        "capa": public_capa_stage(capa_stage),
        "floss": public_floss_stage(floss_stage),
        "lief": public_lief_stage(lief_stage),
        "profile_revision": PROFILE_REVISION,
        "schema_version": 3,
    }
    projection = canonical_json(envelope)
    if len(projection) > MAX_PROJECTION_BYTES:
        return {
            "error": "combined projection exceeds hard ceiling",
            "error_class": "projection_limit",
            "exit_class": "resource_limit",
            "status": "failed",
            "truncated": False,
        }
    raw_manifest = canonical_json({
        "capa": capa_stage.get("raw_result_hash", ""),
        "floss": floss_stage.get("raw_result_hash", ""),
        "lief": lief_stage.get("raw_result_hash", ""),
    })
    result: dict[str, Any] = {
        "canonical_projection_hash": hashlib.sha256(projection).hexdigest(),
        "projection": envelope,
        "raw_result_hash": hashlib.sha256(raw_manifest).hexdigest(),
        "status": status,
        "truncated": any(bool(stage.get("truncated")) for stage in stages),
    }
    failed = [stage for stage in stages if stage["status"] == "failed"]
    if failed:
        result.update({
            "error": "one static analyzer failed; retained bounded partial projection",
            "error_class": safe_text(failed[0].get("error_class"), 128),
            "exit_class": "partial_analysis",
        })
    return result


def run_once(
    connection: socket.socket,
    identity: str,
    parser: Callable[[str], Any] = default_parse,
    capa_analyzer: Callable[[Path, dict[str, Any], socket.socket], dict[str, Any]] = analyze_capa,
    floss_analyzer: Callable[[Path, dict[str, Any], socket.socket], dict[str, Any]] = analyze_floss,
) -> None:
    if not IDENTITY_PATTERN.fullmatch(identity):
        raise ProtocolError("worker identity is not a digest")
    write_frame(connection, {
        "analyzer": ANALYZER,
        "analyzer_version": ANALYZER_VERSION,
        "version": WORKER_PROTOCOL_VERSION,
        "worker_identity": identity,
    }, 16 << 10)
    assignment = validate_assignment(read_frame(connection, MAX_ASSIGNMENT_BYTES), identity)
    connection.settimeout(630)
    with tempfile.TemporaryDirectory(prefix="uroboros-binary-", dir="/work") as directory:
        artifact_path = Path(directory) / "artifact.bin"
        receive_artifact(connection, artifact_path, assignment["expected_size"], assignment["sha256"])
        result = result_base(assignment, identity)
        lief_stage = analyze_with_hard_timeout(connection, artifact_path, assignment, parser)
        if connection_closed(connection):
            raise ProtocolError("broker disconnected during LIEF analysis")
        capa_stage = capa_analyzer(artifact_path, assignment, connection)
        if connection_closed(connection):
            raise ProtocolError("broker disconnected during capa analysis")
        floss_stage = floss_analyzer(artifact_path, assignment, connection)
        result.update(combine_stages(assignment, lief_stage, capa_stage, floss_stage))
        write_frame(connection, result, MAX_RESULT_BYTES)


def main() -> int:
    os.umask(0o077)
    socket_path = os.environ.get("UROBOROS_BINARY_WORKER_SOCKET", "").strip()
    identity = os.environ.get("UROBOROS_BINARY_WORKER_IDENTITY", "").strip()
    if not socket_path.startswith("/") or not IDENTITY_PATTERN.fullmatch(identity):
        return 64
    connection = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    connection.settimeout(15)
    try:
        connection.connect(socket_path)
        connection.settimeout(None)
        run_once(connection, identity)
        return 0
    except Exception:
        return 70
    finally:
        connection.close()


if __name__ == "__main__":
    raise SystemExit(main())
