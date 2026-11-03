"""Smoke only the new captured-traffic bridge; no LLM or previous job reports."""
import asyncio
import json
import logging
import os
import time
import uuid

import requests
from mcp import ClientSession
from mcp.client.streamable_http import streamablehttp_client

from api_lab.engine import digest
from api_lab.fixture import example_plan
from api_lab.register import configured_secret
from arsenal.register import _call


async def main():
    logging.getLogger("httpx").setLevel(logging.WARNING)
    logging.getLogger("mcp").setLevel(logging.WARNING)
    password = configured_secret("ADMIN_PASSWORD")
    if not password:
        raise RuntimeError("ADMIN_PASSWORD is required")
    jwt = await asyncio.to_thread(_call, "POST", "/api/auth/login", None, {"username": "Uroboros", "password": password})
    jwt = jwt["token"]
    bases = ["http://api-lab-vulnerable:9000", "http://api-lab-fixed:9000"]
    marker = uuid.uuid4().hex
    proxy = os.environ.get("TRAFFIC_SCHEMA_PROXY", "http://127.0.0.1:8788")
    # Run in the console network namespace so its loopback recording proxy is reachable.
    session = requests.Session()
    session.trust_env = False
    session.proxies = {"http": proxy}
    def capture(method, path, body=None):
        response = session.request(method, bases[0] + path + "?traffic_schema_smoke=" + marker,
                                   headers={"Authorization": "Bearer fixture-alice"}, json=body, timeout=10)
        response.raise_for_status()
        return response.json()
    note = None
    try:
        note = await asyncio.to_thread(capture, "POST", "/notes", {"title": "traffic-schema-smoke", "secret": marker})
        await asyncio.to_thread(capture, "GET", "/notes/%d" % note["id"])
        await asyncio.to_thread(capture, "DELETE", "/notes/%d" % note["id"])
        note = None
        await asyncio.to_thread(capture, "GET", "/health")
    finally:
        if note:
            await asyncio.to_thread(capture, "DELETE", "/notes/%d" % note["id"])
        session.close()
    traffic = await asyncio.to_thread(_call, "GET", "/api/traffic?q=" + marker + "&size=100", jwt)
    ids = [entry["id"] for entry in traffic["exchanges"]]
    if len(ids) != 4:
        raise RuntimeError("expected four new captured fixture exchanges, got %d" % len(ids))
    imported = await asyncio.to_thread(_call, "POST", "/api/traffic/schema", jwt,
                                      {"base_url": bases[0], "exchange_ids": ids,
                                       "identity_refs": {eid: "alice" for eid in ids}})
    identifier = imported["schema_id"]
    token = configured_secret("API_LAB_TOKEN") or configured_secret("ARSENAL_TOKEN")
    async with streamablehttp_client("http://api-lab:8010/mcp", headers={"Authorization": "Bearer " + token}) as (read, write, _):
        async with ClientSession(read, write) as mcp:
            await mcp.initialize()
            async def call(name, args):
                result = await mcp.call_tool(name, args)
                if result.isError:
                    raise RuntimeError("new traffic-schema MCP call failed: " + name)
                data = json.loads(next(item.text for item in result.content if item.type == "text"))
                if data.get("exit_state") == "tool_error":
                    raise RuntimeError(data.get("reason", "MCP call failed"))
                return data
            async def finished(job_id):
                async with asyncio.timeout(45):
                    while True:
                        status = await call("api_lab_status", {"job_id": job_id, "wait_seconds": 1})
                        if status["status"] == "finished":
                            if not status["complete"]:
                                raise RuntimeError("new traffic-schema experiment did not finish")
                            return status
            catalog = await call("api_lab_catalog", {"base_url": bases[0], "schema_id": identifier})
            operations = {(item["method"], item["path"]): item["operation_id"] for item in catalog["operations"]}
            plan = example_plan()
            names = {"createNote": ("POST", "/notes"), "getNote": ("GET", "/notes/{id}"),
                     "deleteNote": ("DELETE", "/notes/{id}"), "health": ("GET", "/health")}
            for step in plan["steps"] + plan["cleanup"]:
                step["operation_id"] = operations[names[step["operation_id"]]]
            started = await call("api_lab_start", {"base_url": bases[0], "schema_id": identifier, "plan": plan, "rps": 100})
            result = await finished(started["job_id"])
            if result["checks"]["violation"] != 1:
                raise RuntimeError("new inferred-schema fixture experiment missed its expected control")
            text, offset = "", 0
            while True:
                artifact = await call("api_lab_artifact", {"job_id": started["job_id"], "name": "reproducer-private-note", "offset": offset})
                text += artifact["text"]
                if not artifact["has_more"]:
                    break
                offset = artifact["next_offset"]
            recipe = json.loads(text)
            replay = await call("api_lab_replay_recipe", {"recipe": recipe, "base_url": bases[1], "rps": 100})
            control = await finished(replay["job_id"])
            if control["checks"]["passed"] != 1 or control["source_recipe_hash"] != digest(recipe):
                raise RuntimeError("new inferred-schema archived replay did not match")
            print(json.dumps({"extension": "traffic-to-spec", "captured_exchanges": len(ids),
                              "schema_id": identifier, "operation_count": imported["operation_count"],
                              "dependency_candidates": len(imported["dependency_candidates"]),
                              "new_job_id": started["job_id"], "new_replay_job_id": replay["job_id"],
                              "inferred_experiment": "violation", "inferred_replay": "passed"}))


if __name__ == "__main__":
    asyncio.run(main())
