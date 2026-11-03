"""Frozen source snapshots, local Semgrep jobs, and durable source context.

Static matches are candidates. Only the normal API experiment's explicit
controls/oracles assess runtime behavior; source attachment never confirms it.
"""
import asyncio
import base64
import copy
import hashlib
import json
import os
import tempfile
import threading
import time
import uuid
from pathlib import Path

import yaml

from arsenal.execmgr import Runner
from api_lab.engine import digest


ACTIVE = ("snapshotting", "scanning", "cancelling")
FORMAT = "uroboros-source-context-v1"


def byte_hash(raw):
    return hashlib.sha256(raw).hexdigest()


def validate_context(document):
    if not isinstance(document, dict) or document.get("format") != FORMAT or not document.get("files"):
        raise ValueError("invalid source context")
    for file in document["files"]:
        raw = base64.b64decode(file["slice_base64"], validate=True)
        if byte_hash(raw) != file["slice_sha256"] or raw.decode("utf-8", errors="replace") != file["text"]:
            raise ValueError("source slice integrity check failed")
    return document


class SourceScans:
    def __init__(self, jobs, projects=None, binary=None):
        self.jobs = jobs
        self.projects = projects
        self.binary = binary or os.environ.get("API_LAB_SEMGREP", "/opt/semgrep/bin/semgrep")
        self.active = {}
        with jobs.lock:
            jobs.db.execute("CREATE TABLE IF NOT EXISTS source_scans (id TEXT PRIMARY KEY, document TEXT NOT NULL, output TEXT)")
            jobs.db.execute("CREATE TABLE IF NOT EXISTS source_files (scan TEXT, path TEXT, hash TEXT, body BLOB, PRIMARY KEY(scan,path))")
            jobs.db.execute("CREATE TABLE IF NOT EXISTS source_contexts (hash TEXT PRIMARY KEY, document TEXT NOT NULL)")
            for row in jobs.db.execute("SELECT id,document FROM source_scans").fetchall():
                doc = json.loads(row[1])
                if doc["status"] in ACTIVE:
                    doc.update(status="interrupted", complete=False, reason="Service restarted; scan was not resumed", finished_at=time.time())
                    self._save(doc)
            jobs.db.commit()

    def configured_projects(self):
        if self.projects is not None:
            return self.projects
        path = os.environ.get("API_LAB_SOURCE_PROJECTS", "")
        if not path:
            raise ValueError("operator must configure API_LAB_SOURCE_PROJECTS")
        return json.loads(Path(path).read_text(encoding="utf-8"))["projects"]

    def describe(self):
        return {"projects": [{"name": name, "default_paths": config.get("default_paths", ["."]),
                              "extensions": config.get("extensions", []), "revision_label": config.get("revision_label", "unspecified")}
                             for name, config in self.configured_projects().items()],
                "engine": "Semgrep Community Edition", "result_kind": "static_candidates_not_runtime_proof",
                "rules": "Local starter rules, or an explicit Semgrep rules object supplied to source_scan"}

    def _save(self, doc, output=None):
        self.jobs.db.execute("INSERT INTO source_scans VALUES (?,?,?) ON CONFLICT(id) DO UPDATE SET document=excluded.document, output=COALESCE(excluded.output,source_scans.output)",
                             (doc["scan_id"], json.dumps(doc), json.dumps(output) if output is not None else None))

    def _get(self, scan_id):
        row = self.jobs.db.execute("SELECT document,output FROM source_scans WHERE id=?", (scan_id,)).fetchone()
        if row is None:
            raise ValueError("unknown source scan_id")
        return json.loads(row[0]), json.loads(row[1]) if row[1] else None

    def start(self, project, paths=None, rules=None, budget_seconds=300, timeout_per_rule=5, max_source_bytes=50*1024*1024, output_bytes=4*1024*1024):
        projects = self.configured_projects()
        if project not in projects:
            raise ValueError("unknown configured source project")
        config = copy.deepcopy(projects[project])
        root = Path(config["root"]).resolve()
        if not root.is_dir():
            raise ValueError("configured source root is unavailable")
        paths = config.get("default_paths", ["."]) if paths is None else paths
        if not isinstance(paths, list) or not paths or any(not isinstance(path, str) or not path for path in paths):
            raise ValueError("paths must select source files or directories")
        for path in paths:
            selected = Path(path)
            if selected.is_absolute() or ".." in selected.parts:
                raise ValueError("source paths must be relative to the configured project")
            if not (root/selected).resolve().is_relative_to(root) or not (root/selected).exists():
                raise ValueError("selected source path is unavailable or outside the configured project")
        if rules is None:
            rules = yaml.safe_load(Path(__file__).with_name("source_rules.yml").read_text())
        if not isinstance(rules, dict) or not isinstance(rules.get("rules"), list) or not rules["rules"]:
            raise ValueError("rules must contain a nonempty Semgrep rules array")
        if budget_seconds <= 0 or timeout_per_rule < 0 or max_source_bytes < 0 or output_bytes <= 0:
            raise ValueError("positive job/output budgets; nonnegative rule timeout/source byte budget")
        with self.jobs.lock:
            if self.active:
                return {"exit_state": "busy", "complete": False, "scan_id": next(iter(self.active))}
            scan_id = uuid.uuid4().hex
            doc = {"scan_id": scan_id, "project": project, "status": "snapshotting", "complete": False,
                   "started_at": time.time(), "selected_paths": paths, "rules_hash": digest(rules),
                   "revision_label": config.get("revision_label", "unspecified"), "rules_source": "local_starter" if rules == yaml.safe_load(Path(__file__).with_name("source_rules.yml").read_text()) else "supplied_rules",
                   "coverage": "selected_snapshot_and_rules_only", "result_kind": "static_candidates_not_runtime_proof"}
            self._save(doc)
            self.jobs.db.commit()
            stop = threading.Event()
            thread = threading.Thread(target=self._work, args=(scan_id, root, config, paths, rules, budget_seconds, timeout_per_rule, max_source_bytes, output_bytes, stop), daemon=True)
            self.active[scan_id] = (stop, thread)
            try:
                thread.start()
            except Exception:
                self.active.pop(scan_id,None)
                doc.update(status="finished",complete=False,exit_state="tool_error",reason="source worker could not start")
                self._save(doc)
                self.jobs.db.commit()
                raise
            return dict(doc)

    def _snapshot(self, scan_id, root, config, paths, destination, max_bytes, stop, deadline):
        candidates = set()
        excluded = set(config.get("exclude_directories", []))
        extensions = set(config.get("extensions", []))
        skipped = []
        for selection in paths:
            selected = root/selection
            if selected.is_file():
                candidates.add(selected)
            else:
                for directory, dirs, files in os.walk(selected, followlinks=False):
                    dirs[:] = sorted(d for d in dirs if d not in excluded and not (Path(directory)/d).is_symlink())
                    candidates.update(Path(directory)/name for name in files)
        manifest, total = {}, 0
        for file in sorted(candidates):
            if stop.is_set() or time.monotonic() >= deadline:
                raise InterruptedError("snapshot cancelled or job budget exhausted")
            relative = file.relative_to(root).as_posix()
            if file.is_symlink() or not file.resolve().is_relative_to(root):
                skipped.append({"path": relative, "reason": "symlink_not_snapshotted"})
                continue
            if extensions and file.suffix not in extensions:
                continue
            with file.open("rb") as stream:
                raw = stream.read(max_bytes-total+1) if max_bytes else stream.read()
            total += len(raw)
            if max_bytes and total > max_bytes:
                raise ValueError("source snapshot exceeds max_source_bytes; increase it or narrow paths")
            copied = destination/relative
            copied.parent.mkdir(parents=True, exist_ok=True)
            copied.write_bytes(raw)
            copied.chmod(0o400)
            hash = byte_hash(raw)
            manifest[relative] = hash
            with self.jobs.lock:
                self.jobs.db.execute("INSERT INTO source_files VALUES (?,?,?,?)", (scan_id, relative, hash, raw))
                self.jobs.db.commit()
        if not manifest:
            raise ValueError("selection contains no source files matching configured extensions")
        return manifest, total, skipped

    def _work(self, scan_id, root, config, paths, rules, budget, rule_timeout, max_bytes, output_bytes, stop):
        deadline = time.monotonic()+budget
        try:
            with tempfile.TemporaryDirectory(prefix="source-", dir=self.jobs.directory) as temp:
                snapshot = Path(temp)/"snapshot"
                snapshot.mkdir()
                manifest, size, skipped = self._snapshot(scan_id, root, config, paths, snapshot, max_bytes, stop, deadline)
                with self.jobs.lock:
                    doc, _ = self._get(scan_id)
                    doc.update(status="scanning", source_revision=digest(manifest), file_manifest=manifest, snapshot_bytes=size, skipped=skipped)
                    self._save(doc)
                    self.jobs.db.commit()
                rule_file = Path(temp)/"rules.yml"
                rule_file.write_text(yaml.safe_dump(rules), encoding="utf-8")
                run = Runner().run([self.binary, "scan", "--oss-only", "--json", "--dataflow-traces", "--metrics", "off",
                                    "--disable-version-check", "--no-rewrite-rule-ids", "--no-git-ignore", "--jobs", "1",
                                    "--timeout", str(rule_timeout), "--max-target-bytes", "0", "--config", str(rule_file), str(snapshot)],
                                   max(0.01, deadline-time.monotonic()), cap=output_bytes, cancel_event=stop)
                try:
                    output = json.loads(run["stdout"])
                except ValueError:
                    output = {"results": [], "errors": [{"message": "Semgrep did not produce a complete JSON document"}]}
                # Paths identify frozen project files, never an ephemeral staging directory.
                def normalize(value):
                    if isinstance(value, dict):
                        return {key: normalize(item) for key, item in value.items()}
                    if isinstance(value, list):
                        return [normalize(item) for item in value]
                    if isinstance(value, str):
                        return value.replace(str(snapshot)+"/", "")
                    return value
                output = normalize(output)
                candidates = []
                for match in output.get("results", []):
                    if match.get("path") not in manifest:
                        raise ValueError("Semgrep returned a candidate outside the frozen snapshot")
                    candidate = {"rule_id": match["check_id"], "path": match["path"], "start": match["start"], "end": match["end"],
                                 "file_sha256": manifest[match["path"]], "extra": match.get("extra", {}), "status": "static_candidate"}
                    candidate["candidate_id"] = digest({key: candidate[key] for key in ("rule_id", "path", "start", "end", "file_sha256")}|{"rules_hash": digest(rules)})
                    candidates.append(candidate)
                with self.jobs.lock:
                    doc, _ = self._get(scan_id)
                    doc.update(status="finished", complete=run["exit_state"] == "ok" and not output.get("errors") and not skipped,
                               exit_state=run["exit_state"], finished_at=time.time(), candidate_count=len(candidates),
                               engine_version=output.get("version", "unknown"), errors=output.get("errors", []),
                               scanned_paths=output.get("paths", {}).get("scanned", []), truncated=run["truncated"],
                               diagnostic=normalize(run["stderr"]))
                    self._save(doc, {"candidates": candidates, "semgrep": output, "rules": rules})
                    self.jobs.db.commit()
        except Exception as exc:
            with self.jobs.lock:
                doc, _ = self._get(scan_id)
                doc.update(status="finished", complete=False, exit_state="cancelled" if stop.is_set() else "tool_error", finished_at=time.time(), reason=str(exc))
                self._save(doc)
                self.jobs.db.commit()
        finally:
            with self.jobs.lock:
                self.active.pop(scan_id, None)

    def status(self, scan_id):
        with self.jobs.lock:
            doc, _ = self._get(scan_id)
            return self._view(doc)

    def _view(self, doc):
        result = {key:value for key,value in doc.items() if key not in ("file_manifest", "diagnostic")}
        result["file_count"] = len(doc.get("file_manifest",{}))
        result["diagnostic"] = doc.get("diagnostic", "")[-2000:]
        return result

    async def wait_status(self, scan_id, wait_seconds=1):
        if not 0 <= wait_seconds <= 20:
            raise ValueError("wait_seconds 0..20")
        end = time.monotonic()+wait_seconds
        while True:
            doc = self.status(scan_id)
            if doc["status"] not in ACTIVE or time.monotonic() >= end:
                return doc
            await asyncio.sleep(0.1)

    def cancel(self, scan_id):
        with self.jobs.lock:
            doc, _ = self._get(scan_id)
            if scan_id in self.active:
                self.active[scan_id][0].set()
                doc["status"] = "cancelling"
                self._save(doc)
                self.jobs.db.commit()
            return doc

    def results(self, scan_id, offset=0, limit=20):
        if offset < 0 or not 1 <= limit <= 100:
            raise ValueError("offset nonnegative; limit 1..100")
        with self.jobs.lock:
            doc, output = self._get(scan_id)
            candidates = (output or {}).get("candidates", [])
            entries = [{key: candidate[key] for key in ("candidate_id", "rule_id", "path", "start", "end", "file_sha256", "status")}
                       |{"message": candidate["extra"].get("message", ""), "severity": candidate["extra"].get("severity", "")} for candidate in candidates[offset:offset+limit]]
            return {"scan": self._view(doc), "entries": entries, "next_offset": min(len(candidates), offset+limit), "has_more": offset+limit<len(candidates)}

    def slice(self, scan_id, candidate_id, context_lines=20, related_paths=None):
        if context_lines < 0:
            raise ValueError("context_lines must be nonnegative")
        with self.jobs.lock:
            scan, output = self._get(scan_id)
            if scan["status"] in ACTIVE or not output:
                raise ValueError("source context requires a terminal scan with candidate evidence")
            candidate = next((c for c in output["candidates"] if c["candidate_id"] == candidate_id), None)
            if candidate is None:
                raise ValueError("unknown candidate_id for this source scan")
            files = []
            for path in dict.fromkeys([candidate["path"]]+(related_paths or [])):
                row = self.jobs.db.execute("SELECT hash,body FROM source_files WHERE scan=? AND path=?", (scan_id, path)).fetchone()
                if row is None or byte_hash(row[1]) != row[0]:
                    raise ValueError("requested frozen source file is unavailable or corrupted")
                lines = row[1].splitlines(keepends=True)
                start = max(1, candidate["start"]["line"]-context_lines) if path == candidate["path"] else 1
                end = min(len(lines), candidate["end"]["line"]+context_lines) if path == candidate["path"] else len(lines)
                raw = b"".join(lines[start-1:end])
                files.append({"path": path, "file_sha256": row[0], "start_line": start, "end_line": end,
                              "text": raw.decode("utf-8", errors="replace"), "slice_sha256": byte_hash(raw),
                              "slice_base64": base64.b64encode(raw).decode("ascii")})
            context = {"format": FORMAT, "scan": scan, "rules": output["rules"], "candidate": candidate,
                       "files": files, "claim_status": "static_candidate_not_runtime_proof",
                       "deployment_mapping": "not_established_by_source_scan"}
            validate_context(context)
            identifier = digest(context)
            self.jobs.db.execute("INSERT OR IGNORE INTO source_contexts VALUES (?,?)", (identifier, json.dumps(context, sort_keys=True,separators=(",",":"),ensure_ascii=True)))
            self.jobs.db.commit()
            return {"source_context_id": identifier, "hash": identifier, "candidate_id": candidate_id,
                    "source_revision": scan["source_revision"], "files": [{key: file[key] for key in ("path", "file_sha256", "start_line", "end_line")} for file in files],
                    "next_action": "Read api_lab_source_context, then build explicit runtime checks and pass source_context_id to api_lab_start. Static candidates do not confirm findings."}

    def context(self, identifier):
        with self.jobs.lock:
            row = self.jobs.db.execute("SELECT document FROM source_contexts WHERE hash=?", (identifier,)).fetchone()
        if row is None:
            raise ValueError("unknown source_context_id")
        document = json.loads(row[0])
        if digest(document) != identifier:
            raise ValueError("source context hash mismatch")
        return validate_context(document)

    def context_page(self, identifier, offset=0, limit=8000):
        if offset < 0 or not 1 <= limit <= 16000:
            raise ValueError("offset nonnegative; limit 1..16000")
        text = json.dumps(self.context(identifier), sort_keys=True,separators=(",",":"),ensure_ascii=True)
        return {"source_context_id": identifier, "hash": identifier, "text": text[offset:offset+limit], "offset": offset,
                "next_offset": min(len(text), offset+limit), "has_more": offset+limit<len(text), "length": len(text)}
