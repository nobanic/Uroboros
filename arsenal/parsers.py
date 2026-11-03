"""Parse scanner JSON into normalized records (C4 identities) with parse-error
accounting (C5). Stdlib-only. Each parser returns:
    {"records": list[dict], "parse_errors": int, "lines_total": int}
Malformed input is skipped but counted; it never crashes and never silently
becomes a clean success.
"""

import ipaddress
import json
from urllib.parse import urlsplit

from arsenal.contracts import make_record, MAX_RECORDS


def _result(records, parse_errors, lines_total):
    # in-scope dedup by cross-tool vulnerability identity (C4)
    seen, deduped, limited = set(), [], False
    for r in records:
        if r["dedup_fp"] in seen:
            continue
        seen.add(r["dedup_fp"])
        if len(deduped) >= MAX_RECORDS:
            limited = True
        else:
            deduped.append(r)
    return {"records": deduped, "parse_errors": parse_errors, "lines_total": lines_total,
            "records_limited": limited}


def parse_nuclei(text):
    records, errs, total = [], 0, 0
    for line in text.splitlines():
        line = line.strip()
        if not line:
            continue
        total += 1
        try:
            d = json.loads(line)
        except Exception:
            errs += 1
            continue
        if not isinstance(d, dict):
            errs += 1
            continue
        info = d.get("info", {}) or {}
        if not isinstance(info, dict) or not (d.get("template-id") or d.get("template_id")):
            errs += 1
            continue
        records.append(make_record(
            tool="nuclei", type="vuln",
            severity=info.get("severity", ""),
            name=info.get("name", d.get("template-id", "")),
            target=d.get("host", ""),
            endpoint=d.get("matched-at", d.get("matched_at", "")),
            template_id=d.get("template-id", d.get("template_id", "")),
            raw=line,
        ))
    return _result(records, errs, total)


def parse_httpx(text):
    records, errs, total = [], 0, 0
    for line in text.splitlines():
        line = line.strip()
        if not line:
            continue
        total += 1
        try:
            d = json.loads(line)
            if not isinstance(d, dict) or not (d.get("url") or d.get("input")):
                raise ValueError("missing probe target")
            tech = d.get("tech") or d.get("technologies") or []
            if not isinstance(tech, list) or not all(isinstance(t, str) for t in tech):
                raise ValueError("invalid technologies")
        except (ValueError, TypeError):
            errs += 1
            continue
        records.append(make_record(
            tool="httpx", type="service", severity="info",
            name=d.get("title", d.get("url", "")),
            target=d.get("url", d.get("input", "")), endpoint=d.get("url", ""),
            evidence="status=%s tech=%s" % (d.get("status_code", d.get("status", "")), ",".join(tech)),
            raw=line,
        ))
    return _result(records, errs, total)


def parse_subfinder(text):
    records, errs, total = [], 0, 0
    for line in text.splitlines():
        line = line.strip()
        if not line:
            continue
        total += 1
        try:
            data = json.loads(line)
            host = str(data.get("host", "")).strip().lower().rstrip(".")
            sources = data.get("sources") or ([data["source"]] if data.get("source") else [])
            if not host or not isinstance(sources, list) or not all(isinstance(item, str) for item in sources):
                raise ValueError("invalid subfinder result")
        except (ValueError, TypeError, KeyError):
            errs += 1
            continue
        record = make_record(
            tool="subfinder", type="subdomain", severity="info", name=host,
            target="", evidence="sources=" + ",".join(sorted(set(sources))), raw=line,
        )
        record.update(stage="subfinder", host=host)
        records.append(record)
    return _result(records, errs, total)


def parse_naabu(text):
    records, errs, total = [], 0, 0
    for line in text.splitlines():
        line = line.strip()
        if not line:
            continue
        total += 1
        try:
            data = json.loads(line)
            address = str(data.get("ip") or data.get("host") or "").strip().strip("[]")
            address = ipaddress.ip_address(address).compressed
            port = data.get("port")
            if isinstance(port, bool) or not isinstance(port, int) or not 1 <= port <= 65535:
                raise ValueError("invalid naabu port")
        except (ValueError, TypeError):
            errs += 1
            continue
        record = make_record(
            tool="naabu", type="port", severity="info", name="tcp/%d" % port,
            target="", evidence="connect=open", raw=line,
        )
        record.update(stage="naabu", address=address, port=port)
        records.append(record)
    return _result(records, errs, total)


def _extract_ffuf_json(text):
    """ffuf `-s -of json -o /dev/stdout` prints plain match lines first, then the
    JSON report object. Return the parsed report, or None if none is recoverable."""
    try:
        return json.loads(text)           # fast path: 0 silent matches -> pure JSON
    except Exception:
        pass
    brace = text.find("{")                # report starts at the first '{' after matches
    if brace <= 0:
        return None
    try:
        return json.loads(text[brace:])
    except Exception:
        return None


def parse_ffuf(text):
    text = text.strip()
    if not text:
        return _result([], 1, 0)  # ffuf must emit a JSON report, even for zero hits
    d = _extract_ffuf_json(text)
    if not isinstance(d, dict) or not isinstance(d.get("results"), list):
        return _result([], 1, len(text.splitlines()))
    results = d.get("results", []) or []
    records, errs = [], 0
    for hit in results:
        if not isinstance(hit, dict) or not isinstance(hit.get("input", {}), dict) or not hit.get("url"):
            errs += 1
            continue
        # ffuf 2.1.0's auto-calibration can mutate input.FUZZ on a genuine
        # result. Its actual request URL remains correct; use that stable
        # identity, keeping the unmodified scanner record in raw.
        try:
            word = urlsplit(hit["url"]).path.lstrip("/") or hit["url"]
        except ValueError:
            errs += 1
            continue
        records.append(make_record(
            tool="ffuf", type="content",
            severity="info",
            name=word,
            target=hit.get("url", ""),
            endpoint=hit.get("url", ""),
            evidence="status=%s len=%s" % (hit.get("status", ""), hit.get("length", "")),
            raw=json.dumps(hit),
        ))
    return _result(records, errs, len(results))
