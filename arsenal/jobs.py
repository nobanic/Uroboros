"""Durable Nuclei jobs, independent of an MCP session or HTTP request lifetime.

One active Nuclei job; no unbounded queue. SQLite stores incremental observations
and bounded history. A restart preserves evidence and marks active jobs interrupted;
it does not pretend to resume a killed scanner or repeat target requests.
"""

import asyncio
import json
import sqlite3
import threading
import time
import uuid
from pathlib import Path

from arsenal import contracts as c, nuclei, parsers
from arsenal.diagnostics import ErrorLogCapture, error_record
from arsenal.results import finalize


ACTIVE = ("running", "cancelling")


class NucleiJobs:
    def __init__(self, path, executor, history_limit=100):
        Path(path).parent.mkdir(parents=True, exist_ok=True)
        self._db = sqlite3.connect(path, check_same_thread=False)
        self._db.execute("PRAGMA journal_mode=WAL")
        self._db.execute("CREATE TABLE IF NOT EXISTS jobs (id TEXT PRIMARY KEY, document TEXT NOT NULL)")
        self._db.execute("CREATE TABLE IF NOT EXISTS observations (job TEXT, seq INTEGER, fingerprint TEXT, document TEXT, PRIMARY KEY(job,fingerprint), UNIQUE(job,seq))")
        self._db.execute("CREATE TABLE IF NOT EXISTS diagnostics (job TEXT, seq INTEGER, stream TEXT, category TEXT, protocol TEXT, document TEXT, PRIMARY KEY(job,seq))")
        self._artifact_dir = Path(path).resolve().parent / "job-artifacts"
        self._artifact_dir.mkdir(parents=True, exist_ok=True)
        self._lock = threading.RLock()
        self._executor = executor
        self._active = {}
        self._history_limit = history_limit
        with self._lock:
            for doc in self._all():
                if doc["status"] in ACTIVE:
                    doc.update(status="interrupted", exit_state="interrupted", complete=False, finished_at=time.time())
                    doc["stats"]["reason"] = "Arsenal restarted; scanner was not resumed. Preserved observations are partial."
                    self._save(doc)
                    self._remove_error_file(doc["job_id"])
            self._db.commit()

    def _all(self):
        documents = [json.loads(row[0]) for row in self._db.execute("SELECT document FROM jobs")]
        return sorted(documents, key=lambda doc:doc["started_at"], reverse=True)

    def _get(self, job_id):
        row = self._db.execute("SELECT document FROM jobs WHERE id=?", (job_id,)).fetchone()
        if not row:
            raise ValueError("unknown job_id")
        return json.loads(row[0])

    def _save(self, doc):
        self._db.execute("INSERT OR REPLACE INTO jobs VALUES (?,?)", (doc["job_id"], json.dumps(doc)))

    def _error_path(self, job_id):
        if len(job_id) != 32 or any(char not in "0123456789abcdef" for char in job_id):
            raise ValueError("invalid artifact job ID")
        return self._artifact_dir / (job_id + ".errors.jsonl")

    def _remove_error_file(self, job_id):
        # Exact file only; caller-supplied paths and recursive removal are absent.
        if len(job_id) == 32 and all(char in "0123456789abcdef" for char in job_id):
            self._error_path(job_id).unlink(missing_ok=True)

    def _diagnostic_summary(self, doc):
        capture = doc["stats"].get("diagnostic_capture", "unavailable")
        summary = {"capture":capture, "error_records":0, "stderr_records":0,
                   "categories":{}, "protocols":{}, "truncated":doc["stats"].get("diagnostics_truncated", False)}
        summary["capture_complete"] = (capture == "enabled" and doc["status"] == "finished"
                                       and not summary["truncated"] and not doc["stats"].get("diagnostic_failure")
                                       and not doc["stats"].get("truncated"))
        for stream, category, count in self._db.execute("SELECT stream,category,COUNT(*) FROM diagnostics WHERE job=? GROUP BY stream,category",(doc["job_id"],)):
            summary["error_records" if stream == "errors" else "stderr_records"] += count
            if stream == "errors":
                summary["categories"][category] = count
        summary["protocols"] = dict(self._db.execute("SELECT protocol,COUNT(*) FROM diagnostics WHERE job=? AND stream='errors' GROUP BY protocol",(doc["job_id"],)))
        if capture == "unavailable":
            summary["reason"] = "This job predates diagnostic capture; missing logs cannot be reconstructed."
        summary["note"] = "Error-log entries and the engine errors counter differ. Categories are hints, not proof of blocking or scanner bugs."
        return summary

    def _diagnostic_line(self, job_id, stream, line):
        record = error_record(line) if stream == "errors" else {"category":"stderr", "protocol":"", "text":line}
        with self._lock:
            sequence = self._db.execute("SELECT COALESCE(MAX(seq)+1,0) FROM diagnostics WHERE job=?",(job_id,)).fetchone()[0]
            self._db.execute("INSERT INTO diagnostics VALUES (?,?,?,?,?,?)",(job_id,sequence,stream,record["category"],record["protocol"],json.dumps(record)))
            self._db.commit()

    def _view(self, doc):
        result = dict(doc)
        result["stats"] = dict(doc["stats"], diagnostics=self._diagnostic_summary(doc))
        result["records"] = []
        result["record_count"] = self._db.execute("SELECT COUNT(*) FROM observations WHERE job=?", (doc["job_id"],)).fetchone()[0]
        result["elapsed_seconds"] = round((doc.get("finished_at") or time.time()) - doc["started_at"], 2)
        if doc["status"] in ACTIVE:
            result["poll_after_seconds"] = 10
            result["next_action"] = "Call nuclei_job_status with wait_seconds=10, or nuclei_job_results for available observations. Do not start the same scan again."
        return result

    def start(self, target, argv, metadata, budget):
        with self._lock:
            if self._active:
                existing_id = next(iter(self._active))
                return {"tool":"nuclei", "exit_state":c.BUSY, "complete":False, "records":[],
                        "job_id":existing_id, "stats":{"reason":"Nuclei already has an active job; inspect it instead of retrying."}}
            # Bound completed history; evidence and job metadata are pruned together.
            for old in self._all()[self._history_limit - 1:]:
                self._db.execute("DELETE FROM observations WHERE job=?", (old["job_id"],))
                self._db.execute("DELETE FROM jobs WHERE id=?", (old["job_id"],))
                self._db.execute("DELETE FROM diagnostics WHERE job=?", (old["job_id"],))
                self._remove_error_file(old["job_id"])
            job_id = uuid.uuid4().hex
            doc = {"tool":"nuclei", "job_id":job_id, "target":target, "status":"running",
                   "exit_state":"running", "complete":False, "started_at":time.time(),
                   "stats":dict(metadata, execution_budget_seconds=budget, execution="background", diagnostic_capture="enabled")}
            self._save(doc)
            self._db.commit()
            stop = threading.Event()
            thread = threading.Thread(target=self._work, args=(job_id, argv, budget, stop), daemon=True)
            self._active[job_id] = (stop, thread)
            try:
                thread.start()
            except Exception:
                self._active.pop(job_id)
                doc.update(status="finished", exit_state=c.TOOL_ERROR, finished_at=time.time())
                doc["stats"]["reason"] = "worker thread could not start"
                self._save(doc)
                self._db.commit()
                raise
            return self._view(doc)

    def _line(self, job_id, stream, line):
        if not line.strip():
            return
        with self._lock:
            if stream == "stdout":
                parsed = parsers.parse_nuclei(line)
                count = self._db.execute("SELECT COUNT(*) FROM observations WHERE job=?", (job_id,)).fetchone()[0]
                for record in parsed["records"]:
                    if count >= c.MAX_RECORDS:
                        break
                    cursor = self._db.execute("INSERT OR IGNORE INTO observations VALUES (?,?,?,?)",
                                              (job_id, count, record["dedup_fp"], json.dumps(record)))
                    count += cursor.rowcount
            else:
                # Archive full bounded stderr, including diagnostics after the
                # compact 2000-character preview returned to the model.
                self._diagnostic_line(job_id, "stderr", line)
                progress = nuclei.progress(line)
                if progress is not None:
                    doc = self._get(job_id)
                    doc["stats"]["progress"] = progress
                    self._save(doc)
            self._db.commit()

    def _work(self, job_id, argv, budget, stop):
        capture = None
        try:
            error_path = self._error_path(job_id)
            error_path.touch(exist_ok=False)
            capture = ErrorLogCapture(error_path, stop, lambda line:self._diagnostic_line(job_id, "errors", line))
            capture.start()
            argv = list(argv) + ["-elog", str(error_path)]
            result = self._executor.run_sync("nuclei", argv, budget, cancel_event=stop,
                                             on_line=lambda stream, line:self._line(job_id, stream, line))
            capture.finish()
            parsed = parsers.parse_nuclei(result["stdout"])
            final = finalize("nuclei", result, parsed)
            if capture.truncated or capture.failure:
                final.update(exit_state=c.TOOL_ERROR, complete=False)
                final["stats"].update(diagnostics_truncated=capture.truncated,
                                     diagnostic_failure=capture.failure or "error-log byte cap reached; scan stopped")
            # Also supports executors that do not emit callbacks. Dedup keeps
            # final reconciliation consistent with already published evidence.
            for record in parsed["records"]:
                self._line(job_id, "stdout", record["raw"])
            with self._lock:
                doc = self._get(job_id)
                doc.update(status="finished", exit_state=final["exit_state"], complete=final["complete"], finished_at=time.time())
                doc["stats"].update(final["stats"])
                progress = nuclei.progress(result["stderr"])
                if progress is not None:
                    doc["stats"]["progress"] = progress
                self._save(doc)
                self._db.commit()
        except Exception as exc:
            with self._lock:
                doc = self._get(job_id)
                doc.update(status="finished", exit_state=c.TOOL_ERROR, complete=False, finished_at=time.time())
                doc["stats"]["reason"] = str(exc)
                self._save(doc)
                self._db.commit()
        finally:
            try:
                if capture and capture._thread.is_alive():
                    stop.set()
                    capture.finish()
                self._remove_error_file(job_id)
            finally:
                with self._lock:
                    self._active.pop(job_id, None)

    def diagnostics(self, job_id, offset=0, limit=20, stream="errors", category="", text_offset=0):
        if offset < 0 or text_offset < 0 or not 1 <= limit <= 100 or stream not in ("errors", "stderr", "all"):
            raise ValueError("offset/text_offset must be nonnegative; limit 1..100; stream errors/stderr/all")
        with self._lock:
            result = self._view(self._get(job_id))
            where, parameters = "job=?", [job_id]
            if stream != "all":
                where += " AND stream=?"
                parameters.append(stream)
            if category:
                where += " AND category=?"
                parameters.append(category)
            total = self._db.execute("SELECT COUNT(*) FROM diagnostics WHERE " + where, parameters).fetchone()[0]
            rows = self._db.execute("SELECT seq,stream,document FROM diagnostics WHERE " + where + " ORDER BY seq LIMIT ? OFFSET ?", parameters + [limit, offset]).fetchall()
            entries = []
            for sequence, source, document in rows:
                record = json.loads(document)
                next_text_offset = min(len(record["text"]),text_offset+2000)
                # Raw lines are fully archived within the stream byte cap;
                # return bounded excerpts to protect the model context.
                entries.append({"sequence":sequence, "stream":source, "category":record["category"],
                                "protocol":record["protocol"], "text":record["text"][text_offset:next_text_offset],
                                "text_truncated":text_offset > 0 or next_text_offset < len(record["text"]),
                                "text_offset":text_offset, "next_text_offset":next_text_offset,
                                "text_has_more":next_text_offset < len(record["text"]), "text_length":len(record["text"])})
            result.update(entries=entries, entry_count=total, offset=offset,
                          next_offset=offset+len(entries), has_more=offset+len(entries)<total,
                          stream=stream, category=category)
            return result

    def status(self, job_id):
        with self._lock:
            return self._view(self._get(job_id))

    async def wait_status(self, job_id, wait_seconds=0):
        if not 0 <= wait_seconds <= 20:
            raise ValueError("wait_seconds must be between 0 and 20")
        deadline = time.monotonic() + wait_seconds
        while True:
            result = self.status(job_id)
            if result["status"] not in ACTIVE or time.monotonic() >= deadline:
                return result
            await asyncio.sleep(min(0.2, max(0, deadline - time.monotonic())))

    def results(self, job_id, offset=0, limit=20, include_raw=False):
        if offset < 0 or not 1 <= limit <= 100:
            raise ValueError("offset must be nonnegative; limit must be between 1 and 100")
        with self._lock:
            result = self._view(self._get(job_id))
            rows = self._db.execute("SELECT document FROM observations WHERE job=? AND seq>=? ORDER BY seq LIMIT ?", (job_id, offset, limit)).fetchall()
            records = [json.loads(row[0]) for row in rows]
            if not include_raw:
                for record in records:
                    raw = json.loads(record.pop("raw"))
                    record["evidence"] = json.dumps({key:raw[key] for key in ("matcher-name", "extracted-results", "matched-at", "type") if key in raw})
            result["records"] = records
            next_offset = offset + len(records)
            result.update(offset=offset, next_offset=next_offset, has_more=next_offset < result["record_count"], raw_included=include_raw)
            return result

    def cancel(self, job_id):
        with self._lock:
            doc = self._get(job_id)
            active = self._active.get(job_id)
            if active and doc["status"] in ACTIVE:
                active[0].set()
                doc.update(status="cancelling", exit_state="cancel_requested", complete=False)
                self._save(doc)
                self._db.commit()
            return self._view(doc)

    def list(self, limit=10):
        if not 1 <= limit <= 100:
            raise ValueError("limit must be between 1 and 100")
        with self._lock:
            return {"jobs":[self._view(doc) for doc in self._all()[:limit]]}

    def close(self):
        with self._lock:
            active = list(self._active.values())
            for stop, _ in active:
                stop.set()
        for _, thread in active:
            thread.join(15)
        with self._lock:
            if self._active:
                raise RuntimeError("job worker did not stop; database remains open")
            self._db.close()
