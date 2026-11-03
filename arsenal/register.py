"""Idempotently register the Arsenal execution sidecar with Uroboros.

Raw scanner MCP tools are deliberately not granted to agents. Uroboros calls the
backend-only task lease endpoint and exposes its own task-bound host contract.
Pure helpers are unit-tested; live wiring needs a running stack and never prints
the bearer token.

Usage (stack up, bench/.env has ADMIN_PASSWORD + ARSENAL_TOKEN):
    python arsenal/register.py
"""

import json
import os
import sys
import urllib.error
import urllib.request

from arsenal.auth import redact

BASE = os.environ.get("UROBOROS_BASE", "http://127.0.0.1:8787")
ARSENAL_URL = os.environ.get("ARSENAL_MCP_URL", "http://arsenal:8000/mcp")


# ---- pure helpers (tested) ----
def build_mcp_body(name, url, token):
    return {"name": name, "transport": "http", "url": url, "enabled": True,
            "request_timeout_seconds": 240,
            "env": {"Authorization": "Bearer " + token}}


def merge_visibility(current, mcp_id):
    """Add mcp_id to the agent's visible-mcp set without dropping existing mcp/skill (C: GET->merge->PUT)."""
    mcp = list((current or {}).get("mcp", []) or [])
    skill = list((current or {}).get("skill", []) or [])
    if mcp_id not in mcp:
        mcp.append(mcp_id)
    return {"mcp": mcp, "skill": skill}


def remove_visibility(current, mcp_id):
    """Remove direct model access without disturbing any other MCP or skill."""
    return {"mcp": [item for item in list((current or {}).get("mcp", []) or []) if item != mcp_id],
            "skill": list((current or {}).get("skill", []) or [])}


def select_agents(all_keys, env_value):
    """Which agents to authorize the arsenal for. DEFAULT = every agent (mandate:
    give the model the tools, don't restrict in code). The optional comma-separated
    ARSENAL_AGENTS env restricts to a subset (opt-in), keeping only keys that exist."""
    want = [x.strip() for x in (env_value or "").split(",") if x.strip()]
    if not want:
        return list(all_keys)
    return [k for k in all_keys if k in want]


# ---- live wiring (deferred / operator) ----
def _call(method, path, token, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            raw = r.read().decode()
        return json.loads(raw) if raw.strip() else {}
    except urllib.error.HTTPError as e:
        raise RuntimeError("%s %s -> %s %s" % (method, path, e.code, redact(e.read().decode()[:200])))


def main():
    password = os.environ.get("ADMIN_PASSWORD")
    token = os.environ.get("ARSENAL_TOKEN")
    if not password or not token:
        sys.exit("need ADMIN_PASSWORD and ARSENAL_TOKEN in env (bench/.env)")
    jwt = _call("POST", "/api/auth/login", None, {"username": "Uroboros", "password": password})["token"]

    servers = _call("GET", "/api/mcp", jwt).get("servers", [])
    existing = next((s for s in servers if s["name"] == "arsenal"), None)
    if existing:
        sid = existing["id"]
        # Preserve endpoint, credentials and user configuration; change only a
        # deadline that cannot cover nuclei's 180s budget plus cleanup/delivery.
        if existing.get("request_timeout_seconds", 120) < 240:
            _call("POST", "/api/mcp", jwt, {**existing, "request_timeout_seconds": 240})
        print("arsenal MCP already present (id=%s)" % sid)
    else:
        _call("POST", "/api/mcp", jwt, build_mcp_body("arsenal", ARSENAL_URL, token))
        servers = _call("GET", "/api/mcp", jwt).get("servers", [])
        sid = next(s["id"] for s in servers if s["name"] == "arsenal")
        print("created arsenal MCP (id=%s)" % sid)  # token not printed (C6)
    _call("POST", "/api/mcp/%s/refresh" % sid, jwt)

    agents = _call("GET", "/api/agents", jwt)
    agent_list = agents.get("agents", agents) if isinstance(agents, dict) else agents
    all_keys = [a.get("key") for a in agent_list if a.get("key")]
    for k in all_keys:
        cur = _call("GET", "/api/agents/%s/visibility" % k, jwt)
        narrowed = remove_visibility(cur, sid)
        if narrowed != cur:
            _call("PUT", "/api/agents/%s/visibility" % k, jwt, narrowed)
    print("registered Arsenal as a backend-only MCP; direct agent visibility removed. "
          "Use task-bound arsenal_run/status/results/cancel host tools.")


if __name__ == "__main__":
    main()
