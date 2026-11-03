"""Backend-only Arjun GET query-parameter discovery contract.

The model never supplies this envelope. Uroboros resolves one already verified
endpoint asset, freezes the fixed profile and calls the sidecar through the
authenticated backend MCP connection. Arjun remains an unmodified child
process; this module owns its narrow command and sanitized output projection.
"""

import hashlib
import hmac
import importlib.util
import json
import os
import re
import stat
import unicodedata
from pathlib import Path
from urllib.parse import urlparse

from arsenal import contracts as c
from arsenal import taskrun


PROFILE_REVISION = "arjun-get-query-v1"
TOOL_VERSION = "2.2.7"
ARTIFACT_SHA256 = "b193cdaf97bf7b0e8cd91a41da778639e01fd9738d5f666a8161377f475ce72e"
BUDGET_SECONDS = 300
MAX_REQUESTS = 256
THREADS = 1
RATE_LIMIT = 5
REQUEST_TIMEOUT_SECONDS = 10
CHUNK_SIZE = 500
MAX_PARAMETER_BYTES = 128
MAX_PARAMETERS = 256
MAX_OUTPUT_BYTES = 1024 * 1024

_SHA256 = re.compile(r"^[0-9a-f]{64}$")
_PROFILE_FIELDS = {
    "profile_revision", "endpoint_id", "method", "budget_seconds", "max_requests",
    "threads", "rate_limit", "request_timeout_seconds", "chunk_size",
    "artifact_sha256", "wordlist_sha256", "special_sha256", "dependency_lock_sha256",
    "exact_endpoint_sha256", "route_sha256",
}


def data_paths():
    """Return immutable package data and the image dependency lock path."""
    spec = importlib.util.find_spec("arjun")
    if spec is None or not spec.origin:
        return {"wordlist": "", "special": "", "dependency_lock": "/opt/arsenal/requirements.lock"}
    root = Path(spec.origin).resolve().parent
    return {
        "wordlist": str(root / "db" / "large.txt"),
        "special": str(root / "db" / "special.json"),
        "dependency_lock": "/opt/arsenal/requirements.lock",
    }


def _hash_file(path):
    source = Path(path)
    if not path or not source.is_file() or source.is_symlink():
        return "unavailable"
    digest = hashlib.sha256()
    with source.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def provenance(paths=None):
    paths = dict(paths or data_paths())
    return {
        "wordlist_sha256": _hash_file(paths.get("wordlist", "")),
        "special_sha256": _hash_file(paths.get("special", "")),
        "dependency_lock_sha256": _hash_file(paths.get("dependency_lock", "")),
    }


def _canonical_endpoint(raw):
    value = str(raw or "").strip()
    parsed = urlparse(value)
    if parsed.scheme not in ("http", "https") or not parsed.hostname:
        raise ValueError("Arjun endpoint must be an absolute HTTP(S) URL")
    if parsed.username is not None or parsed.password is not None:
        raise ValueError("Arjun endpoint must not contain user information")
    if parsed.query or parsed.fragment or "?" in value or "#" in value:
        raise ValueError("Arjun endpoint must not contain a query or fragment")
    try:
        parsed.port
    except ValueError as exc:
        raise ValueError("Arjun endpoint has an invalid port") from exc
    if any(ord(ch) < 32 or ch.isspace() for ch in value):
        raise ValueError("Arjun endpoint contains whitespace or controls")
    path = parsed.path or "/"
    lowered = path.lower()
    if "%2f" in lowered or "%5c" in lowered or "\\" in path:
        raise ValueError("Arjun endpoint path contains an encoded separator")
    if any(segment in (".", "..") for segment in path.split("/")):
        raise ValueError("Arjun endpoint path contains a dot segment")
    return value


def _positive_integer(value, name):
    if isinstance(value, bool) or not isinstance(value, int) or value <= 0:
        raise ValueError("%s must be a positive integer" % name)
    return value


def _validate_profile(config, endpoint_id, paths):
    if set(config) != _PROFILE_FIELDS:
        raise ValueError("Arjun profile fields do not match the fixed contract")
    fixed = {
        "profile_revision": PROFILE_REVISION,
        "endpoint_id": endpoint_id,
        "method": "GET",
        "budget_seconds": BUDGET_SECONDS,
        "max_requests": MAX_REQUESTS,
        "threads": THREADS,
        "rate_limit": RATE_LIMIT,
        "request_timeout_seconds": REQUEST_TIMEOUT_SECONDS,
        "chunk_size": CHUNK_SIZE,
        "artifact_sha256": ARTIFACT_SHA256,
    }
    for key, expected in fixed.items():
        if config.get(key) != expected or isinstance(config.get(key), bool) != isinstance(expected, bool):
            raise ValueError("Arjun fixed profile mismatch")
    hashes = provenance(paths)
    for key, expected in hashes.items():
        value = config.get(key)
        if expected == "unavailable" or value != expected or not _SHA256.fullmatch(str(value or "")):
            raise ValueError("Arjun immutable data provenance mismatch")
    for key in ("exact_endpoint_sha256", "route_sha256"):
        if not _SHA256.fullmatch(str(config.get(key) or "")):
            raise ValueError("Arjun network policy fingerprint is invalid")


def parse_contract(task_id, run_id, endpoint_id, target, scope_json, expected_scope_hash,
                   config_json, expected_config_hash, proxy_url, paths=None):
    _positive_integer(task_id, "task_id")
    _positive_integer(endpoint_id, "endpoint_id")
    if not taskrun._RUN_ID.fullmatch(str(run_id or "")):
        raise ValueError("run_id is invalid")
    try:
        scope = taskrun.normalize_scope(json.loads(scope_json))
        config = json.loads(config_json)
    except (json.JSONDecodeError, TypeError) as exc:
        raise ValueError("Arjun scope/config JSON is invalid") from exc
    if not isinstance(config, dict):
        raise ValueError("Arjun config must be an object")
    target = _canonical_endpoint(taskrun.validate_task_target(target, scope))
    actual_scope_hash = taskrun.scope_hash(scope)
    if not expected_scope_hash or not hmac.compare_digest(actual_scope_hash, str(expected_scope_hash)):
        raise ValueError("frozen scope hash mismatch")
    paths = dict(paths or data_paths())
    _validate_profile(config, endpoint_id, paths)
    actual_config_hash = taskrun.config_hash("arjun", target, config)
    if not expected_config_hash or not hmac.compare_digest(actual_config_hash, str(expected_config_hash)):
        raise ValueError("Arjun config hash mismatch")
    return {
        "task_id": task_id, "run_id": str(run_id), "endpoint_id": endpoint_id,
        "tool": "arjun", "target": target, "scope": scope,
        "scope_hash": actual_scope_hash, "config": config,
        "config_hash": actual_config_hash,
        "proxy_url": taskrun.validate_proxy_url(proxy_url), "paths": paths,
    }


def build_command(contract, runtime_dir):
    runtime = Path(runtime_dir).resolve()
    if not runtime.is_dir():
        raise ValueError("Arjun runtime directory is unavailable")
    output = runtime / "arjun-output.json"
    wordlist = Path(contract["paths"]["wordlist"])
    special = Path(contract["paths"]["special"])
    if not wordlist.is_file() or wordlist.is_symlink() or not special.is_file() or special.is_symlink():
        raise ValueError("Arjun immutable data files are unavailable")
    argv = [
        "arjun", "-u", contract["target"], "-m", "GET", "-oJ", str(output),
        "-t", str(THREADS), "--rate-limit", str(RATE_LIMIT),
        "-T", str(REQUEST_TIMEOUT_SECONDS), "-c", str(CHUNK_SIZE),
        "--disable-redirects", "-w", str(wordlist),
    ]
    return argv, BUDGET_SECONDS, str(output)


def sanitize_parameter(raw):
    if not isinstance(raw, str):
        raise ValueError("Arjun parameter name must be text")
    value = unicodedata.normalize("NFC", raw)
    if value != raw or not value or value != value.strip() or len(value.encode("utf-8")) > MAX_PARAMETER_BYTES:
        raise ValueError("Arjun parameter name is empty, non-canonical or too long")
    depth = 0
    base_seen = False
    suffix_started = False
    for char in value:
        if char == "[":
            if not base_seen or depth != 0:
                raise ValueError("Arjun parameter brackets are invalid")
            depth = 1
            suffix_started = True
        elif char == "]":
            if depth != 1:
                raise ValueError("Arjun parameter brackets are invalid")
            depth = 0
        elif char.isalnum() or char in "_.-":
            if suffix_started and depth == 0:
                raise ValueError("Arjun parameter brackets are invalid")
            base_seen = True
        else:
            raise ValueError("Arjun parameter contains a forbidden character")
    if depth != 0 or not base_seen:
        raise ValueError("Arjun parameter brackets are invalid")
    return value


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("Arjun JSON contains duplicate object keys")
        result[key] = value
    return result


def parse_output(output_path, target):
    path = Path(output_path)
    try:
        info = path.lstat()
    except FileNotFoundError:
        return []
    if not stat.S_ISREG(info.st_mode) or path.is_symlink() or info.st_size > MAX_OUTPUT_BYTES:
        raise ValueError("Arjun JSON output is not a bounded regular file")
    try:
        value = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=_unique_object)
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise ValueError("Arjun JSON output is malformed") from exc
    if not isinstance(value, dict) or len(value) > 1:
        raise ValueError("Arjun JSON output must contain at most one endpoint")
    if not value:
        return []
    if list(value) != [target]:
        raise ValueError("Arjun JSON output does not match the admitted endpoint")
    item = value[target]
    if not isinstance(item, dict) or set(item) != {"params", "method", "headers"}:
        raise ValueError("Arjun JSON endpoint record has an invalid schema")
    if item["method"] != "GET" or not isinstance(item["headers"], dict) or not isinstance(item["params"], list):
        raise ValueError("Arjun JSON endpoint record violates the GET contract")
    if len(item["params"]) > MAX_PARAMETERS:
        raise ValueError("Arjun JSON contains too many parameters")
    parameters = [sanitize_parameter(raw) for raw in item["params"]]
    if len(set(parameters)) != len(parameters):
        raise ValueError("Arjun JSON contains duplicate parameters")
    return sorted(parameters)


def records(endpoint_id, parameters):
    return [{
        "tool": "arjun", "type": "parameter", "name": parameter,
        "endpoint_id": endpoint_id, "method": "GET", "location": "query",
        "parameter": parameter,
    } for parameter in parameters]


def output(contract, result, output_path, metadata):
    parameters = []
    parse_error = False
    if os.path.exists(output_path):
        try:
            parameters = parse_output(output_path, contract["target"])
        except ValueError:
            parse_error = True
    state = result["exit_state"]
    stdout = str(result.get("stdout", ""))
    if state == c.OK:
        if parse_error:
            state = c.PARSE_ERROR
        elif "Webpage is returning different content on each request. Skipping." in stdout or " due to errors" in stdout:
            state = c.PARTIAL
            parameters = []
        elif parameters:
            state = c.OK
        elif "No parameters were discovered." in stdout:
            state = c.OK_EMPTY
        else:
            state = c.PARSE_ERROR
    elif parse_error:
        parameters = []
    stats = {
        "truncated": bool(result.get("truncated")),
        "stdout_present": bool(stdout.strip()),
        "stderr_present": bool(str(result.get("stderr", "")).strip()),
        "output_present": os.path.exists(output_path),
        "output_parse_error": parse_error,
        "parameters_parsed": len(parameters),
        "profile_revision": PROFILE_REVISION,
        "provenance": metadata,
    }
    return {
        "tool": "arjun", "exit_state": state,
        "complete": state in (c.OK, c.OK_EMPTY),
        "records": records(contract["endpoint_id"], parameters),
        "stats": stats, "task_id": contract["task_id"], "run_id": contract["run_id"],
        "scope_hash": contract["scope_hash"], "config_hash": contract["config_hash"],
        "authority": "candidate_or_hypothesis_only",
    }
