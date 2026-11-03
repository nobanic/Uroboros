"""Idempotent console registration; preserves existing server and agent settings."""
import os
from arsenal.register import _call,merge_visibility,select_agents


def configured_secret(name):
    # docker --env-file retains .env quoting, unlike Compose interpolation.
    value = os.environ.get(name,"")
    if len(value)>=2 and value[0] in ("'",'"') and value[-1]==value[0]:
        return value[1:-1]
    return value


def main():
    password = configured_secret("ADMIN_PASSWORD")
    token = configured_secret("API_LAB_TOKEN") or configured_secret("ARSENAL_TOKEN")
    if not password or not token:
        raise SystemExit("ADMIN_PASSWORD and API_LAB_TOKEN (or ARSENAL_TOKEN) are required")
    jwt = _call("POST","/api/auth/login",None,{"username":"Uroboros","password":password})["token"]
    servers = _call("GET","/api/mcp",jwt).get("servers",[])
    existing = next((item for item in servers if item["name"]=="api_lab"),None)
    if existing:
        server_id = existing["id"]
    else:
        _call("POST","/api/mcp",jwt,{"name":"api_lab","transport":"http","enabled":True,
              "url":os.environ.get("API_LAB_MCP_URL","http://api-lab:8010/mcp"),"request_timeout_seconds":60,
              "env":{"Authorization":"Bearer "+token}})
        server_id = next(item["id"] for item in _call("GET","/api/mcp",jwt)["servers"] if item["name"]=="api_lab")
    tools = _call("POST","/api/mcp/%s/refresh"%server_id,jwt).get("tools",[])
    agents = _call("GET","/api/agents",jwt)
    available = agents.get("agents",[]) if isinstance(agents,dict) else agents
    keys = select_agents([item["key"] for item in available],os.environ.get("API_LAB_AGENTS",""))
    for key in keys:
        visibility = _call("GET","/api/agents/%s/visibility"%key,jwt)
        _call("PUT","/api/agents/%s/visibility"%key,jwt,merge_visibility(visibility,server_id))
    print("API Lab registered: id=%s, tools=%s, visible agents=%s"%(server_id,len(tools),",".join(keys)))


if __name__=="__main__":
    try:
        main()
    except (RuntimeError,OSError,KeyError) as exc:
        raise SystemExit(str(exc))
