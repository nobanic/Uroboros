"""Disposable HTTP target and recording-proxy double for Katana smoke tests."""

from __future__ import annotations

import argparse
import base64
import hashlib
import http.client
import json
import os
import re
import signal
import socketserver
import threading
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler
from urllib.parse import urlsplit, urlunsplit


CAPTURE_HEADER = "X-Uroboros-Capture-Request"
TRAFFIC_HEADER = "X-Uroboros-Traffic-ID"
CAPTURE_CONTRACT = "uroboros-recorded-exchange-v1"
REFERENCE = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}\Z")
HOP_HEADERS = {
    "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
    "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade",
}


def _emit(event, **fields):
    print(json.dumps({"event": event, **fields}, sort_keys=True, separators=(",", ":")), flush=True)


def _head_hash(start_line, headers):
    encoded = json.dumps(
        {"start_line": start_line, "headers": [[key, value] for key, value in headers]},
        sort_keys=True,
        separators=(",", ":"),
        ensure_ascii=True,
    ).encode("ascii")
    return hashlib.sha256(encoded).hexdigest()


def _origin(value):
    parsed = urlsplit(value)
    if parsed.scheme != "http" or not parsed.hostname or parsed.username is not None or parsed.password is not None:
        raise ValueError("fixture origin must be credential-free HTTP")
    if parsed.path not in {"", "/"} or parsed.query or parsed.fragment:
        raise ValueError("fixture origin must contain only an origin")
    port = parsed.port or 80
    return "http://%s%s" % (parsed.hostname.lower(), (":" + str(port)) if port != 80 else "")


class TargetState:
    def __init__(self):
        self.lock = threading.Lock()
        self.requests = 0
        self.mutations = 0

    def record(self, method, path, mutation=False):
        with self.lock:
            self.requests += 1
            if mutation:
                self.mutations += 1
        _emit("target_request", method=method, path=path, mutation=mutation)


class TargetHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "KatanaFixture/1"
    sys_version = ""

    def log_message(self, *_):
        return

    def _send(self, status, content_type, body):
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.send_header("Connection", "close")
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)
        self.close_connection = True

    def do_GET(self):
        parsed = urlsplit(self.path)
        path = parsed.path
        self.server.fixture_state.record("GET", path)
        if path == "/":
            body = b"""<!doctype html>
<html><head><title>Katana shadow fixture</title><script src="/app.js"></script></head>
<body>
  <a href="/linked?view=fixture-link-secret">linked</a>
  <form method="post" action="/mutation"><input name="should_not_submit"></form>
  <img src="http://outside.invalid/shadow-leak.png" alt="scope guard probe">
  <script>
    addEventListener('DOMContentLoaded', function () {
      const runtimePath = atob('L2FwaS9ydW50aW1l');
      fetch(runtimePath + '?nonce=' + Date.now());
      fetch('/mutation', {method: 'POST', body: 'must=stay-blocked'});
    });
  </script>
</body></html>"""
            self._send(200, "text/html; charset=utf-8", body)
            return
        if path == "/app.js":
            body = b"window.fixtureStaticCandidate = '/api/static-candidate?source=script-secret';\n"
            self._send(200, "application/javascript", body)
            return
        if path in {"/linked", "/api/static-candidate"}:
            self._send(200, "text/html; charset=utf-8", b"<html><body>fixture leaf</body></html>")
            return
        if path == "/api/runtime":
            self._send(200, "application/json", b'{"runtime":true,"fixture_secret":"raw-only"}')
            return
        self._send(404, "text/plain", b"not found")

    do_HEAD = do_GET

    def do_POST(self):
        parsed = urlsplit(self.path)
        mutation = parsed.path == "/mutation"
        self.server.fixture_state.record("POST", parsed.path, mutation=mutation)
        size = int(self.headers.get("Content-Length", "0"))
        if size:
            self.rfile.read(min(size, 1024 * 1024))
        self._send(201 if mutation else 404, "application/json", b'{"mutation":true}')


class RecorderState:
    def __init__(self, target_origin, run_id, capture_file):
        self.target_origin = _origin(target_origin)
        if not isinstance(run_id, str) or not REFERENCE.fullmatch(run_id):
            raise ValueError("fixture recorder run_id is invalid")
        self.run_id = run_id
        self.lock = threading.Lock()
        self.sequence = 0
        descriptor = os.open(capture_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        self.capture = os.fdopen(descriptor, "wb")

    def commit(self, record):
        with self.lock:
            self.sequence += 1
            traffic_id = "fixture-flow-%04d" % self.sequence
            value = {
                "contract": CAPTURE_CONTRACT,
                "run_id": self.run_id,
                "traffic_id": traffic_id,
                **record,
            }
            encoded = json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("ascii") + b"\n"
            self.capture.write(encoded)
            self.capture.flush()
            os.fsync(self.capture.fileno())
            return traffic_id

    def close(self):
        self.capture.close()


class RecorderHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "KatanaRecorderDouble/1"
    sys_version = ""

    def log_message(self, *_):
        return

    def _send_text(self, status, value):
        body = (value + "\n").encode("ascii")
        self.send_response(status)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)
        self.close_connection = True

    def _forward(self):
        if not self.headers.get(CAPTURE_HEADER):
            self._send_text(428, "capture marker required")
            return
        parsed = urlsplit(self.path)
        if parsed.scheme != "http" or not parsed.hostname:
            self._send_text(400, "absolute HTTP URL required")
            return
        origin = _origin(urlunsplit((parsed.scheme, parsed.netloc, "", "", "")))
        if origin != self.server.recorder_state.target_origin:
            self._send_text(403, "outside fixture origin")
            return
        length = int(self.headers.get("Content-Length", "0"))
        if length < 0 or length > 1024 * 1024:
            self._send_text(413, "body too large")
            return
        body = self.rfile.read(length) if length else None
        headers = {}
        for key, value in self.headers.items():
            lowered = key.lower()
            if lowered in HOP_HEADERS or lowered in {"host", CAPTURE_HEADER.lower(), "content-length"}:
                continue
            headers[key] = value
        if body is not None:
            headers["Content-Length"] = str(len(body))
        connection = http.client.HTTPConnection(parsed.hostname, parsed.port or 80, timeout=10)
        try:
            connection.request(
                self.command,
                urlunsplit(("", "", parsed.path or "/", parsed.query, "")),
                body=body,
                headers=headers,
            )
            response = connection.getresponse()
            payload = response.read(4 * 1024 * 1024 + 1)
            if len(payload) > 4 * 1024 * 1024:
                self._send_text(502, "fixture response too large")
                return
            response_headers = response.getheaders()
            request_url = urlunsplit((parsed.scheme, parsed.netloc, parsed.path or "/", parsed.query, ""))
            traffic_id = self.server.recorder_state.commit({
                "timestamp": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
                "method": self.command,
                "url": request_url,
                "status": response.status,
                "request_head_sha256": _head_hash(self.requestline, list(self.headers.items())),
                "response_head_sha256": _head_hash(
                    "HTTP/1.1 %d %s" % (response.status, response.reason),
                    response_headers,
                ),
                "request_body_b64": base64.b64encode(body or b"").decode("ascii"),
                "response_body_b64": base64.b64encode(payload).decode("ascii"),
                "response_content_type": response.getheader("Content-Type", ""),
            })
            self.send_response(response.status, response.reason)
            for key, value in response_headers:
                if key.lower() not in HOP_HEADERS and key.lower() not in {"content-length", TRAFFIC_HEADER.lower()}:
                    self.send_header(key, value)
            self.send_header("Content-Length", str(len(payload)))
            self.send_header(TRAFFIC_HEADER, traffic_id)
            self.send_header("Connection", "close")
            self.end_headers()
            if self.command != "HEAD":
                self.wfile.write(payload)
            self.close_connection = True
            _emit("committed", traffic_id=traffic_id, method=self.command, path=parsed.path or "/")
        finally:
            connection.close()

    do_GET = _forward
    do_HEAD = _forward
    do_OPTIONS = _forward
    do_POST = _forward


class ThreadingServer(socketserver.ThreadingMixIn, socketserver.TCPServer):
    allow_reuse_address = False
    daemon_threads = True


def _address(value):
    parsed = urlsplit("//" + value)
    if not parsed.hostname or parsed.port is None:
        raise ValueError("listen must be host:port")
    return parsed.hostname, parsed.port


def _serve(server, label, listen):
    stopping = threading.Event()

    def stop(*_):
        stopping.set()

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    server.timeout = 0.25
    _emit("ready", service=label, listen=listen)
    with server:
        while not stopping.is_set():
            server.handle_request()


def main(argv=None):
    parser = argparse.ArgumentParser(description="Katana shadow disposable fixture")
    sub = parser.add_subparsers(dest="service", required=True)
    target = sub.add_parser("target")
    target.add_argument("--listen", default="0.0.0.0:8080")
    recorder = sub.add_parser("recorder")
    recorder.add_argument("--listen", default="127.0.0.1:8788")
    recorder.add_argument("--target-origin", required=True)
    recorder.add_argument("--run-id", default="katana-fixture-unbound")
    recorder.add_argument("--capture-file", default="/tmp/uroboros-recorded-exchanges.jsonl")
    args = parser.parse_args(argv)
    if args.service == "target":
        server = ThreadingServer(_address(args.listen), TargetHandler)
        server.fixture_state = TargetState()
        _serve(server, "target", args.listen)
    else:
        server = ThreadingServer(_address(args.listen), RecorderHandler)
        server.recorder_state = RecorderState(args.target_origin, args.run_id, args.capture_file)
        try:
            _serve(server, "recorder", args.listen)
        finally:
            server.recorder_state.close()


if __name__ == "__main__":
    main()
