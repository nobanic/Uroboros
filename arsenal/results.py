"""Completion semantics shared by the MCP server and dependency-free tests."""

from arsenal import contracts as c


def finalize(tool, result, parsed=None):
    state = result["exit_state"]
    records = parsed["records"] if parsed else []
    stats = {"truncated": result["truncated"]}
    if result["stderr"] or state != c.OK:
        stats["stderr"] = result["stderr"][:2000]
    if parsed:
        stats.update({key: parsed[key] for key in ("parse_errors", "lines_total", "records_limited")})
        if state == c.OK:
            if parsed["parse_errors"] or parsed["records_limited"]:
                state = c.PARTIAL if records else c.PARSE_ERROR
            elif not records:
                state = c.OK_EMPTY
    else:
        stats["stderr"] = result["stderr"][:2000]
    return {"tool": tool, "exit_state": state, "complete": state in (c.OK, c.OK_EMPTY),
            "records": records, "stats": stats}
