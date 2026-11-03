"""Fail-closed planning and evidence finalization for Katana shadow crawls.

This module is intentionally offline.  It neither starts a process nor talks to
Docker, MCP, the database, or the planner.  The separate operator runner may use
these pure contracts to execute one explicitly scoped crawl.
"""

from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import urlsplit, urlunsplit

from api_lab.runtime_evidence import (
    import_katana_jsonl,
    katana_proxy_traffic_ids,
    make_manifest,
)


KATANA_VERSION = "1.8.0"
KATANA_SOURCE = "https://github.com/projectdiscovery/katana"
KATANA_LICENSE = "MIT"
KATANA_PLATFORM = "linux/amd64"
KATANA_IMAGE_MANIFEST_SHA256 = "92ccca6e268149d01dc5d8f628fcfc2fc4b0f932f5d4d5f93eb3f1ba9cc74bcf"
KATANA_IMAGE = "docker.io/projectdiscovery/katana@sha256:" + KATANA_IMAGE_MANIFEST_SHA256

CAPTURE_REQUEST_HEADER = "X-Uroboros-Capture-Request"
TRAFFIC_ID_HEADER = "X-Uroboros-Traffic-ID"
DEFAULT_RECORDING_PROXY = "http://127.0.0.1:8788"
DEFAULT_GUARD_PROXY = "http://127.0.0.1:18789"
MODES = {"standard", "headless"}
EXPECTED_GUARD_REJECTIONS = {
    "outside_authority",
    "outside_origin",
    "method_not_read_only",
    "transaction_budget",
}
REFERENCE = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}\Z")
CONTAINER_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}\Z")

HEADLESS_OPTIONS = (
    "--disable-background-networking",
    "--disable-client-side-phishing-detection",
    "--disable-component-update",
    "--disable-default-apps",
    "--disable-domain-reliability",
    "--disable-quic",
    "--disable-sync",
    "--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
    "--metrics-recording-only",
    "--no-first-run",
    "--safebrowsing-disable-auto-update",
    "--webrtc-ip-handling-policy=disable_non_proxied_udp",
)


def _canonical(value) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("ascii")


def _sha256(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def _bounded_string(value, label, maximum=512):
    if not isinstance(value, str) or not value or len(value) > maximum:
        raise ValueError(label + " must be a non-empty bounded string")
    if any(ord(character) < 32 or ord(character) == 127 for character in value):
        raise ValueError(label + " contains control characters")
    return value


def _reference(value, label):
    if not isinstance(value, str) or not REFERENCE.fullmatch(value):
        raise ValueError(label + " must be an opaque reference")
    return value


def _positive_int(value, label, lower, upper):
    if not isinstance(value, int) or isinstance(value, bool) or not lower <= value <= upper:
        raise ValueError("%s must be between %d and %d" % (label, lower, upper))
    return value


def _http_url(value, label, *, origin_only=False, query=False):
    _bounded_string(value, label, 2048)
    if any(ord(character) <= 32 for character in value):
        raise ValueError(label + " contains whitespace")
    parsed = urlsplit(value)
    if parsed.scheme.lower() not in {"http", "https"} or not parsed.hostname:
        raise ValueError(label + " must be an absolute HTTP(S) URL")
    if parsed.username is not None or parsed.password is not None:
        raise ValueError(label + " must not contain credentials")
    try:
        port = parsed.port
    except ValueError:
        raise ValueError(label + " contains an invalid port") from None
    if parsed.fragment or (parsed.query and not query):
        raise ValueError(label + " must not contain a query or fragment")
    host = parsed.hostname.lower()
    if ":" in host:
        host = "[" + host.strip("[]") + "]"
    default = 80 if parsed.scheme.lower() == "http" else 443
    netloc = host + ((":" + str(port)) if port is not None and port != default else "")
    path = parsed.path or "/"
    normalized = urlunsplit((parsed.scheme.lower(), netloc, "" if origin_only else path, parsed.query, ""))
    return normalized.rstrip("/") if origin_only else normalized


def target_origin(target_url):
    parsed = urlsplit(_http_url(target_url, "target URL"))
    return _http_url(urlunsplit((parsed.scheme, parsed.netloc, "", "", "")), "target origin", origin_only=True)


def _loopback_proxy(value, label):
    normalized = _http_url(value, label, origin_only=True)
    parsed = urlsplit(normalized)
    if parsed.scheme != "http" or parsed.hostname not in {"127.0.0.1", "::1", "localhost"} or parsed.port is None:
        raise ValueError(label + " must be an explicit loopback HTTP proxy")
    return normalized


def exact_origin_scope_regex(origin):
    """Return a Go-compatible regex that includes only this exact origin."""
    selected = _http_url(origin, "scope origin", origin_only=True)
    return "^" + re.escape(selected) + r"(?:/.*)?$"


@dataclass(frozen=True)
class KatanaShadowPlan:
    run_id: str
    target_url: str
    target_revision: str
    scope_ref: str
    network_container: str
    mode: str = "standard"
    recording_proxy: str = DEFAULT_RECORDING_PROXY
    max_requests: int = 100
    max_seconds: int = 30
    max_concurrency: int = 2
    max_response_bytes: int = 1_048_576
    depth: int = 3

    def __post_init__(self):
        if not isinstance(self.run_id, str) or len(self.run_id) > 80 or not CONTAINER_NAME.fullmatch(self.run_id):
            raise ValueError("run_id must be a filename-safe opaque reference")
        _http_url(self.target_url, "target URL")
        _bounded_string(self.target_revision, "target revision")
        _reference(self.scope_ref, "scope reference")
        if not isinstance(self.network_container, str) or not CONTAINER_NAME.fullmatch(self.network_container):
            raise ValueError("network container name is invalid")
        if self.mode not in MODES:
            raise ValueError("mode must be standard or headless")
        if self.mode == "headless" and target_origin(self.target_url).startswith("https://"):
            raise ValueError(
                "phase-1 headless execution is limited to controlled HTTP targets; "
                "an HTTPS method guard is required before external use"
            )
        _loopback_proxy(self.recording_proxy, "recording proxy")
        _positive_int(self.max_requests, "max_requests", 1, 500)
        # Phase 1 intentionally stays a small shadow crawl.  Longer scans need a
        # separately reviewed total-request enforcement mechanism.
        _positive_int(self.max_seconds, "max_seconds", 5, 60)
        _positive_int(self.max_concurrency, "max_concurrency", 1, 4)
        _positive_int(self.max_response_bytes, "max_response_bytes", 1024, 4 * 1024 * 1024)
        _positive_int(self.depth, "depth", 1, 5)

    @property
    def origin(self):
        return target_origin(self.target_url)


def katana_argv(plan: KatanaShadowPlan, guard_proxy=DEFAULT_GUARD_PROXY):
    """Build the deterministic scanner argv; no shell syntax is ever returned."""
    proxy = _loopback_proxy(guard_proxy, "scope guard proxy")
    timeout = min(10, max(2, plan.max_seconds // 2))
    argv = [
        "-u", plan.target_url,
        "-proxy", proxy,
        "-H", CAPTURE_REQUEST_HEADER + ": 1",
        "-crawl-scope", exact_origin_scope_regex(plan.origin),
        "-field-scope", "fqdn",
        "-depth", str(plan.depth),
        "-crawl-duration", str(plan.max_seconds) + "s",
        "-max-domain-pages", str(plan.max_requests),
        "-max-response-size", str(plan.max_response_bytes),
        "-timeout", str(timeout),
        "-retry", "0",
        "-concurrency", str(plan.max_concurrency),
        "-parallelism", "1",
        "-rate-limit", str(plan.max_concurrency),
        "-rate-limit-minute", str(plan.max_requests),
        "-host-rate-limit", str(plan.max_concurrency),
        "-disable-redirects",
        "-js-crawl",
        "-form-extraction",
        "-jsonl",
        "-omit-raw",
        "-omit-body",
        "-no-color",
        "-silent",
        "-disable-update-check",
    ]
    if plan.mode == "headless":
        argv.extend([
            "-headless",
            "-system-chrome",
            "-system-chrome-path", "/usr/bin/chromium-browser",
            "-no-sandbox",
            "-xhr-extraction",
            "-page-load-strategy", "domcontentloaded",
            "-dom-wait-time", "2",
            "-headless-options", ",".join(HEADLESS_OPTIONS),
        ])
    return argv


def plan_document(plan: KatanaShadowPlan, guard_proxy=DEFAULT_GUARD_PROXY):
    """Canonical, credential-free configuration included by hash in evidence."""
    return {
        "contract": "uroboros-katana-shadow-v1",
        "run_id": plan.run_id,
        "target_url": plan.target_url,
        "target_origin": plan.origin,
        "target_revision": plan.target_revision,
        "scope_ref": plan.scope_ref,
        "network_container": plan.network_container,
        "recording_proxy": _loopback_proxy(plan.recording_proxy, "recording proxy"),
        "scope_guard_proxy": _loopback_proxy(guard_proxy, "scope guard proxy"),
        "mode": plan.mode,
        "budgets": {
            "max_requests": plan.max_requests,
            "max_seconds": plan.max_seconds,
            "max_concurrency": plan.max_concurrency,
            "max_response_bytes": plan.max_response_bytes,
            "depth": plan.depth,
        },
        "katana": {
            "version": KATANA_VERSION,
            "image": KATANA_IMAGE,
            "platform": KATANA_PLATFORM,
            "argv": katana_argv(plan, guard_proxy),
        },
        "disabled_features": [
            "automatic_form_fill",
            "automatic_login",
            "captcha_solver",
            "headless_remote_browser",
            "knowledge_base",
            "known_files",
            "no_scope",
            "recorded_flow",
            "secret_validation",
            "store_response",
            "update",
        ],
    }


def plan_sha256(plan: KatanaShadowPlan, guard_proxy=DEFAULT_GUARD_PROXY):
    return _sha256(_canonical(plan_document(plan, guard_proxy)))


def katana_docker_argv(
    plan: KatanaShadowPlan,
    *,
    container_name,
    guard_proxy=DEFAULT_GUARD_PROXY,
    ca_file=None,
):
    """Build a hardened one-shot Docker invocation for the pinned image."""
    if not isinstance(container_name, str) or not CONTAINER_NAME.fullmatch(container_name):
        raise ValueError("Katana container name is invalid")
    if plan.origin.startswith("https://") and ca_file is None:
        raise ValueError("HTTPS shadow crawls require the recording-proxy CA file")
    temporary_mount = "/tmp:rw,nosuid,nodev,size=512m"
    if plan.mode == "headless":
        # go-rod's leakless launcher materializes a tiny helper in its temporary
        # directory.  exec is required there; the mount remains nosuid/nodev and
        # exists only for this capability-dropped one-shot container.
        temporary_mount = "/tmp:rw,exec,nosuid,nodev,size=512m"
    argv = [
        "docker", "run", "--rm", "--pull", "never",
        "--name", container_name,
        "--platform", KATANA_PLATFORM,
        "--network", "container:" + plan.network_container,
        "--read-only",
        "--tmpfs", temporary_mount,
        "--shm-size", "256m",
        "--cap-drop", "ALL",
        "--security-opt", "no-new-privileges:true",
        "--pids-limit", "256",
        "--memory", "1g",
        "--cpus", "1.0",
        "--ulimit", "nofile=1024:1024",
        "--user", "65532:65532",
        "--env", "HOME=/tmp",
        "--env", "XDG_CACHE_HOME=/tmp/.cache",
        "--env", "XDG_CONFIG_HOME=/tmp/.config",
        KATANA_IMAGE,
    ]
    if ca_file is not None:
        selected = Path(ca_file).resolve()
        if not selected.is_file():
            raise ValueError("recording-proxy CA file does not exist")
        argv[argv.index(KATANA_IMAGE):argv.index(KATANA_IMAGE)] = [
            "--volume", str(selected) + ":/run/uroboros-ca.pem:ro",
            "--env", "SSL_CERT_FILE=/run/uroboros-ca.pem",
        ]
    argv.extend(katana_argv(plan, guard_proxy))
    return argv


def finalize_evidence(
    raw_jsonl,
    plan: KatanaShadowPlan,
    *,
    started_at,
    finished_at,
    browser_version=None,
    guard_coverage=None,
    exit_reason="completed",
    truncated=False,
    guard_proxy=DEFAULT_GUARD_PROXY,
):
    """Accept only proxy-confirmed Katana output and produce a graph batch."""
    traffic_ids = katana_proxy_traffic_ids(raw_jsonl, require_all_responses=True)
    if len(traffic_ids) > plan.max_requests:
        raise ValueError("Katana proxy-confirmed responses exceed max_requests")
    if plan.mode == "headless":
        _bounded_string(browser_version, "browser version", 128)
    elif browser_version is not None:
        raise ValueError("standard crawl must not claim a browser version")
    if not isinstance(guard_coverage, dict):
        raise ValueError("Katana shadow evidence requires scope-guard coverage")
    for key in ("allowed", "rejected", "upstream_errors"):
        value = guard_coverage.get(key)
        if not isinstance(value, int) or isinstance(value, bool) or value < 0:
            raise ValueError("scope-guard coverage counter is invalid: " + key)
    rejected_reasons = guard_coverage.get("rejected_reasons")
    if not isinstance(rejected_reasons, dict) or any(
        not isinstance(reason, str)
        or not isinstance(count, int)
        or isinstance(count, bool)
        or count <= 0
        for reason, count in rejected_reasons.items()
    ):
        raise ValueError("scope-guard rejection reasons are invalid")
    unexpected_reasons = sorted(set(rejected_reasons) - EXPECTED_GUARD_REJECTIONS)
    if unexpected_reasons:
        raise ValueError("scope-guard rejection reasons are unsupported: " + ", ".join(unexpected_reasons))
    if sum(rejected_reasons.values()) != guard_coverage["rejected"]:
        raise ValueError("scope-guard rejection reasons do not match the rejection counter")
    transactions = guard_coverage.get("transactions")
    if (
        not isinstance(transactions, int) or isinstance(transactions, bool) or transactions <= 0
    ):
        raise ValueError("scope-guard transaction counter is invalid")
    if transactions != guard_coverage["allowed"] + guard_coverage["rejected"]:
        raise ValueError("scope-guard transaction counter does not match allowed and rejected traffic")
    budget_rejections = rejected_reasons.get("transaction_budget", 0)
    if budget_rejections:
        if exit_reason != "budget_exhausted" or truncated is not True:
            raise ValueError("transaction-budget evidence must be marked budget_exhausted and truncated")
        if transactions <= plan.max_requests:
            raise ValueError("transaction-budget rejection does not exceed the configured budget")
    elif exit_reason == "budget_exhausted":
        raise ValueError("budget_exhausted evidence requires a transaction-budget rejection")
    elif transactions > plan.max_requests:
        raise ValueError("scope-guard transactions exceed max_requests without a budget rejection")
    if guard_coverage["allowed"] < len(traffic_ids):
        raise ValueError("scope-guard coverage cannot account for proxy-confirmed responses")
    if guard_coverage["upstream_errors"] != 0:
        raise ValueError("scope-guard coverage contains an upstream error")
    coverage = {
        "mode": plan.mode,
        "scope_ref": plan.scope_ref,
        "recording_proxy": {
            "capture_request_header": CAPTURE_REQUEST_HEADER.lower(),
            "traffic_id_header": TRAFFIC_ID_HEADER.lower(),
            "confirmed_responses": len(traffic_ids),
            "traffic_ids": sorted(traffic_ids),
        },
        "scope_guard": guard_coverage,
    }
    limitations = [
        "Anonymous shadow crawl only; authentication flows are disabled in phase 1.",
        "Automatic form submission, login, captcha solving, knowledge-base downloads and secret validation are disabled.",
        "Katana candidates require independent recording-proxy inspection and API Lab controls before finding promotion.",
        "Shadow evidence is not registered with MCP and is invisible to the planner and agents.",
    ]
    if plan.mode == "headless":
        limitations.append("Chromium uses --no-sandbox inside a read-only, capability-dropped, no-new-privileges container.")
        limitations.append("The one-shot container has a nosuid/nodev exec-enabled /tmp only for go-rod's leakless launcher helper.")
        limitations.append("QUIC and non-proxied WebRTC UDP are disabled; the selected Uroboros network namespace remains the outer egress boundary.")
    if budget_rejections:
        limitations.append("The transaction budget was exhausted; this incomplete crawl cannot support absence or full-coverage claims.")
    manifest = make_manifest(
        tool_name="katana",
        tool_version=KATANA_VERSION,
        tool_sha256=KATANA_IMAGE_MANIFEST_SHA256,
        tool_source=KATANA_SOURCE,
        license_id=KATANA_LICENSE,
        run_id=plan.run_id,
        target_origin=plan.origin,
        target_revision=plan.target_revision,
        scope_origins=[plan.origin],
        started_at=started_at,
        finished_at=finished_at,
        exit_reason=exit_reason,
        budgets={
            "max_requests": plan.max_requests,
            "max_seconds": plan.max_seconds,
            "max_concurrency": plan.max_concurrency,
        },
        config_sha256=plan_sha256(plan, guard_proxy),
        browser_version=browser_version,
        coverage=coverage,
        truncated=truncated,
        limitations=limitations,
    )
    bundle = import_katana_jsonl(raw_jsonl, manifest, proxy_confirmed_only=True)
    observed_ids = sorted(
        node["data"]["traffic_id"]
        for node in bundle["nodes"]
        if node["kind"] == "response_observation" and "traffic_id" in node["data"]
    )
    if observed_ids != sorted(traffic_ids):
        raise ValueError("Katana evidence lost recording-proxy lineage")
    imported_rejections = bundle["summary"]["details"].get("scope_guard_rejections", 0)
    if guard_coverage["rejected"] < imported_rejections:
        raise ValueError("scope-guard coverage lost a Katana rejection record")
    return bundle
