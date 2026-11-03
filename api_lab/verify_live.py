"""Small native MCP smoke suite against the two disposable Compose fixtures."""
import asyncio
import json
import logging
import os
from pathlib import Path
from mcp import ClientSession
from mcp.client.streamable_http import streamablehttp_client
from api_lab.fixture import example_plan
from api_lab.engine import digest


async def main():
    logging.disable(logging.CRITICAL)
    summaries = []
    async with streamablehttp_client("http://127.0.0.1:8010/mcp",headers={"Authorization":"Bearer "+os.environ["API_LAB_TOKEN"]}) as (read,write,_):
        async with ClientSession(read,write) as session:
            await session.initialize()
            async def call(name,arguments):
                result = await session.call_tool(name,arguments)
                if result.isError:
                    raise RuntimeError("MCP tool failed: " + name)
                return json.loads(next(item.text for item in result.content if item.type=="text"))
            async def finished(job):
                async with asyncio.timeout(30):
                    while True:
                        result = await call("api_lab_status",{"job_id":job,"wait_seconds":5})
                        if result["status"]=="finished":
                            if not result["complete"]:
                                raise RuntimeError("experiment incomplete: "+str(result))
                            return result
            tools = await session.list_tools()
            assert len(tools.tools)==19
            base = "http://api-lab-vulnerable:9000"
            catalog = await call("api_lab_catalog",{"base_url":base,"schema_url":base+"/openapi.json"})
            assert {item["name"] for item in catalog["identities"]} >= {"alice","bob"}
            for write in (False,True):
                started = await call("api_lab_start",{"base_url":base,"schema_url":base+"/openapi.json","plan":example_plan(write=write),"rps":20})
                job = started["job_id"]
                try:
                    result = await finished(job)
                    assert result["checks"]["violation"]==1,result
                    artifact = await call("api_lab_artifact",{"job_id":job,"name":"reproducer-private-note"})
                    assert not artifact["has_more"]
                    recipe = json.loads(artifact["text"])
                    assert digest(recipe)==artifact["hash"]
                    assert recipe["oracle"]["minimized_replay_verified"]
                    replay = await call("api_lab_replay",{"job_id":job,"artifact_name":"reproducer-private-note","base_url":"http://api-lab-fixed:9000","rps":20})
                    control = await finished(replay["job_id"])
                    assert control["checks"]["passed"]==1 and control["checks"]["violation"]==0,control
                    summaries.append({"kind":"private_write" if write else "private_read","vulnerable_job":job,"vulnerable_verdict":result["checks"],
                                      "fixed_job":replay["job_id"],"fixed_verdict":control["checks"],"reproducer_hash":artifact["hash"]})
                finally:
                    if (await call("api_lab_status",{"job_id":job,"wait_seconds":0}))["status"] in ("running","cancelling"):
                        await call("api_lab_cancel",{"job_id":job})
    Path("/var/lib/api-lab/smoke-check.json").write_text(json.dumps(summaries),encoding="utf-8")
    print(json.dumps({"mcp_tools":19,"controlled_tests":summaries}),flush=True)


if __name__=="__main__":
    asyncio.run(main())
