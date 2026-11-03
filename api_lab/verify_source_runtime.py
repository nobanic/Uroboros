"""New source -> runtime -> original evidence/replay smoke, without an LLM."""
import asyncio
import json
import logging
import os

from mcp import ClientSession
from mcp.client.streamable_http import streamablehttp_client

from api_lab.engine import digest
from api_lab.fixture import example_plan
from api_lab.register import configured_secret
from api_lab.source_scan import validate_context


async def main():
    logging.getLogger("httpx").setLevel(logging.WARNING)
    logging.getLogger("mcp").setLevel(logging.WARNING)
    token = configured_secret("API_LAB_TOKEN") or configured_secret("ARSENAL_TOKEN")
    bases = ["http://api-lab-vulnerable:9000", "http://api-lab-fixed:9000"]
    async with streamablehttp_client("http://api-lab:8010/mcp", headers={"Authorization": "Bearer "+token}) as (read, write, _):
        async with ClientSession(read, write) as session:
            await session.initialize()
            async def call(name, args):
                result = await session.call_tool(name, args)
                if result.isError:
                    raise RuntimeError("MCP call failed: "+name)
                data = json.loads(next(item.text for item in result.content if item.type == "text"))
                if data.get("exit_state") in ("tool_error", "busy"):
                    raise RuntimeError("New source/runtime smoke failed: "+data.get("reason", data["exit_state"]))
                return data
            async def scan_finished(scan_id, require_complete=True):
                async with asyncio.timeout(120):
                    while True:
                        status = await call("api_lab_source_status", {"scan_id": scan_id, "wait_seconds": 1})
                        if status["status"] == "finished":
                            if require_complete and not status["complete"]:
                                raise RuntimeError("new source scan is incomplete")
                            return status
            async def job_finished(job_id):
                async with asyncio.timeout(45):
                    while True:
                        status = await call("api_lab_status", {"job_id": job_id, "wait_seconds": 1})
                        if status["status"] == "finished":
                            if not status["complete"]:
                                raise RuntimeError("new runtime experiment is incomplete")
                            return status
            async def page_document(tool, args):
                text, offset = "", 0
                while True:
                    page = await call(tool, dict(args, offset=offset))
                    text += page["text"]
                    if not page["has_more"]:
                        break
                    offset = page["next_offset"]
                document = json.loads(text)
                if digest(document) != page["hash"]:
                    raise RuntimeError("evidence page hash mismatch")
                return document
            tools = {tool.name for tool in (await session.list_tools()).tools}
            assert "api_lab_source_scan" in tools
            started = await call("api_lab_source_scan", {"project": "uroboros", "paths": ["api_lab/fixture.py"]})
            scan = await scan_finished(started["scan_id"])
            candidates = await call("api_lab_source_results", {"scan_id": started["scan_id"]})
            candidate = candidates["entries"][0]
            selected = await call("api_lab_source_slice", {"scan_id": started["scan_id"], "candidate_id": candidate["candidate_id"], "context_lines": 1000})
            identifier = selected["source_context_id"]
            source = await page_document("api_lab_source_context", {"source_context_id": identifier})
            validate_context(source)
            plan = example_plan()
            plan["source_hypothesis"] = {"candidate_id": candidate["candidate_id"], "claim": "Review the owner check in the selected fixture deployment and test its owner-only policy with independent owner/peer/anonymous controls", "operation_id": "getNote", "deployment_basis": "operator_owned_fixture_source_and_explicit_runtime_mode"}
            experiment = await call("api_lab_start", {"base_url": bases[0], "schema_url": bases[0]+"/openapi.json", "source_context_id": identifier, "plan": plan, "rps": 100})
            runtime = await job_finished(experiment["job_id"])
            assert runtime["checks"]["violation"] == 1
            exported = await page_document("api_lab_export", {"job_id": experiment["job_id"]})
            original_source = next(a for a in exported["artifacts"] if a["name"] == "source-context")
            assert original_source["hash"] == identifier and digest(original_source["data"]) == identifier
            assert any(e["event"] == "request" for e in exported["events"])
            recipe = next(a["data"] for a in exported["artifacts"] if a["name"] == "reproducer-private-note")
            assert digest(recipe["source_context"]) == identifier
            replay = await call("api_lab_replay_recipe", {"recipe": recipe, "base_url": bases[1], "rps": 100})
            fixed = await job_finished(replay["job_id"])
            assert fixed["checks"]["passed"] == 1 and fixed["source_context_hash"] == identifier
            # Also exercise the configured Go/TS selections; matches stay static candidates.
            default = await call("api_lab_source_scan", {"project": "uroboros"})
            code = await scan_finished(default["scan_id"], require_complete=False)
            if code["exit_state"] != "ok":
                raise RuntimeError("console static scan process failed")
            go_scan = await call("api_lab_source_scan", {"project": "uroboros", "paths": ["agent", "server"]})
            go = await scan_finished(go_scan["scan_id"])
            print(json.dumps({"extension": "source-to-runtime", "mcp_tools": len(tools), "engine_version": scan["engine_version"],
                              "source_scan_id": started["scan_id"], "source_context_id": identifier,
                              "new_job_id": experiment["job_id"], "new_replay_job_id": replay["job_id"],
                              "runtime": "violation", "replay": "passed", "source_and_http_evidence_exported": True,
                              "console_scan_id": default["scan_id"], "console_file_count": code["file_count"],
                              "console_static_candidates": code["candidate_count"], "console_complete": code["complete"],
                              "console_parse_diagnostics": len(code.get("errors",[])),
                              "go_scan_id": go_scan["scan_id"], "go_file_count": go["file_count"], "go_complete": go["complete"]}))


if __name__ == "__main__":
    asyncio.run(main())
