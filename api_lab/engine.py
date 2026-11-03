"""OpenAPI-aware, identity-separated experiments with explicit policy oracles.

No model code is evaluated. Plans are JSON operations and response references.
Schema failures, policy violations and missing preconditions are separate results.
"""

import copy
import hashlib
import importlib.metadata
import json
import sys
import time
import uuid
from pathlib import Path
from urllib.parse import urlsplit

import requests
import schemathesis
import yaml
from hypothesis import Phase, given, seed, settings


VERSION = "0.2"
SENSITIVE = {"authorization", "cookie", "set-cookie", "password", "access_token", "refresh_token", "api_key", "client_secret"}


def digest(value):
    return hashlib.sha256(json.dumps(value,sort_keys=True,separators=(",",":"),ensure_ascii=True).encode()).hexdigest()


def origin(url):
    parsed = urlsplit(url)
    if parsed.scheme not in ("http","https") or not parsed.hostname or parsed.username or parsed.password:
        raise ValueError("base URL must be HTTP(S), without embedded credentials")
    return parsed.scheme.lower()+"://"+parsed.netloc.lower()


def pointer(value, path):
    if not path:
        return value
    if not path.startswith("/"):
        raise ValueError("JSON pointer must start with /")
    for part in path[1:].split("/"):
        part = part.replace("~1","/").replace("~0","~")
        value = value[int(part)] if isinstance(value,list) else value[part]
    return value


def resolve(value, responses, nonce):
    if isinstance(value,dict):
        if set(value) == {"$from"}:
            name, sep, path = value["$from"].partition("#")
            if not sep or name not in responses:
                raise ValueError("unavailable response reference: " + value["$from"])
            return copy.deepcopy(pointer(responses[name]["body"],path))
        if value == {"$nonce":True}:
            return nonce
        return {key:resolve(item,responses,nonce) for key,item in value.items()}
    if isinstance(value,list):
        return [resolve(item,responses,nonce) for item in value]
    return value


class Identities:
    def __init__(self, path=None, data=None):
        self.data = data if data is not None else json.loads(Path(path).read_text(encoding="utf-8"))
        self.entries = self.data.get("identities",{})
        self.secrets = set()
        for entry in self.entries.values():
            for value in entry.get("headers",{}).values():
                self.secrets.add(str(value))
                if str(value).lower().startswith("bearer "):
                    self.secrets.add(str(value)[7:])

    def headers(self, name, base_url):
        if name == "anonymous":
            return {}
        if name not in self.entries:
            raise ValueError("identity not configured: " + name)
        entry = self.entries[name]
        if origin(base_url) not in entry.get("origins",[]):
            raise ValueError("identity is not configured for this origin: " + name)
        return dict(entry.get("headers",{}))

    def redact(self, value):
        if isinstance(value,dict):
            return {key:("[REDACTED]" if key.lower() in SENSITIVE else self.redact(item)) for key,item in value.items()}
        if isinstance(value,list):
            return [self.redact(item) for item in value]
        if isinstance(value,str):
            for secret in sorted(self.secrets,key=len,reverse=True):
                if secret:
                    value = value.replace(secret,"[REDACTED]")
        return value

    def describe(self):
        return [{"name":name,"origins":entry.get("origins",[])} for name,entry in self.entries.items()]


def validate_plan(plan):
    if not isinstance(plan,dict) or not isinstance(plan.get("steps"),list) or not plan["steps"]:
        raise ValueError("plan requires a nonempty steps array")
    names = set()
    def references(value):
        if isinstance(value,dict):
            if set(value)=={"$from"}:
                return [value["$from"].split("#",1)[0]]
            return [name for item in value.values() for name in references(item)]
        if isinstance(value,list):
            return [name for item in value for name in references(item)]
        return []
    for step in plan["steps"] + plan.get("cleanup",[]):
        if not isinstance(step,dict) or not step.get("name") or not step.get("operation_id") or not step.get("identity"):
            raise ValueError("each step needs name, operation_id and identity")
        if step["name"] in names:
            raise ValueError("step names must be unique")
        if any(name not in names for name in references(step)):
            raise ValueError("step references must point to earlier responses")
        names.add(step["name"])
    if not plan.get("checks"):
        raise ValueError("plan requires explicit checks; schema validity alone is not a security oracle")
    check_names = set()
    has_private_checks = False
    for check in plan["checks"]:
        if check.get("kind") not in ("private_read","private_write","json_equal","status"):
            raise ValueError("supported checks: private_read/private_write/json_equal/status")
        if not check.get("name") or not check.get("claim") or check["name"] in check_names:
            raise ValueError("each check needs a unique name and explicit claim")
        check_names.add(check["name"])
        if check["kind"].startswith("private_"):
            has_private_checks = True
            if "step" in check or "control_step" in check:
                raise ValueError("private checks generate peer and anonymous probes automatically; do not supply step/control_step")
        required = ("owner_step","peer_control_step","peer_identity","object_pointer","principal_pointer","private_fields") if check["kind"].startswith("private_") else ("step","control_step")
        if any(key not in check for key in required):
            raise ValueError("missing check preconditions: " + ", ".join(required))
        if any(check[key] not in {step["name"] for step in plan["steps"]} for key in required if key.endswith("step")):
            raise ValueError("checks must refer to declared setup steps")
        if any(name not in {step["name"] for step in plan["steps"]} for name in references(check)):
            raise ValueError("check references must point to declared responses")
        if check["kind"].startswith("private_") and (not isinstance(check["private_fields"],list) or not check["private_fields"]):
            raise ValueError("private_fields must be a nonempty array of JSON pointers")
        if check["kind"] in ("json_equal","status") and "expected" not in check:
            raise ValueError("state checks require expected")
        if check["kind"] == "json_equal" and "pointer" not in check:
            raise ValueError("json_equal requires pointer")
        if check["kind"] == "private_write" and not all(key in check for key in ("write","restore_body")):
            raise ValueError("private_write requires write request and restore_body")
    if has_private_checks and any(step.get("expected_status") in (401,403,404) for step in plan["steps"]):
        raise ValueError("private-check steps are successful setup/positive controls only; denial probes are generated automatically")


class Experiment:
    def __init__(self, base_url, schema_url, identities, emit, options=None, schema_document=None, source_context=None,
                 wfc_intake=None, wfc_execution=None):
        self.base_url = base_url.rstrip("/")
        origin(self.base_url)
        self.identities, self.emit = identities, emit
        self.options = options or {}
        self.max_requests = self.options.get("max_requests",100)
        self.rps = self.options.get("rps",5)
        self.request_timeout = self.options.get("request_timeout",5)
        self.requests = 0
        self.last_request = 0
        self.sessions = {}
        self.operation_specs = {}
        self.seed = self.options.get("seed",1)
        self.schema_url = schema_url
        self.source_context = copy.deepcopy(source_context)
        self.wfc_intake = copy.deepcopy(wfc_intake)
        self.wfc_execution = copy.deepcopy(wfc_execution)
        if source_context is not None:
            from api_lab.source_scan import validate_context
            validate_context(source_context)
        self.inline_schema = schema_document is not None
        target_revision = "unknown"
        if self.inline_schema:
            self.document = copy.deepcopy(schema_document)
        else:
            session = requests.Session()
            session.trust_env = False
            if origin(schema_url) != origin(self.base_url):
                raise ValueError("schema URL must use the selected target origin")
            try:
                response = session.get(schema_url,headers=identities.headers(self.options.get("schema_identity","anonymous"),base_url),
                                       timeout=self.request_timeout,allow_redirects=False,stream=True)
                raw = self._read(response)
                if response.status_code != 200:
                    raise ValueError("OpenAPI fetch did not return 200")
                self.document = yaml.safe_load(raw)
                target_revision = response.headers.get("X-API-Revision","unknown")
            finally:
                session.close()
        if not isinstance(self.document,dict) or not self.document.get("openapi", "").startswith("3."):
            raise ValueError("v1 requires an OpenAPI 3 document")
        # Override declared server URLs; credentials belong to the chosen origin.
        self.document["servers"] = [{"url":self.base_url}]
        self.schema = schemathesis.openapi.from_dict(self.document)
        self.operations = {}
        for path, methods in self.document.get("paths",{}).items():
            for method, definition in methods.items():
                if method.lower() in ("get","post","put","patch","delete","head","options") and isinstance(definition,dict):
                    name = definition.get("operationId", method.upper()+" "+path)
                    if name in self.operations:
                        raise ValueError("duplicate operationId")
                    self.operations[name] = self.schema[path][method.upper()]
                    self.operation_specs[name] = definition
        contract = copy.deepcopy(self.document)
        contract.pop("servers",None)
        self.metadata = {"api_lab_version":VERSION, "schemathesis_version":importlib.metadata.version("schemathesis"),
                         "contract_hash":digest(contract),
                         "schema_hash":digest(self.document), "schema_url":schema_url,
                         "source_hash":digest({p.name:p.read_text() for p in Path(__file__).parent.glob("*.py")}),
                         "target_revision":target_revision, "seed":self.seed}
        if self.inline_schema:
            self.metadata["schema_source"] = "captured_traffic"
            self.metadata["traffic_schema_hash"] = digest(schema_document)
            self.metadata["schema_inference"] = "observed_shape_not_declared_contract"
        if self.source_context is not None:
            self.metadata["source_context_hash"] = digest(self.source_context)
            self.metadata["source_revision"] = self.source_context["scan"]["source_revision"]
        if self.wfc_intake is not None:
            self.metadata["wfc_intake_hash"] = digest(self.wfc_intake)
            self.metadata["wfc_proposal_id"] = self.wfc_intake["proposal_id"]
            self.metadata["reviewed_target_revision"] = self.wfc_execution["review"]["target"]["revision"]
            self.metadata["execution_review_sha256"] = self.wfc_execution["review_sha256"]
        self.emit({"event":"metadata","data":self.metadata})

    def _read(self, response):
        body = bytearray()
        try:
            for chunk in response.iter_content(65536):
                body.extend(chunk)
                if len(body) > 1024*1024:
                    raise ValueError("response exceeds 1 MiB; experiment incomplete")
            response._content = bytes(body)
            response._content_consumed = True
            return response.content
        finally:
            response.close()

    def _case(self, step, responses, nonce):
        if step["operation_id"] not in self.operations:
            raise ValueError("unknown operation_id: " + step["operation_id"])
        operation = self.operations[step["operation_id"]]
        values = {key:resolve(step[key],responses,nonce) for key in ("path_parameters","query","body") if key in step}
        if "body" in values:
            values["media_type"] = "application/json"
        if step.get("generate"):
            cases = []
            @seed(self.seed + self.requests)
            @settings(max_examples=1,database=None,deadline=None,phases=[Phase.generate])
            @given(operation.as_strategy(**values))
            def draw(case):
                cases.append(case)
            draw()
            return cases[0]
        return operation.Case(**values)

    def request(self, step, responses, nonce, phase):
        if self.requests >= self.max_requests:
            raise ValueError("request budget exhausted; experiment incomplete")
        identity = step["identity"]
        credentials = self.identities.headers(identity,self.base_url)
        case = self._case(step,responses,nonce)
        if case.media_type not in (None,"application/json"):
            raise ValueError("v1 supports JSON request bodies")
        url = self.base_url + case.formatted_path
        if origin(url) != origin(self.base_url):
            raise ValueError("operation must use selected target origin")
        headers = dict(case.headers or {})
        headers = {key:value for key,value in headers.items() if key.lower() not in ("authorization","cookie","host")}
        headers.update(credentials)
        if identity not in self.sessions:
            self.sessions[identity] = requests.Session()
            self.sessions[identity].trust_env = False
        wait = max(0,1/self.rps-(time.monotonic()-self.last_request))
        if wait:
            time.sleep(wait)
        self.last_request = time.monotonic()
        self.requests += 1
        arguments = {"headers":headers,"params":case.query,"timeout":self.request_timeout,"allow_redirects":False,"stream":True}
        # The SDK sentinel means there is no body, distinct from JSON null.
        if "body" in step or step.get("generate") and case.media_type:
            arguments["json"] = case.body
        response = self.sessions[identity].request(case.method,url,**arguments)
        raw = self._read(response)
        try:
            body = json.loads(raw)
        except ValueError:
            body = {"_text":raw.decode(errors="replace")}
        issues = []
        try:
            self.operations[step["operation_id"]].validate_response(response,case=case)
        except Exception as exc:
            issues.append(str(exc)[:2000])
        result = {"request":{"identity":identity,"operation_id":step["operation_id"],"method":case.method,
                             "url":response.request.url,"headers":dict(response.request.headers),
                             "body":arguments.get("json"),"query":case.query},
                  "status":response.status_code,"body":body,"response_headers":dict(response.headers),
                  "response_hash":hashlib.sha256(raw).hexdigest(),"schema_issues":issues}
        self.emit({"event":"request","phase":phase,"name":step["name"],"data":self.identities.redact(result)})
        return result

    def _check(self, check, steps, responses, nonce, phase):
        verdict = {"name":check["name"],"kind":check["kind"],"claim":check["claim"],
                   "policy_source":check.get("policy_source","model_assumption"),"verdict":"inconclusive"}
        try:
            if not check["kind"].startswith("private_"):
                actual, control = responses[check["step"]], responses[check["control_step"]]
                if not 200 <= control["status"] < 300:
                    raise ValueError("legitimate control did not succeed")
                expected = resolve(check["expected"],responses,nonce)
                observed = actual["status"] if check["kind"] == "status" else pointer(actual["body"],check["pointer"])
                verdict.update(verdict="passed" if observed == expected else "violation",expected=expected,observed=observed)
                return verdict
            owner, peer_control = responses[check["owner_step"]], responses[check["peer_control_step"]]
            if not all(200 <= item["status"] < 300 for item in (owner,peer_control)):
                raise ValueError("owner and peer legitimate controls must both succeed")
            if not check["private_fields"]:
                raise ValueError("explicit private fields are required")
            principal = check["principal_pointer"]
            if pointer(owner["body"],principal) == pointer(peer_control["body"],principal):
                raise ValueError("controls do not establish different principals")
            read_step = copy.deepcopy(steps[check["owner_step"]])
            if self.operations[read_step["operation_id"]].method.upper() != "GET":
                raise ValueError("owner_step must be a GET")
            if steps[check["peer_control_step"]]["identity"] != check["peer_identity"] or read_step["identity"] == check["peer_identity"]:
                raise ValueError("peer identity does not match its control, or equals owner")
            if not any(pointer(owner["body"],field) not in (None,"") and pointer(owner["body"],field) != pointer(peer_control["body"],field) for field in check["private_fields"]):
                raise ValueError("private test values do not distinguish the two control objects")
            anonymous = dict(read_step,identity="anonymous",name=check["name"]+":anonymous")
            public = self.request(anonymous,responses,nonce,phase)
            if 200 <= public["status"] < 300 and all(pointer(public["body"],field) == pointer(owner["body"],field) for field in check["private_fields"]):
                raise ValueError("anonymous control returns the same data; public or separate unauthenticated exposure")
            if check["kind"] == "private_read":
                probe = self.request(dict(read_step,identity=check["peer_identity"],name=check["name"]+":peer"),responses,nonce,phase)
                if probe["status"] in (401,403,404):
                    verdict.update(verdict="passed",reason="peer denied; both legitimate controls succeeded")
                elif 200 <= probe["status"] < 300:
                    same_object = pointer(probe["body"],check["object_pointer"]) == pointer(owner["body"],check["object_pointer"])
                    exposed = []
                    for field in check["private_fields"]:
                        try:
                            value = pointer(owner["body"],field)
                            if value is not None and value != "" and pointer(probe["body"],field) == value and pointer(peer_control["body"],field) != value:
                                exposed.append(field)
                        except (KeyError,IndexError):
                            pass
                    verdict.update(verdict="violation" if same_object and exposed else "passed",
                                   exposed_fields=exposed,reason="compared object identity and explicit private field values")
                else:
                    raise ValueError("unexpected peer status; cannot decide authorization policy")
                verdict.update(owner_status=owner["status"],peer_status=probe["status"],anonymous_status=public["status"])
            else:
                write = dict(check["write"],identity=check["peer_identity"],name=check["name"]+":write")
                desired = resolve(write["body"],responses,nonce)
                try:
                    probe = self.request(write,responses,nonce,phase)
                    after = self.request(dict(read_step,name=check["name"]+":readback"),responses,nonce,phase)
                    owner_write = self.request(dict(write,identity=read_step["identity"],name=check["name"]+":owner-write"),responses,nonce,phase)
                    legitimate = self.request(dict(read_step,name=check["name"]+":owner-readback"),responses,nonce,phase)
                    if not 200 <= owner_write["status"] < 300 or not 200 <= legitimate["status"] < 300 or not 200 <= after["status"] < 300:
                        raise ValueError("write/readback positive controls failed")
                    changed = []
                    for field in check["private_fields"]:
                        expected = pointer(desired,field)
                        if pointer(legitimate["body"],field) != expected or pointer(owner["body"],field) == expected:
                            raise ValueError("mutation must differ from baseline and work for the owner")
                        if pointer(after["body"],field) == expected:
                            changed.append(field)
                    verdict.update(verdict="violation" if changed else "passed",changed_fields=changed,peer_status=probe["status"])
                finally:
                    restored = self.request(dict(write,identity=read_step["identity"],body=check["restore_body"],name=check["name"]+":restore"),responses,nonce,phase)
                    if not 200 <= restored["status"] < 300:
                        raise ValueError("restore failed; target state may remain modified")
                    readback = self.request(dict(read_step,name=check["name"]+":restore-readback"),responses,nonce,phase)
                    if not 200 <= readback["status"] < 300 or any(pointer(readback["body"],field)!=pointer(owner["body"],field) for field in check["private_fields"]):
                        raise ValueError("restore readback differs from original state")
        except (ValueError,KeyError,IndexError,TypeError) as exc:
            verdict.update(verdict="inconclusive",reason=str(exc))
        return verdict

    def sequence(self, plan, phase):
        responses, steps = {}, {step["name"]:step for step in plan["steps"]}
        nonce = uuid.uuid4().hex
        results = []
        try:
            for step in plan["steps"]:
                responses[step["name"]] = self.request(step,responses,nonce,phase)
                if step.get("expected_status") is not None and responses[step["name"]]["status"] != step["expected_status"]:
                    raise ValueError("setup status differs from expected: " + step["name"])
            for check in plan["checks"]:
                verdict = self._check(check,steps,responses,nonce,phase)
                results.append(verdict)
                self.emit({"event":"check","phase":phase,"data":self.identities.redact(verdict)})
            return results
        finally:
            prior_error = sys.exc_info()[0] is not None
            cleanup_errors = []
            for step in plan.get("cleanup",[]):
                try:
                    cleaned = self.request(step,responses,nonce,phase+":cleanup")
                    responses[step["name"]] = cleaned
                    if step.get("expected_status") is not None and cleaned["status"] != step["expected_status"]:
                        raise ValueError("cleanup status differs from expected: " + step["name"])
                except Exception as exc:
                    cleanup_errors.append(str(exc))
                    self.emit({"event":"diagnostic","phase":phase+":cleanup","reason":str(exc),"step":step["name"]})
            if cleanup_errors and not prior_error:
                raise ValueError("cleanup failed: " + "; ".join(cleanup_errors))

    def run(self, plan, examples=1, minimize=True):
        validate_plan(plan)
        for step in plan["steps"]+plan.get("cleanup",[]):
            if step["operation_id"] not in self.operations:
                raise ValueError("unknown operation_id: " + step["operation_id"])
        self.emit({"event":"artifact","name":"schema","data":self.identities.redact(self.document)})
        self.emit({"event":"artifact","name":"plan","data":self.identities.redact(plan)})
        if self.source_context is not None:
            self.emit({"event":"artifact","name":"source-context","data":self.source_context})
        if self.wfc_intake is not None:
            self.emit({"event":"artifact","name":"wfc-intake","data":self.wfc_intake})
            self.emit({"event":"artifact","name":"wfc-execution-review","data":self.wfc_execution})
        try:
            for example in range(examples):
                verdicts = self.sequence(plan,"example-%d" % example)
                for verdict in verdicts:
                    if verdict["verdict"] == "violation":
                        check = next(item for item in plan["checks"] if item["name"] == verdict["name"])
                        recipe = copy.deepcopy(plan)
                        recipe["checks"] = [check]
                        if minimize:
                            recipe["steps"] = dependency_slice(plan,check)
                            try:
                                verified = self.sequence(recipe,"reproducer-"+check["name"])
                            except (ValueError,KeyError,requests.RequestException) as exc:
                                self.emit({"event":"diagnostic","phase":"minimize","reason":str(exc)})
                                verified = []
                            if not verified or verified[0]["verdict"] != "violation":
                                recipe = copy.deepcopy(plan)
                                recipe["checks"] = [check]
                                verdict["minimized_replay_verified"] = False
                            else:
                                verdict["minimized_replay_verified"] = True
                        reproducer = {"plan":recipe,"base_url":self.base_url,"schema_url":self.schema_url,
                                      "schema_identity":self.options.get("schema_identity","anonymous"),
                                      "metadata":self.metadata,"oracle":verdict}
                        if self.inline_schema:
                            reproducer["schema_document"] = self.document
                        reproducer = self.identities.redact(reproducer)
                        if self.source_context is not None:
                            # Frozen source evidence is original program data;
                            # credential redaction must not invalidate its hashes.
                            reproducer["source_context"] = self.source_context
                        if self.wfc_intake is not None:
                            reproducer["wfc_intake"] = self.wfc_intake
                            reproducer["wfc_execution"] = self.wfc_execution
                        self.emit({"event":"artifact","name":"reproducer-"+check["name"],
                                   "data":reproducer})
            self.emit({"event":"complete","requests":self.requests,"examples":examples})
        finally:
            for session in self.sessions.values():
                session.close()


def dependency_slice(plan, check):
    """Remove unrelated declared steps, then runtime-verify the resulting recipe.

    This is a verified dependency slice, not a claim of globally minimal PoC.
    Hidden state dependencies can require fallback to the full original plan.
    """
    needed = {check[key] for key in ("owner_step","peer_control_step","step","control_step") if key in check}
    def refs(value):
        if isinstance(value,dict):
            if "$from" in value:
                needed.add(value["$from"].split("#",1)[0])
            for item in value.values():
                refs(item)
        elif isinstance(value,list):
            for item in value:
                refs(item)
    refs(check)
    refs(plan.get("cleanup",[]))
    previous = None
    while previous != needed:
        previous = set(needed)
        for step in plan["steps"]:
            if step["name"] in needed:
                refs(step)
    return [copy.deepcopy(step) for step in plan["steps"] if step["name"] in needed]
