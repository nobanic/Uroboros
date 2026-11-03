"""Isolated experiment process: JSONL events only; no credentials in argv."""
import json
import os
import sys
from pathlib import Path
from api_lab.engine import Experiment, Identities
from api_lab.wfc_api_lab_intake import load_reviewed_identities


def main():
    specification = json.loads(Path(sys.argv[1]).read_text())
    identity_path = os.environ["API_LAB_IDENTITIES"]
    intake = specification.get("wfc_intake")
    try:
        identities = (
            load_reviewed_identities(identity_path, intake["identity_config_sha256"])
            if intake is not None else Identities(identity_path)
        )
    except Exception as exc:
        print(json.dumps({"event":"error","reason":str(exc)},ensure_ascii=True),flush=True)
        return 1
    def emit(event):
        if event.get("event") == "artifact" and event.get("name") == "source-context":
            output = event
        else:
            data = event.get("data", {})
            source = data.get("source_context") if isinstance(data,dict) else None
            if source is not None:
                event = dict(event, data={key:value for key,value in data.items() if key != "source_context"})
            output = identities.redact(event)
            if source is not None:
                output["data"]["source_context"] = source
        print(json.dumps(output,ensure_ascii=True),flush=True)
    try:
        experiment = Experiment(specification["base_url"],specification["schema_url"],identities,emit,specification["options"],
                                schema_document=specification.get("schema_document"),source_context=specification.get("source_context"),
                                wfc_intake=specification.get("wfc_intake"),wfc_execution=specification.get("wfc_execution"))
        experiment.run(specification["plan"],specification["examples"],specification["minimize"])
    except Exception as exc:
        emit({"event":"error","reason":str(exc)})
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
