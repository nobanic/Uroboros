"""Explicit scan selection. Broad scanning remains available as profile=full."""

import json
import os
from pathlib import Path

from arsenal import contracts as c


LIGHT_TEMPLATES = (
    "http/technologies/tech-detect.yaml",
    "http/technologies/owasp-juice-shop-detected.yaml",
    "http/exposures/configs/prometheus-metrics.yaml",
    "http/miscellaneous/dir-listing.yaml",
)


def command(target, severity="", tags="", profile="full", template_ids="", template_dir=None,
            task_bound=False, proxy_url=""):
    if profile not in ("light", "full"):
        raise ValueError("profile must be light or full")
    caps = c.CAPS["nuclei"]
    concurrency = 1 if profile == "light" else caps["conc"]
    argv = ["nuclei", "-u", target, "-jsonl", "-no-interactsh", "-silent", "-duc", "-no-stdin",
            "-stats-json", "-stats-interval", "5", "-no-color",
            "-rl", str(caps["rps"]), "-c", str(concurrency)]
    meta = {"profile": profile, "selection": "all_available", "execution_budget_seconds": caps["timeout"],
            "template_concurrency": concurrency}
    root = Path(template_dir or os.environ.get("NUCLEI_TEMPLATE_DIR", "/root/nuclei-templates"))
    if not root.is_dir():
        raise ValueError("pinned Nuclei template corpus is missing")
    selected_root = root / "http" if task_bound else root
    if not selected_root.is_dir():
        raise ValueError("pinned HTTP template corpus is missing")
    if template_ids.strip():
        argv += ["-id", template_ids.strip()]
        meta.update(selection="template_ids", template_ids=template_ids.strip())
        argv += ["-t", str(selected_root)]
        meta["template_root"] = str(selected_root)
    elif profile == "light":
        files = [root / name for name in LIGHT_TEMPLATES]
        missing = [name for name, file in zip(LIGHT_TEMPLATES, files) if not file.is_file()]
        if missing:
            raise ValueError("light templates missing: " + ", ".join(missing))
        argv += ["-t", ",".join(str(file) for file in files)]
        meta.update(selection="light_templates", selected_templates=list(LIGHT_TEMPLATES))
    else:
        argv += ["-t", str(selected_root)]
        selection = "pinned_http_corpus" if task_bound else "pinned_corpus"
        meta.update(selection=selection, template_root=str(selected_root))
    if task_bound:
        if not proxy_url:
            raise ValueError("task-bound Nuclei requires a recording proxy")
        # HTTP-only plus no Interactsh means code/DNS/TCP/TLS/OAST templates are
        # never loaded. -proxy-internal prevents auxiliary HTTP requests from
        # escaping the same fail-closed recording proxy.
        argv += ["-pt", "http", "-p", proxy_url, "-proxy-internal"]
        meta.update(protocols=["http"], oast=False, code_templates=False, proxy_enforced=True)
    if profile == "light":
        argv += ["-timeout", "3", "-retries", "0", "-omit-raw"]
        meta["execution_budget_seconds"] = 60
        meta["raw_http_included"] = False
    else:
        # Retain matcher metadata/extractors, not full response bodies for every
        # observation; bounded output must support long scans as well.
        argv += ["-omit-raw"]
        meta["raw_http_included"] = False
    if severity:
        argv += ["-severity", severity]
    if tags:
        argv += ["-tags", tags]
    return argv, meta


def progress(stderr):
    """Retain the final statistics sample, including requests that failed.

    Stats JSON may follow a logger message on the same line. Keep stderr intact
    for diagnostics; these samples are not vulnerability observation records.
    """
    latest = None
    for line in stderr.splitlines():
        start = line.find("{")
        if start < 0:
            continue
        try:
            sample = json.loads(line[start:])
        except ValueError:
            continue
        if isinstance(sample, dict) and all(key in sample for key in ("requests", "total", "templates")):
            latest = sample
    return latest
