"""Locked contracts for the arsenal sidecar: exit states, record schema (dual
identity per C4), execution caps, and opt-in target validation (C1).

Stdlib-only so the whole testable core runs without the mcp SDK or scanner binaries.
"""

import hashlib
from urllib.parse import urlparse

# --- exit states (C4.3) ---
OK = "ok"
OK_EMPTY = "ok_empty"
BUSY = "busy"
TIMEOUT = "timeout"
TOOL_ERROR = "tool_error"
PARSE_ERROR = "parse_error"
PARTIAL = "partial"
CANCELLED = "cancelled"
SCOPE_REJECTED = "scope_rejected"

# --- execution caps (C2) ---
CAPS = {
    "nuclei": {"timeout": 180, "rps": 50, "conc": 25},
    "httpx": {"timeout": 30, "rps": 50, "conc": 25},
    "ffuf": {"timeout": 120, "rps": 50, "conc": 40},
    "subfinder": {"timeout": 60, "rps": 5, "conc": 2},
    "naabu": {"timeout": 120, "rps": 100, "conc": 25},
    "arjun": {"timeout": 300, "rps": 5, "conc": 1},
}
OUTPUT_CAP_BYTES = 4 * 1024 * 1024  # 4 MiB per stream
MAX_RECORDS = 2000

_ALLOWED_SCHEMES = ("http", "https")


def _h(*parts):
    return hashlib.sha256("|".join(str(p) for p in parts).encode()).hexdigest()[:16]


def make_record(tool, type, severity, name, target, endpoint="", evidence="", raw="", template_id=""):
    """Normalized record with two identities (C4):
    - observation_id: per-tool (includes tool) — traceability of what each scanner saw.
    - dedup_fp: cross-tool vulnerability identity (EXCLUDES tool) — same vuln from two
      tools collapses to one finding.
    """
    return {
        "tool": tool,
        "type": type,
        "severity": severity,
        "name": name,
        "target": target,
        "endpoint": endpoint,
        "evidence": evidence,
        "template_id": template_id,
        "raw": raw,
        "observation_id": _h(tool, type, target, endpoint, name, template_id),
        "dedup_fp": _h(type, target, endpoint, name, template_id),
    }


def validate_target(target, allowlist):
    """(C1, opt-in / default OFF — mandate holds)
    Empty allowlist => scope filter is OFF => allow everything (no baked-in refusal).
    When the operator sets an allowlist, enforce scheme (http/https only) + host/IP + port.
    Returns (ok: bool, reason: str).
    """
    u = urlparse(target if "://" in target else "http://" + target)
    if u.scheme not in _ALLOWED_SCHEMES:
        return False, "scheme not allowed: %s" % u.scheme
    if not allowlist:
        return True, "scope filter off"
    host = (u.hostname or "").lower().rstrip(".")
    try:
        explicit_port = u.port
    except ValueError:
        return False, "target has an invalid port"
    effective_port = explicit_port or (443 if u.scheme == "https" else 80)
    for entry in allowlist:
        e = entry.strip()
        if not e:
            continue
        # Parse host, host:port, IPv4, and bracketed IPv6 without treating an
        # omitted target port as a wildcard.  In particular, an allowlist entry
        # fixture:8443 must never authorize http://fixture/ (effective port 80).
        try:
            parsed = urlparse("http://" + e)
            entry_host = (parsed.hostname or "").lower().rstrip(".")
            entry_port = parsed.port
        except ValueError:
            continue
        if host == entry_host and (entry_port is None or effective_port == entry_port):
            return True, "in allowlist"
    shown_port = str(explicit_port) if explicit_port else ""
    return False, "target %s not in allowlist" % (host + (":" + shown_port if shown_port else ""))
