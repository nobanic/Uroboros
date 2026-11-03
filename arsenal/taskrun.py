"""Backend-only task contract for scanner execution.

The model never supplies this envelope. Uroboros freezes it from the task's
authoritative scope and calls ``arsenal_task_scan`` directly over authenticated
MCP. All scanner HTTP(S) traffic must use the per-run recording proxy.
"""

import hashlib
import hmac
import importlib.util
import ipaddress
import json
import os
import re
import shutil
import tempfile
from contextlib import contextmanager
from functools import lru_cache
from pathlib import Path
from urllib.parse import urlparse

from arsenal import contracts as c, nuclei, parsers, wafpassive


CONTRACT_VERSION = 3
SCAN_TOOLS = ("nuclei", "httpx", "ffuf")
TOOLS = SCAN_TOOLS + ("subfinder", "naabu", "arjun")
_RUN_ID = re.compile(r"^[A-Za-z0-9._-]{1,128}$")
_FFUF_EXTENSIONS = re.compile(r"^\.[A-Za-z0-9]{1,16}(?:,\.[A-Za-z0-9]{1,16})*$")
_SEVERITIES = {"", "info", "low", "medium", "high", "critical", "unknown"}
_PINNED = {
    "nuclei": {"version": "3.3.0", "artifact_sha256": "235f264d32e47e1ccf58d534e2eb4d0d4eeb47f1cae1ebb30a584b8b52565202"},
    "httpx": {"version": "1.6.9", "artifact_sha256": "c8d36461b5d736e88c3f9104fed15f2112eb7263dbda35fd08aa5a771bddfb5f"},
    "ffuf": {"version": "2.1.0", "artifact_sha256": "fc2c82736c14dcbea4daf3d3cf3878c1c4773008ba45c2bc0fceba7d17b40bb5"},
    "subfinder": {"version": "2.17.0", "artifact_sha256": "a39c3e5c7a26ef3c430915c4d6cbf7716a00f26769a75e9405aba796142eba37"},
    "naabu": {"version": "2.6.1", "artifact_sha256": "018c4c9884dea971eda860435ede3021d1150732f34cfd245498c6726d8cab90"},
    "arjun": {"version": "2.2.7", "artifact_sha256": "b193cdaf97bf7b0e8cd91a41da778639e01fd9738d5f666a8161377f475ce72e"},
}
TEMPLATE_VERSION = "10.5.0"
TEMPLATE_ARCHIVE_SHA256 = "dd836a827b4147d7d8294ca6a89526288ccb1a4bcc895dceac7af3563bc61775"


def _canonical_json(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


def _clean_host(raw):
    value = str(raw or "").strip().rstrip(".").lower()
    if not value:
        raise ValueError("scope host is empty")
    try:
        return ipaddress.ip_address(value.strip("[]")).compressed
    except ValueError:
        pass
    if any(ch.isspace() for ch in value) or any(ch in value for ch in "/\\?#@:"):
        raise ValueError("scope host is malformed")
    return value.encode("idna").decode("ascii")


def normalize_scope(value):
    if not isinstance(value, dict):
        raise ValueError("frozen_scope must be an object")
    if set(value) - {"root_domains", "exact_hosts", "cidrs"}:
        raise ValueError("frozen_scope has unknown fields")

    def string_list(key):
        values = value.get(key, [])
        if not isinstance(values, list) or not all(isinstance(item, str) for item in values):
            raise ValueError("frozen_scope.%s must be a string array" % key)
        return values

    roots = set()
    for raw in string_list("root_domains"):
        host = _clean_host(raw)
        try:
            ipaddress.ip_address(host)
        except ValueError:
            roots.add(host)
        else:
            raise ValueError("IP literals belong in frozen_scope.cidrs")
    exact = {_clean_host(raw) for raw in string_list("exact_hosts")}
    cidrs = set()
    for raw in string_list("cidrs"):
        try:
            cidrs.add(str(ipaddress.ip_network(raw.strip(), strict=False)))
        except ValueError as exc:
            raise ValueError("invalid frozen_scope CIDR: %s" % raw) from exc
    if not roots and not exact and not cidrs:
        raise ValueError("frozen_scope is empty")
    return {"root_domains": sorted(roots), "exact_hosts": sorted(exact), "cidrs": sorted(cidrs)}


def scope_hash(scope):
    return hashlib.sha256(_canonical_json(normalize_scope(scope)).encode()).hexdigest()


def validate_task_target(target, scope):
    parsed = urlparse(str(target or "").strip())
    if parsed.scheme not in ("http", "https") or not parsed.hostname:
        raise ValueError("target must be an absolute HTTP(S) URL")
    try:
        parsed.port
    except ValueError as exc:
        raise ValueError("target has an invalid port") from exc
    host = _clean_host(parsed.hostname)
    normalized = normalize_scope(scope)
    if host in normalized["exact_hosts"]:
        return str(target).strip()
    try:
        address = ipaddress.ip_address(host)
    except ValueError:
        if any(host == root or host.endswith("." + root) for root in normalized["root_domains"]):
            return str(target).strip()
    else:
        if any(address in ipaddress.ip_network(raw) for raw in normalized["cidrs"]):
            return str(target).strip()
    raise ValueError("target is outside the frozen task scope")


def validate_proxy_url(raw):
    parsed = urlparse(str(raw or "").strip())
    if parsed.scheme not in ("http", "https") or not parsed.hostname or not parsed.port:
        raise ValueError("task run requires an authenticated HTTP(S) recording proxy URL")
    if parsed.username != "uroboros" or not parsed.password or len(parsed.password) < 32:
        raise ValueError("task run recording proxy credentials are missing")
    return str(raw).strip()


def config_hash(tool, target, config):
    return hashlib.sha256(_canonical_json({"config": config, "target": target, "tool": tool}).encode()).hexdigest()


def parse_contract(task_id, run_id, tool, target, scope_json, expected_scope_hash,
                   config_json, expected_config_hash, proxy_url):
    if isinstance(task_id, bool) or not isinstance(task_id, int) or task_id <= 0:
        raise ValueError("task_id must be a positive integer")
    if not _RUN_ID.fullmatch(str(run_id or "")):
        raise ValueError("run_id is invalid")
    if tool not in SCAN_TOOLS:
        raise ValueError("tool must be nuclei, httpx or ffuf")
    try:
        scope = normalize_scope(json.loads(scope_json))
        config = json.loads(config_json or "{}")
    except json.JSONDecodeError as exc:
        raise ValueError("scope/config JSON is invalid") from exc
    if not isinstance(config, dict):
        raise ValueError("config must be an object")
    target = validate_task_target(target, scope)
    actual_scope_hash = scope_hash(scope)
    if not expected_scope_hash or not hmac.compare_digest(actual_scope_hash, expected_scope_hash):
        raise ValueError("frozen scope hash mismatch")
    actual_config_hash = config_hash(tool, target, config)
    if not expected_config_hash or not hmac.compare_digest(actual_config_hash, expected_config_hash):
        raise ValueError("scanner config hash mismatch")
    return {
        "task_id": task_id, "run_id": str(run_id), "tool": tool, "target": target,
        "scope": scope, "scope_hash": actual_scope_hash, "config": config,
        "config_hash": actual_config_hash, "proxy_url": validate_proxy_url(proxy_url),
    }


def _integer(config, key, default, minimum, maximum):
    value = config.get(key, default)
    if isinstance(value, bool) or not isinstance(value, int) or not minimum <= value <= maximum:
        raise ValueError("%s must be an integer between %d and %d" % (key, minimum, maximum))
    return value


def build_command(contract, wordlist, template_dir):
    tool, target, config, proxy_url = (contract[k] for k in ("tool", "target", "config", "proxy_url"))
    allowed = {
        "httpx": {"budget_seconds"},
        "ffuf": {"budget_seconds", "extensions"},
        "nuclei": {"budget_seconds", "profile", "template_ids", "severity", "tags"},
    }[tool]
    unknown = set(config) - allowed
    if unknown:
        raise ValueError("unknown %s config fields: %s" % (tool, ", ".join(sorted(unknown))))

    caps = c.CAPS[tool]
    if tool == "httpx":
        timeout = _integer(config, "budget_seconds", caps["timeout"], 1, caps["timeout"])
        argv = ["httpx", "-u", target, "-json", "-title", "-tech-detect", "-status-code", "-silent",
                "-rl", str(caps["rps"]), "-threads", str(caps["conc"]), "-fr", "-maxr", "10",
                "-nfs", "-duc", "-no-stdin", "-http-proxy", proxy_url]
        return argv, timeout, parsers.parse_httpx, {"follow_redirects": True, "proxy_enforced": True}
    if tool == "ffuf":
        timeout = _integer(config, "budget_seconds", caps["timeout"], 1, caps["timeout"])
        extensions = str(config.get("extensions", "")).strip()
        if extensions and not _FFUF_EXTENSIONS.fullmatch(extensions):
            raise ValueError("extensions must be a comma-separated list such as .php,.txt")
        url = target.rstrip("/") + "/FUZZ"
        argv = ["ffuf", "-u", url, "-w", wordlist, "-ac", "-noninteractive", "-of", "json",
                "-o", "/dev/stdout", "-rate", str(caps["rps"]), "-t", str(caps["conc"]), "-s",
                "-x", proxy_url, "-maxtime", str(timeout)]
        if extensions:
            argv += ["-e", extensions]
        return argv, timeout, parsers.parse_ffuf, {"extensions": extensions, "proxy_enforced": True}

    profile = str(config.get("profile", "full"))
    template_ids = str(config.get("template_ids", "")).strip()
    severity = str(config.get("severity", "")).strip().lower()
    tags = str(config.get("tags", "")).strip()
    if profile not in ("light", "full"):
        raise ValueError("profile must be light or full")
    if severity not in _SEVERITIES:
        raise ValueError("severity is invalid")
    if any(ch in template_ids + tags for ch in "\r\n\x00"):
        raise ValueError("template_ids/tags contain invalid control characters")
    argv, metadata = nuclei.command(target, severity, tags, profile, template_ids,
                                    template_dir=template_dir, task_bound=True, proxy_url=proxy_url)
    default_budget = metadata["execution_budget_seconds"]
    timeout = _integer(config, "budget_seconds", default_budget, 10, 7200)
    metadata["execution_budget_seconds"] = timeout
    return argv, timeout, parsers.parse_nuclei, metadata


def _hash_file(path):
    if not path or not Path(path).is_file():
        return "unavailable"
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _arjun_data_paths():
    spec = importlib.util.find_spec("arjun")
    root = Path(spec.origin).resolve().parent if spec is not None and spec.origin else Path("/nonexistent")
    return {
        "wordlist": str(root / "db" / "large.txt"),
        "special": str(root / "db" / "special.json"),
        "dependency_lock": "/opt/arsenal/requirements.lock",
    }


@lru_cache(maxsize=4)
def metadata(wordlist="/opt/arsenal/wordlist.txt"):
    tools = {}
    for name in TOOLS:
        pinned = dict(_PINNED[name])
        pinned["binary_sha256"] = _hash_file(shutil.which(name))
        tools[name] = pinned
    # WAFW00F is not installed or invoked as a binary.  The exact upstream
    # plugin corpus passes a strict AST/hash gate before this provenance is
    # exposed; its corpus hash is the executable fingerprint.
    tools["wafw00f"] = wafpassive.provenance()
    arjun_paths = _arjun_data_paths()
    return {
        "contract_version": CONTRACT_VERSION,
        "source_revision": os.environ.get("ARSENAL_SOURCE_REVISION", "unknown"),
        "tools": tools,
        "nuclei_templates": {"version": TEMPLATE_VERSION, "archive_sha256": TEMPLATE_ARCHIVE_SHA256},
        "wordlist_sha256": _hash_file(wordlist),
        "arjun_wordlist_sha256": _hash_file(arjun_paths["wordlist"]),
        "arjun_special_sha256": _hash_file(arjun_paths["special"]),
        "dependency_lock_sha256": _hash_file(arjun_paths["dependency_lock"]),
        "authority": "candidate_or_hypothesis_only",
    }


@contextmanager
def runtime_environment(proxy_url="", ca_pem=""):
    if proxy_url and (not isinstance(ca_pem, str) or "BEGIN CERTIFICATE" not in ca_pem or
                      len(ca_pem) > 256 * 1024):
        raise ValueError("recording proxy CA certificate is missing or invalid")
    with tempfile.TemporaryDirectory(prefix="arsenal-task-") as runtime_dir:
        allowed = {"PATH", "LANG", "LANGUAGE", "LC_ALL", "TZ", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT"}
        env = {key: value for key, value in os.environ.items() if key.upper() in allowed}
        env.update({
            "HOME": runtime_dir, "USERPROFILE": runtime_dir,
            "XDG_CONFIG_HOME": str(Path(runtime_dir) / "config"),
            "XDG_CACHE_HOME": str(Path(runtime_dir) / "cache"),
            "TMPDIR": runtime_dir, "TEMP": runtime_dir, "TMP": runtime_dir,
        })
        if proxy_url:
            ca_path = Path(runtime_dir) / "uroboros-ca.pem"
            ca_path.write_text(ca_pem, encoding="utf-8")
            env.update({
                "HTTP_PROXY": proxy_url, "HTTPS_PROXY": proxy_url, "ALL_PROXY": proxy_url,
                "http_proxy": proxy_url, "https_proxy": proxy_url, "all_proxy": proxy_url,
                "NO_PROXY": "", "no_proxy": "", "SSL_CERT_FILE": str(ca_path),
                "CURL_CA_BUNDLE": str(ca_path),
            })
        yield env, runtime_dir


def redact_proxy(value, proxy_url):
    parsed = urlparse(proxy_url)
    secrets = [proxy_url, parsed.password or ""]

    def clean(item):
        if isinstance(item, str):
            for secret in secrets:
                if secret:
                    item = item.replace(secret, "REDACTED")
            return item
        if isinstance(item, list):
            return [clean(part) for part in item]
        if isinstance(item, dict):
            return {key: clean(part) for key, part in item.items()}
        return item

    return clean(value)
