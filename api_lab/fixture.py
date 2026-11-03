"""Disposable two-user API; vulnerable and fixed deployments share a schema."""
import argparse
import json
import threading
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from urllib.parse import urlsplit


def openapi():
    note = {"type":"object","required":["id","owner","title","secret"],"properties":{
        "id":{"type":"integer"},"owner":{"type":"string"},"title":{"type":"string"},"secret":{"type":"string"}}}
    body = {"type":"object","required":["title","secret"],"properties":{
        "title":{"type":"string","minLength":1,"maxLength":40},"secret":{"type":"string","minLength":1,"maxLength":80}}}
    parameter = {"name":"id","in":"path","required":True,"schema":{"type":"integer","minimum":1}}
    def operation(name,code=200,schema=None,request=None,parameter_needed=False):
        result = {"operationId":name,"responses":{str(code):{"description":"ok","content":{"application/json":{"schema":schema or {"type":"object"}}}},"default":{"description":"error"}},
                  "security":[{"bearer":[]}]}
        if request:
            result["requestBody"] = {"required":True,"content":{"application/json":{"schema":request}}}
        if parameter_needed:
            result["parameters"] = [parameter]
        return result
    return {"openapi":"3.0.3","info":{"title":"API Lab authorization fixture","version":"1"},
            "components":{"securitySchemes":{"bearer":{"type":"http","scheme":"bearer"}}},
            "paths":{
                "/health":{"get":dict(operation("health"),security=[])},
                "/notes":{"post":operation("createNote",201,note,body)},
                "/notes/{id}":{"get":operation("getNote",schema=note,parameter_needed=True),
                                "patch":operation("updateNote",schema=note,request={"type":"object","required":["secret"],"properties":{"secret":{"type":"string","minLength":1}}},parameter_needed=True),
                                "delete":operation("deleteNote",parameter_needed=True)}}}


def example_plan(write=False):
    ref = lambda name:{"$from":name+"#/id"}
    check = {"name":"private-note","kind":"private_write" if write else "private_read",
             "claim":"A private note may only be read/changed by its owner","policy_source":"fixture_contract",
             "owner_step":"owner_read","peer_control_step":"peer_read","peer_identity":"bob",
             "object_pointer":"/id","principal_pointer":"/owner","private_fields":["/secret"]}
    if write:
        check.update(write={"operation_id":"updateNote","path_parameters":{"id":ref("owner_create")},"body":{"secret":"peer-mutation"}},
                     restore_body={"secret":{"$from":"owner_read#/secret"}})
    return {"steps":[
        {"name":"owner_create","operation_id":"createNote","identity":"alice","generate":True,"body":{"title":"owner-test","secret":{"$nonce":True}},"expected_status":201},
        {"name":"peer_create","operation_id":"createNote","identity":"bob","body":{"title":"peer-test","secret":"peer-control-value"},"expected_status":201},
        {"name":"unrelated_health","operation_id":"health","identity":"anonymous","expected_status":200},
        {"name":"owner_read","operation_id":"getNote","identity":"alice","path_parameters":{"id":ref("owner_create")},"expected_status":200},
        {"name":"peer_read","operation_id":"getNote","identity":"bob","path_parameters":{"id":ref("peer_create")},"expected_status":200}],
        "checks":[check],
        "cleanup":[{"name":"delete_owner","operation_id":"deleteNote","identity":"alice","path_parameters":{"id":ref("owner_create")},"expected_status":200},
                   {"name":"delete_peer","operation_id":"deleteNote","identity":"bob","path_parameters":{"id":ref("peer_create")},"expected_status":200}]}


def make_server(mode="vulnerable",host="127.0.0.1",port=0):
    class Handler(BaseHTTPRequestHandler):
        def reply(self,code,body):
            data = json.dumps(body).encode()
            self.send_response(code)
            self.send_header("Content-Type","application/json")
            self.send_header("Content-Length",str(len(data)))
            self.send_header("X-API-Revision",mode+"-v1")
            self.end_headers()
            try:
                self.wfile.write(data)
            except (BrokenPipeError,ConnectionResetError):
                pass
        def handle_request(self):
            path = urlsplit(self.path).path
            if self.command == "GET" and path=="/openapi.json":
                return self.reply(200,openapi())
            if self.command == "GET" and path=="/health":
                if getattr(self.server,"delay",0):
                    import time
                    time.sleep(self.server.delay)
                return self.reply(200,{"status":"ok"})
            identity = {"Bearer fixture-alice":"alice","Bearer fixture-bob":"bob"}.get(self.headers.get("Authorization"))
            if not identity:
                return self.reply(401,{"error":"unauthorized"})
            body = json.loads(self.rfile.read(int(self.headers.get("Content-Length","0"))) or b"{}")
            with self.server.state_lock:
                if self.command == "POST" and path=="/notes":
                    if not isinstance(body,dict) or not body.get("title") or not body.get("secret"):
                        return self.reply(422,{"error":"invalid body"})
                    note_id = self.server.counter
                    self.server.counter += 1
                    note = {"id":note_id,"owner":identity,"title":body["title"],"secret":body["secret"]}
                    self.server.notes[note_id] = note
                    return self.reply(201,note)
                if path.startswith("/notes/"):
                    try:
                        note_id = int(path.rsplit("/",1)[1])
                    except ValueError:
                        return self.reply(404,{"error":"not found"})
                    note = self.server.notes.get(note_id)
                    if note is None:
                        return self.reply(404,{"error":"not found"})
                    if note["owner"]!=identity and (mode=="fixed" or self.command=="DELETE"):
                        return self.reply(403,{"error":"forbidden"})
                    if self.command=="GET":
                        return self.reply(200,note)
                    if self.command=="PATCH":
                        note["secret"] = body["secret"]
                        return self.reply(200,note)
                    if self.command=="DELETE":
                        self.server.notes.pop(note_id)
                        return self.reply(200,{"deleted":note_id})
                return self.reply(404,{"error":"not found"})
        do_GET = do_POST = do_PATCH = do_DELETE = handle_request
        def log_message(self,*args):
            pass
    server = ThreadingHTTPServer((host,port),Handler)
    server.notes,server.counter,server.state_lock = {},1,threading.Lock()
    return server


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode",choices=("vulnerable","fixed"),default="vulnerable")
    parser.add_argument("--port",type=int,default=9000)
    args = parser.parse_args()
    make_server(args.mode,"0.0.0.0",args.port).serve_forever()
