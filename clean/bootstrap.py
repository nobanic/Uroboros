"""Idempotently activate the clean bundle's source-owned MCP integrations.

This runs inside a one-shot, non-published control-plane container before the
auth-enabled application starts. It never creates a user or password. Sidecar
bearer tokens are internal random runtime credentials and are never printed.
"""

import json
import os
import time
import urllib.error
import urllib.request


BASE = os.environ.get("UROBOROS_BASE", "http://uroboros:8787").rstrip("/")


def call(method, path, body=None, timeout=75):
    payload = json.dumps(body).encode("utf-8") if body is not None else None
    request = urllib.request.Request(BASE + path, data=payload, method=method)
    request.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            raw = response.read().decode("utf-8")
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", errors="replace")[:500]
        raise RuntimeError(f"{method} {path} returned {exc.code}: {detail}") from exc
    return json.loads(raw) if raw.strip() else {}


def wait_for_control_plane():
    last = None
    for _ in range(120):
        try:
            health = call("GET", "/api/health", timeout=5)
            auth = call("GET", "/api/auth/status", timeout=5)
            if health.get("ok") and health.get("auth_disabled") is True and auth.get("disabled") is True:
                return
            last = RuntimeError("one-shot control plane did not confirm bootstrap auth mode")
        except (OSError, RuntimeError, ValueError) as exc:
            last = exc
        time.sleep(2)
    raise RuntimeError(f"control plane did not become ready: {last}")


def agents():
    response = call("GET", "/api/agents")
    rows = response.get("agents", response) if isinstance(response, dict) else response
    return [row["key"] for row in rows if row.get("key")]


def servers():
    return call("GET", "/api/mcp").get("servers", [])


def upsert_server(name, *, transport, url="", command="", args=None, env=None, timeout=120):
    current = next((item for item in servers() if item.get("name") == name), None)
    body = {
        "id": int(current.get("id", 0)) if current else 0,
        "name": name,
        "transport": transport,
        "command": command,
        "args": args or [],
        "env": env or {},
        "url": url,
        "enabled": True,
        "insecure": False,
        "request_timeout_seconds": timeout,
    }
    server_id = int(call("POST", "/api/mcp", body)["id"])
    tools = call("POST", f"/api/mcp/{server_id}/refresh", timeout=max(timeout, 75)).get("tools", [])
    return server_id, len(tools)


def set_visibility(agent_key, server_id, visible):
    path = f"/api/agents/{agent_key}/visibility"
    current = call("GET", path)
    mcp = [int(item) for item in (current.get("mcp") or [])]
    skills = list(current.get("skill") or [])
    if visible and server_id not in mcp:
        mcp.append(server_id)
    if not visible:
        mcp = [item for item in mcp if item != server_id]
    call("PUT", path, {"mcp": mcp, "skill": skills})


def main():
    arsenal_token = os.environ.get("ARSENAL_TOKEN", "")
    api_lab_token = os.environ.get("API_LAB_TOKEN", "")
    if not arsenal_token or not api_lab_token:
        raise RuntimeError("internal sidecar tokens are missing")

    wait_for_control_plane()
    agent_keys = agents()

    browser_id, browser_tools = upsert_server(
        "browser",
        transport="stdio",
        command="npx",
        args=["@playwright/mcp", "--headless"],
        timeout=120,
    )
    for key in agent_keys:
        set_visibility(key, browser_id, True)

    arsenal_id, arsenal_tools = upsert_server(
        "arsenal",
        transport="http",
        url=os.environ.get("ARSENAL_MCP_URL", "http://arsenal:8000/mcp"),
        env={"Authorization": "Bearer " + arsenal_token},
        timeout=240,
    )
    # Raw scanner endpoints remain backend-only; model-facing typed Arsenal
    # tools are built into Uroboros and enforce task scope/evidence authority.
    for key in agent_keys:
        set_visibility(key, arsenal_id, False)

    api_lab_id, api_lab_tools = upsert_server(
        "api_lab",
        transport="http",
        url=os.environ.get("API_LAB_MCP_URL", "http://api-lab:8010/mcp"),
        env={"Authorization": "Bearer " + api_lab_token},
        timeout=120,
    )
    for key in agent_keys:
        set_visibility(key, api_lab_id, True)

    print(
        "clean bootstrap complete: "
        f"browser={browser_tools} tools, arsenal={arsenal_tools} backend tools, "
        f"api_lab={api_lab_tools} tools, agents={len(agent_keys)}"
    )


if __name__ == "__main__":
    main()
