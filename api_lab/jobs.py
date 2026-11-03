"""Durable experiment jobs and hashed artifact pages, independent of MCP sessions."""
import asyncio
import json
import sqlite3
import sys
import threading
import time
import uuid
from pathlib import Path

from arsenal.execmgr import Runner
from api_lab.engine import digest, origin, validate_plan
from api_lab.wfc_api_lab_intake import validate_execution_context, validate_intake_context


ACTIVE = ("running","cancelling")


class Jobs:
    def __init__(self,path,history=100):
        self.directory = Path(path).resolve().parent
        self.directory.mkdir(parents=True,exist_ok=True)
        self.db = sqlite3.connect(path,check_same_thread=False)
        self.db.execute("PRAGMA journal_mode=WAL")
        self.db.execute("CREATE TABLE IF NOT EXISTS jobs (id TEXT PRIMARY KEY, document TEXT)")
        self.db.execute("CREATE TABLE IF NOT EXISTS events (job TEXT, seq INTEGER, document TEXT, PRIMARY KEY(job,seq))")
        self.db.execute("CREATE TABLE IF NOT EXISTS artifacts (job TEXT, name TEXT, hash TEXT, document TEXT, PRIMARY KEY(job,name))")
        self.db.execute("CREATE TABLE IF NOT EXISTS traffic_schemas (hash TEXT PRIMARY KEY, document TEXT NOT NULL)")
        self.lock = threading.RLock()
        self.active = {}
        self.history = history
        with self.lock:
            for doc in self._all():
                if doc["status"] in ACTIVE:
                    doc.update(status="interrupted",exit_state="interrupted",complete=False,finished_at=time.time())
                    doc["reason"] = "Service restarted; experiment was not resumed. Target may retain setup objects."
                    self._save(doc)
                    self._input_path(doc["job_id"]).unlink(missing_ok=True)
            self.db.commit()

    def _input_path(self,job_id):
        if len(job_id)!=32 or any(char not in "0123456789abcdef" for char in job_id):
            raise ValueError("invalid job ID")
        return self.directory/(job_id+".input.json")

    def _all(self):
        return sorted((json.loads(row[0]) for row in self.db.execute("SELECT document FROM jobs")),key=lambda doc:doc["started_at"],reverse=True)

    def _get(self,job_id):
        row = self.db.execute("SELECT document FROM jobs WHERE id=?",(job_id,)).fetchone()
        if not row:
            raise ValueError("unknown job_id")
        return json.loads(row[0])

    def _save(self,doc):
        self.db.execute("INSERT OR REPLACE INTO jobs VALUES (?,?)",(doc["job_id"],json.dumps(doc)))

    def _view(self,doc):
        result = dict(doc)
        result["elapsed_seconds"] = round((doc.get("finished_at") or time.time())-doc["started_at"],2)
        counts = {"passed":0,"violation":0,"inconclusive":0}
        requests = 0
        for row in self.db.execute("SELECT document FROM events WHERE job=?",(doc["job_id"],)):
            event = json.loads(row[0])
            requests += event.get("event") == "request"
            if event.get("event") == "check" and event.get("phase","").startswith("example-"):
                counts[event["data"]["verdict"]] += 1
        result.update(request_count=requests,checks=counts,artifacts=[{"name":name,"hash":hash} for name,hash in self.db.execute("SELECT name,hash FROM artifacts WHERE job=? ORDER BY name",(doc["job_id"],))])
        if doc["status"] in ACTIVE:
            result.update(poll_after_seconds=5,next_action="Poll api_lab_status(wait_seconds=5); read results/artifacts. Do not repeat start while running.")
        return result

    def start(self,base_url,schema_url,plan,options=None,examples=1,minimize=True,task_id="",intent_id="",replay_of="",source_recipe_hash="",schema_document=None,source_context=None,wfc_intake=None,wfc_execution=None):
        origin(base_url)
        if schema_document is None:
            origin(schema_url)
        elif schema_url:
            raise ValueError("choose schema_url or an imported traffic schema, not both")
        validate_plan(plan)
        if source_context is not None:
            from api_lab.source_scan import validate_context
            validate_context(source_context)
        if (wfc_intake is None) != (wfc_execution is None):
            raise ValueError("WFC intake and execution contexts must be supplied together")
        if wfc_intake is not None:
            validate_intake_context(wfc_intake)
            validate_execution_context(wfc_execution)
            if schema_document is None or digest(schema_document) != wfc_intake["schema_id"]:
                raise ValueError("WFC intake schema differs from the retained schema")
            if digest(plan) != wfc_intake["plan_sha256"]:
                raise ValueError("WFC intake plan differs from the reviewed plan")
            review = wfc_execution["review"]
            if review["proposal_id"] != wfc_intake["proposal_id"] or review["plan_sha256"] != wfc_intake["plan_sha256"]:
                raise ValueError("WFC execution review differs from the intake context")
            if review["target"]["origin"] != origin(base_url) or base_url.rstrip("/") != origin(base_url):
                raise ValueError("WFC execution target differs from the reviewed canonical origin")
        options = dict(options or {})
        budget = options.get("budget_seconds",300)
        if not 10 <= budget <= 7200 or not 1 <= examples <= 100:
            raise ValueError("budget 10..7200 seconds; examples 1..100")
        if options.get("rps",5) <= 0 or not 1 <= options.get("max_requests",100) <= 10000 or not 0 < options.get("request_timeout",5) <= 60:
            raise ValueError("positive rps, max_requests 1..10000 and request_timeout up to 60 seconds")
        with self.lock:
            if self.active:
                return {"exit_state":"busy","complete":False,"job_id":next(iter(self.active)),"reason":"API Lab already has an active experiment"}
            for old in self._all()[self.history-1:]:
                for table in ("events","artifacts"):
                    self.db.execute("DELETE FROM "+table+" WHERE job=?",(old["job_id"],))
                self.db.execute("DELETE FROM jobs WHERE id=?",(old["job_id"],))
                self._input_path(old["job_id"]).unlink(missing_ok=True)
            job_id = uuid.uuid4().hex
            spec = {"base_url":base_url,"schema_url":schema_url,"plan":plan,"options":options,"examples":examples,"minimize":minimize}
            if schema_document is not None:
                spec["schema_document"] = schema_document
            if source_context is not None:
                spec["source_context"] = source_context
            if wfc_intake is not None:
                spec["wfc_intake"] = wfc_intake
                spec["wfc_execution"] = wfc_execution
            # Recipes use identity references; configured credentials are read
            # only by the child process from a separate operator-owned file.
            path = self._input_path(job_id)
            path.write_text(json.dumps(spec),encoding="utf-8")
            path.chmod(0o600)
            doc = {"job_id":job_id,"status":"running","exit_state":"running","complete":False,
                   "started_at":time.time(),"base_url":base_url,"schema_url":schema_url,"plan_hash":digest(plan),
                   "task_id":task_id,"intent_id":intent_id,"replay_of":replay_of,"budget_seconds":budget,
                   "source_recipe_hash":source_recipe_hash}
            if source_context is not None:
                doc["source_context_hash"] = digest(source_context)
            if wfc_intake is not None:
                doc.update(wfc_intake_hash=digest(wfc_intake),proposal_id=wfc_intake["proposal_id"],
                           execution_review_sha256=wfc_execution["review_sha256"],
                           execution_action=wfc_execution["action"])
            self._save(doc)
            self.db.commit()
            stop = threading.Event()
            thread = threading.Thread(target=self._work,args=(job_id,budget,stop),daemon=True)
            self.active[job_id] = (stop,thread)
            try:
                thread.start()
            except Exception as exc:
                self.active.pop(job_id,None)
                path.unlink(missing_ok=True)
                doc.update(status="finished",exit_state="tool_error",complete=False,finished_at=time.time(),reason=str(exc))
                self._save(doc)
                self.db.commit()
            return self._view(doc)

    def _line(self,job_id,stream,line):
        if not line.strip():
            return
        event = json.loads(line) if stream=="stdout" else {"event":"diagnostic","text":line}
        with self.lock:
            sequence = self.db.execute("SELECT COALESCE(MAX(seq)+1,0) FROM events WHERE job=?",(job_id,)).fetchone()[0]
            self.db.execute("INSERT INTO events VALUES (?,?,?)",(job_id,sequence,json.dumps(event)))
            if event.get("event") == "artifact":
                content = event["data"]
                self.db.execute("INSERT OR REPLACE INTO artifacts VALUES (?,?,?,?)",(job_id,event["name"],digest(content),json.dumps(content,sort_keys=True)))
            if event.get("event") == "metadata":
                doc = self._get(job_id)
                doc["metadata"] = event["data"]
                self._save(doc)
            if event.get("event") == "error":
                doc = self._get(job_id)
                doc["reason"] = event["reason"]
                self._save(doc)
            self.db.commit()

    def _work(self,job_id,budget,stop):
        try:
            result = Runner().run([sys.executable,"-m","api_lab.worker",str(self._input_path(job_id))],budget,cancel_event=stop,on_line=lambda source,line:self._line(job_id,source,line))
            with self.lock:
                doc = self._get(job_id)
                ended = any(json.loads(row[0]).get("event")=="complete" for row in self.db.execute("SELECT document FROM events WHERE job=?",(job_id,)))
                state = result["exit_state"]
                if state == "ok" and not ended:
                    state = "tool_error"
                doc.update(status="finished",exit_state=state,complete=state=="ok" and ended,
                           finished_at=time.time(),truncated=result["truncated"])
                self._save(doc)
                self.db.commit()
        except Exception as exc:
            with self.lock:
                doc = self._get(job_id)
                doc.update(status="finished",exit_state="tool_error",complete=False,finished_at=time.time(),reason=str(exc))
                self._save(doc)
                self.db.commit()
        finally:
            try:
                self._input_path(job_id).unlink(missing_ok=True)
            finally:
                with self.lock:
                    self.active.pop(job_id,None)

    def status(self,job_id):
        with self.lock:
            return self._view(self._get(job_id))

    async def wait_status(self,job_id,wait_seconds=5):
        if not 0 <= wait_seconds <= 20:
            raise ValueError("wait_seconds must be 0..20")
        deadline = time.monotonic()+wait_seconds
        while True:
            result = self.status(job_id)
            if result["status"] not in ACTIVE or time.monotonic() >= deadline:
                return result
            await asyncio.sleep(min(0.2,max(0,deadline-time.monotonic())))

    def results(self,job_id,offset=0,limit=20):
        if offset<0 or not 1 <= limit <= 100:
            raise ValueError("offset nonnegative, limit 1..100")
        with self.lock:
            result = self._view(self._get(job_id))
            rows = self.db.execute("SELECT seq,document FROM events WHERE job=? AND seq>=? ORDER BY seq LIMIT ?",(job_id,offset,limit)).fetchall()
            entries = []
            for sequence,document in rows:
                event = json.loads(document)
                if event.get("event") == "artifact":
                    event = {"event":"artifact","name":event["name"],"hash":digest(event["data"])}
                elif event.get("event") == "request":
                    data = event["data"]
                    event = {"event":"request","phase":event["phase"],"name":event["name"],"status":data["status"],
                             "identity":data["request"]["identity"],"operation_id":data["request"]["operation_id"],
                             "response_hash":data["response_hash"],"schema_issues":data["schema_issues"],"artifact":"event:"+str(sequence)}
                entries.append({"sequence":sequence,**event})
            count = self.db.execute("SELECT COUNT(*) FROM events WHERE job=?",(job_id,)).fetchone()[0]
            result.update(entries=entries,next_offset=offset+len(entries),has_more=offset+len(entries)<count)
            return result

    def artifact(self,job_id,name,offset=0,limit=8000):
        if offset<0 or not 1 <= limit <= 16000:
            raise ValueError("artifact offset nonnegative; limit 1..16000 characters")
        with self.lock:
            self._get(job_id)
            if name.startswith("event:"):
                row = self.db.execute("SELECT document FROM events WHERE job=? AND seq=?",(job_id,int(name[6:]))).fetchone()
            else:
                row = self.db.execute("SELECT document FROM artifacts WHERE job=? AND name=?",(job_id,name)).fetchone()
            if not row:
                raise ValueError("artifact not found")
            document = json.loads(row[0])
            text = json.dumps(document,sort_keys=True,ensure_ascii=True)
            return {"job_id":job_id,"name":name,"hash":digest(document),"offset":offset,"text":text[offset:offset+limit],
                    "next_offset":min(len(text),offset+limit),"has_more":offset+limit<len(text),"length":len(text)}

    def replay(self,job_id,artifact_name,base_url="",options=None):
        with self.lock:
            original = self._get(job_id)
            row = self.db.execute("SELECT document FROM artifacts WHERE job=? AND name=?",(job_id,artifact_name)).fetchone()
            if not row:
                raise ValueError("reproducer artifact not found")
            recipe = json.loads(row[0])
            if not isinstance(recipe,dict) or "plan" not in recipe:
                raise ValueError("artifact is not a reproducer")
            return self.replay_recipe(recipe,base_url,options,task_id=original["task_id"],intent_id=original["intent_id"],replay_of=job_id)

    def replay_recipe(self,recipe,base_url="",options=None,task_id="",intent_id="",replay_of=""):
        if not isinstance(recipe,dict) or not isinstance(recipe.get("plan"),dict):
            raise ValueError("recipe must contain a plan")
        if recipe.get("wfc_intake") is not None or recipe.get("wfc_execution") is not None:
            raise ValueError("WFC intake recipes require a fresh operator review via /operator/wfc-intake")
        target = base_url or recipe["base_url"]
        document = recipe.get("schema_document")
        schema_url = ""
        if document is None:
            schema_path = recipe["schema_url"][len(origin(recipe["schema_url"])):]
            schema_url = origin(target)+schema_path
        options = dict(options or {})
        options.setdefault("seed",recipe.get("metadata",{}).get("seed",1))
        options.setdefault("schema_identity",recipe.get("schema_identity","anonymous"))
        return self.start(target,schema_url,recipe["plan"],options,examples=1,minimize=False,
                          task_id=task_id,intent_id=intent_id,replay_of=replay_of,source_recipe_hash=digest(recipe),schema_document=document,
                          source_context=recipe.get("source_context"))

    def save_schema(self, document):
        identifier = digest(document)
        with self.lock:
            self.db.execute("INSERT OR IGNORE INTO traffic_schemas VALUES (?,?)", (identifier, json.dumps(document,sort_keys=True,separators=(",",":"),ensure_ascii=True)))
            self.db.commit()
        return identifier

    def schema_document(self, identifier):
        with self.lock:
            row = self.db.execute("SELECT document FROM traffic_schemas WHERE hash=?", (identifier,)).fetchone()
        if row is None:
            raise ValueError("unknown traffic schema_id")
        document = json.loads(row[0])
        if digest(document) != identifier:
            raise ValueError("traffic schema integrity check failed")
        return document

    def schema_page(self, identifier, offset=0, limit=8000):
        if offset < 0 or not 1 <= limit <= 16000:
            raise ValueError("offset nonnegative; limit 1..16000")
        text = json.dumps(self.schema_document(identifier),sort_keys=True,separators=(",",":"),ensure_ascii=True)
        return {"schema_id":identifier,"hash":identifier,"text":text[offset:offset+limit],"offset":offset,
                "next_offset":min(len(text),offset+limit),"has_more":offset+limit<len(text),"length":len(text)}

    def export(self,job_id,offset=0,limit=16000):
        """Freeze a terminal job into canonical ASCII JSON for durable host storage."""
        if offset<0 or not 1<=limit<=16000:
            raise ValueError("offset nonnegative; limit 1..16000")
        with self.lock:
            job = self._get(job_id)
            if job["status"] in ACTIVE:
                raise ValueError("export requires a terminal job; poll status first")
            artifacts = [{"name":name,"hash":hash,"data":json.loads(document)} for name,hash,document in
                         self.db.execute("SELECT name,hash,document FROM artifacts WHERE job=? ORDER BY name",(job_id,))]
            events = [{"sequence":seq,**json.loads(document)} for seq,document in
                      self.db.execute("SELECT seq,document FROM events WHERE job=? ORDER BY seq",(job_id,))]
            bundle = {"format":"uroboros-api-lab-evidence-v1","job":job,"artifacts":artifacts,"events":events}
            text = json.dumps(bundle,sort_keys=True,separators=(",",":"),ensure_ascii=True)
            return {"job_id":job_id,"hash":digest(bundle),"offset":offset,"text":text[offset:offset+limit],
                    "next_offset":min(len(text),offset+limit),"has_more":offset+limit<len(text),"length":len(text)}

    def cancel(self,job_id):
        with self.lock:
            doc = self._get(job_id)
            if job_id in self.active and doc["status"] in ACTIVE:
                self.active[job_id][0].set()
                doc.update(status="cancelling",exit_state="cancel_requested",complete=False)
                self._save(doc)
                self.db.commit()
            return self._view(doc)

    def list(self,limit=10):
        if not 1<=limit<=100:
            raise ValueError("limit 1..100")
        with self.lock:
            return {"jobs":[self._view(doc) for doc in self._all()[:limit]]}

    def close(self):
        with self.lock:
            active = list(self.active.values())
            for stop,_ in active:
                stop.set()
        for _,thread in active:
            thread.join(15)
        with self.lock:
            if self.active:
                raise RuntimeError("worker did not stop")
            self.db.close()
