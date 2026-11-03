"""Tiny exact-origin forward-proxy guard for isolated shadow tools.

The guard has one upstream: Uroboros' loopback recording proxy.  It cannot dial
the target directly. Plain HTTP requests are limited to an explicit method set
and the configured origin; the default remains read-only. HTTPS CONNECT is
limited to the configured authority. Active adapters do not use CONNECT because
the encrypted inner method would not be visible to this guard.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import select
import signal
import socket
import socketserver
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler
from urllib.parse import urlsplit, urlunsplit


READ_ONLY_METHODS = {"GET", "HEAD", "OPTIONS"}
SUPPORTED_HTTP_METHODS = READ_ONLY_METHODS | {"POST", "PUT", "PATCH", "DELETE"}
CAPTURE_REQUEST_HEADER = "X-Uroboros-Capture-Request"
TRAFFIC_ID_HEADER = "X-Uroboros-Traffic-ID"
SCOPE_GUARD_RESPONSE_HEADER = "X-Uroboros-Scope-Guard"
HOP_HEADERS = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "proxy-connection",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
}
MAX_HEADER_BYTES = 64 * 1024


class WireLimitError(OSError):
    pass


def _origin(value, label="origin"):
    if not isinstance(value, str) or not value or len(value) > 2048:
        raise ValueError(label + " must be a bounded URL")
    parsed = urlsplit(value)
    if parsed.scheme.lower() not in {"http", "https"} or not parsed.hostname:
        raise ValueError(label + " must be HTTP(S)")
    if parsed.username is not None or parsed.password is not None or parsed.query or parsed.fragment:
        raise ValueError(label + " contains unsupported URL components")
    try:
        port = parsed.port
    except ValueError:
        raise ValueError(label + " contains an invalid port") from None
    if parsed.path not in {"", "/"}:
        raise ValueError(label + " must not contain a path")
    host = parsed.hostname.lower()
    if ":" in host:
        host = "[" + host.strip("[]") + "]"
    default = 80 if parsed.scheme.lower() == "http" else 443
    netloc = host + ((":" + str(port)) if port is not None and port != default else "")
    return urlunsplit((parsed.scheme.lower(), netloc, "", "", ""))


def _listen(value):
    parsed = urlsplit("//" + value)
    if parsed.hostname not in {"127.0.0.1", "::1", "localhost"} or parsed.port is None:
        raise ValueError("listen must be an explicit loopback host:port")
    return parsed.hostname, parsed.port


def _upstream(value):
    selected = _origin(value, "upstream proxy")
    parsed = urlsplit(selected)
    if parsed.scheme != "http" or parsed.hostname not in {"127.0.0.1", "::1", "localhost"}:
        raise ValueError("upstream proxy must be loopback HTTP")
    return parsed.hostname, parsed.port or 80


def _authority(value, default_port=None):
    parsed = urlsplit("//" + value)
    if not parsed.hostname or parsed.username is not None or parsed.password is not None:
        raise ValueError("invalid authority")
    try:
        port = parsed.port or default_port
    except ValueError:
        raise ValueError("invalid authority port") from None
    if port is None or not 1 <= port <= 65535:
        raise ValueError("authority requires a valid port")
    return parsed.hostname.lower(), port


def _event_hash(value):
    return hashlib.sha256(value.encode("utf-8", errors="replace")).hexdigest()


class GuardState:
    def __init__(self, origin, upstream, max_transactions, max_body_bytes, socket_timeout, tunnel_seconds,
                 allowed_methods=None):
        self.origin = _origin(origin)
        parsed = urlsplit(self.origin)
        self.target_host = parsed.hostname.lower()
        self.target_port = parsed.port or (80 if parsed.scheme == "http" else 443)
        self.target_scheme = parsed.scheme
        self.upstream_host, self.upstream_port = _upstream(upstream)
        self.max_transactions = max_transactions
        self.max_body_bytes = max_body_bytes
        self.socket_timeout = socket_timeout
        self.tunnel_seconds = tunnel_seconds
        selected_methods = READ_ONLY_METHODS if allowed_methods is None else set(allowed_methods)
        if not selected_methods or not selected_methods <= SUPPORTED_HTTP_METHODS:
            raise ValueError("allowed methods contain an unsupported HTTP method")
        self.allowed_methods = frozenset(selected_methods)
        self.lock = threading.Lock()
        self.transactions = 0
        self.allowed = 0
        self.rejected = 0
        self.upstream_errors = 0
        self.bytes_up = 0
        self.bytes_down = 0
        self.stop_event = threading.Event()

    def emit(self, event, **fields):
        line = {"event": event, **fields}
        print(json.dumps(line, sort_keys=True, separators=(",", ":")), flush=True)

    def begin(self, method, kind):
        with self.lock:
            self.transactions += 1
            sequence = self.transactions
            within = sequence <= self.max_transactions
            if not within:
                self.rejected += 1
        if not within:
            self.emit("rejected", sequence=sequence, reason="transaction_budget", method=method, kind=kind)
            self.stop_event.set()
        return sequence, within

    def accept(self, sequence, method, kind):
        with self.lock:
            self.allowed += 1
        self.emit("allowed", sequence=sequence, method=method, kind=kind, origin=self.origin)

    def reject(self, sequence, method, kind, reason, attempted=""):
        with self.lock:
            self.rejected += 1
        fields = {"sequence": sequence, "method": method, "kind": kind, "reason": reason}
        if attempted:
            fields["attempted_sha256"] = _event_hash(attempted)
        self.emit("rejected", **fields)

    def upstream_error(self, sequence, kind, exc):
        with self.lock:
            self.upstream_errors += 1
        self.emit(
            "upstream_error",
            sequence=sequence,
            kind=kind,
            error_type=type(exc).__name__,
            error_sha256=_event_hash(str(exc)),
        )

    def add_bytes(self, up, down):
        with self.lock:
            self.bytes_up += up
            self.bytes_down += down

    def summary(self):
        with self.lock:
            return {
                "event": "summary",
                "transactions": self.transactions,
                "allowed": self.allowed,
                "rejected": self.rejected,
                "upstream_errors": self.upstream_errors,
                "bytes_up": self.bytes_up,
                "bytes_down": self.bytes_down,
            }


class ScopeGuardHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "UroborosKatanaScopeGuard/1"
    sys_version = ""

    def log_message(self, *_):
        return

    @property
    def state(self):
        return self.server.guard_state

    def _reply(self, status, reason, guard_reason=None):
        payload = (reason + "\n").encode("ascii", errors="replace")
        self.send_response(status)
        self.send_header("Content-Type", "text/plain; charset=us-ascii")
        self.send_header("Content-Length", str(len(payload)))
        if guard_reason is not None:
            self.send_header(SCOPE_GUARD_RESPONSE_HEADER, guard_reason)
        self.send_header("Connection", "close")
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(payload)
        self.close_connection = True

    def _single_header(self, name):
        values = self.headers.get_all(name, failobj=[])
        if len(values) > 1:
            raise ValueError("duplicate " + name)
        return values[0] if values else ""

    def _plain_target(self):
        if self.path.startswith("http://") or self.path.startswith("https://"):
            parsed = urlsplit(self.path)
        elif self.path.startswith("/") and not self.path.startswith("//"):
            host = self._single_header("Host")
            if not host:
                raise ValueError("missing Host")
            parsed = urlsplit("http://" + host + self.path)
        else:
            raise ValueError("proxy request target must be absolute")
        if parsed.scheme.lower() != "http" or not parsed.hostname or parsed.username is not None or parsed.password is not None:
            raise ValueError("plain proxy request must use HTTP without credentials")
        try:
            port = parsed.port or 80
        except ValueError:
            raise ValueError("invalid target port") from None
        origin = _origin(urlunsplit(("http", parsed.netloc, "", "", "")))
        return parsed, origin, port

    def _body(self):
        if self._single_header("Transfer-Encoding"):
            raise ValueError("transfer encoding is not accepted")
        value = self._single_header("Content-Length")
        if not value:
            return b""
        try:
            size = int(value, 10)
        except ValueError:
            raise ValueError("invalid content length") from None
        if size < 0 or size > self.state.max_body_bytes:
            raise ValueError("request body exceeds the limit")
        body = self.rfile.read(size)
        if len(body) != size:
            raise ValueError("incomplete request body")
        return body

    def _normalized_headers(self, host):
        result = []
        seen = set()
        total = 0
        internal = {
            "host",
            CAPTURE_REQUEST_HEADER.lower(),
            TRAFFIC_ID_HEADER.lower(),
            SCOPE_GUARD_RESPONSE_HEADER.lower(),
        }
        for key, value in self.headers.items():
            lowered = key.lower()
            if lowered in HOP_HEADERS or lowered in internal:
                continue
            if lowered in seen:
                raise ValueError("duplicate request header")
            seen.add(lowered)
            if "\r" in value or "\n" in value:
                raise ValueError("invalid request header")
            total += len(key.encode("latin-1")) + len(value.encode("latin-1")) + 4
            if total > MAX_HEADER_BYTES:
                raise ValueError("request headers exceed the limit")
            result.append((key, value))
        result.append(("Host", host))
        # Katana's standard transport applies -H, while Chromium navigation in
        # headless mode does not consistently inherit it.  The local guard is
        # the trusted coordination boundary, so it injects exactly one marker;
        # Uroboros' recording proxy strips it before contacting the target.
        result.append((CAPTURE_REQUEST_HEADER, "1"))
        result.append(("Connection", "close"))
        return result

    def _forward_plain(self):
        sequence, within = self.state.begin(self.command, "http")
        if not within:
            self._reply(429, "shadow transaction budget exhausted", "transaction_budget")
            return
        if self.command not in self.state.allowed_methods:
            reason = (
                "method_not_read_only"
                if self.state.allowed_methods == READ_ONLY_METHODS
                else "method_not_allowed"
            )
            self.state.reject(sequence, self.command, "http", reason)
            self._reply(405, "method is outside the configured shadow scope", reason)
            return
        try:
            parsed, origin, _ = self._plain_target()
            if origin != self.state.origin:
                self.state.reject(sequence, self.command, "http", "outside_origin", origin)
                self._reply(403, "outside shadow origin", "outside_origin")
                return
            body = self._body()
            host = parsed.netloc
            headers = self._normalized_headers(host)
        except ValueError as exc:
            self.state.reject(sequence, self.command, "http", "invalid_request", str(exc))
            self._reply(400, "invalid shadow proxy request", "invalid_request")
            return
        upstream = None
        down = 0
        try:
            upstream = socket.create_connection(
                (self.state.upstream_host, self.state.upstream_port),
                timeout=self.state.socket_timeout,
            )
            upstream.settimeout(self.state.socket_timeout)
            request_target = urlunsplit(("http", parsed.netloc, parsed.path or "/", parsed.query, ""))
            head = [self.command + " " + request_target + " HTTP/1.1"]
            head.extend(key + ": " + value for key, value in headers)
            wire = ("\r\n".join(head) + "\r\n\r\n").encode("latin-1") + body
            upstream.sendall(wire)
            self.state.accept(sequence, self.command, "http")
            while True:
                chunk = upstream.recv(64 * 1024)
                if not chunk:
                    break
                if down + len(chunk) > self.state.max_body_bytes + MAX_HEADER_BYTES:
                    raise WireLimitError("recording proxy response exceeded the wire limit")
                self.connection.sendall(chunk)
                down += len(chunk)
            self.state.add_bytes(len(wire), down)
        except (OSError, TimeoutError) as exc:
            self.state.upstream_error(sequence, "http", exc)
            if down == 0:
                self._reply(502, "recording proxy unavailable")
        finally:
            self.close_connection = True
            if upstream is not None:
                try:
                    upstream.close()
                except OSError:
                    pass

    def do_CONNECT(self):
        sequence, within = self.state.begin("CONNECT", "connect")
        if not within:
            self._reply(429, "shadow transaction budget exhausted", "transaction_budget")
            return
        try:
            host, port = _authority(self.path, 443)
        except ValueError as exc:
            self.state.reject(sequence, "CONNECT", "connect", "invalid_authority", str(exc))
            self._reply(400, "invalid CONNECT authority", "invalid_authority")
            return
        if self.state.target_scheme != "https" or (host, port) != (self.state.target_host, self.state.target_port):
            self.state.reject(sequence, "CONNECT", "connect", "outside_authority", self.path)
            self._reply(403, "outside shadow authority", "outside_authority")
            return
        upstream = None
        established = False
        try:
            upstream = socket.create_connection(
                (self.state.upstream_host, self.state.upstream_port),
                timeout=self.state.socket_timeout,
            )
            upstream.settimeout(self.state.socket_timeout)
            headers = self._normalized_headers(self.path)
            head = ["CONNECT " + self.path + " HTTP/1.1"]
            head.extend(key + ": " + value for key, value in headers)
            wire = ("\r\n".join(head) + "\r\n\r\n").encode("latin-1")
            upstream.sendall(wire)
            response = bytearray()
            while b"\r\n\r\n" not in response:
                chunk = upstream.recv(4096)
                if not chunk:
                    raise OSError("upstream proxy closed before CONNECT response")
                response.extend(chunk)
                if len(response) > MAX_HEADER_BYTES:
                    raise OSError("upstream CONNECT response headers are too large")
            status_line = bytes(response).split(b"\r\n", 1)[0].split()
            if len(status_line) < 2 or status_line[1] != b"200":
                self.connection.sendall(response)
                self.close_connection = True
                return
            self.connection.sendall(response)
            established = True
            self.state.accept(sequence, "CONNECT", "connect")
            up, down = self._tunnel(upstream)
            self.state.add_bytes(len(wire) + up, len(response) + down)
        except (OSError, TimeoutError) as exc:
            self.state.upstream_error(sequence, "connect", exc)
            if not established:
                self._reply(502, "recording proxy CONNECT failed")
        finally:
            self.close_connection = True
            if upstream is not None:
                try:
                    upstream.close()
                except OSError:
                    pass

    def _tunnel(self, upstream):
        client = self.connection
        client.setblocking(False)
        upstream.setblocking(False)
        deadline = time.monotonic() + self.state.tunnel_seconds
        uploaded = downloaded = 0
        while time.monotonic() < deadline:
            readable, _, exceptional = select.select([client, upstream], [], [client, upstream], 0.5)
            if exceptional:
                break
            if not readable:
                continue
            for source in readable:
                try:
                    data = source.recv(64 * 1024)
                except BlockingIOError:
                    continue
                if not data:
                    return uploaded, downloaded
                destination = upstream if source is client else client
                if source is client and uploaded + len(data) > self.state.max_body_bytes + MAX_HEADER_BYTES:
                    raise WireLimitError("CONNECT upload exceeded the wire limit")
                if source is upstream and downloaded + len(data) > self.state.max_body_bytes + MAX_HEADER_BYTES:
                    raise WireLimitError("CONNECT download exceeded the wire limit")
                destination.sendall(data)
                if source is client:
                    uploaded += len(data)
                else:
                    downloaded += len(data)
        return uploaded, downloaded

    do_GET = _forward_plain
    do_HEAD = _forward_plain
    do_OPTIONS = _forward_plain
    do_POST = _forward_plain
    do_PUT = _forward_plain
    do_PATCH = _forward_plain
    do_DELETE = _forward_plain
    do_TRACE = _forward_plain

    def __getattr__(self, name):
        # BaseHTTPRequestHandler otherwise synthesizes an unmarked 501 response
        # for extension methods (for example Schemathesis' QUERY coverage probe).
        # Route every syntactically parsed method through the same budget and
        # read-only decision so local guard responses remain distinguishable
        # from target evidence.
        if name.startswith("do_"):
            return self._forward_plain
        raise AttributeError(name)


class GuardServer(socketserver.ThreadingMixIn, socketserver.TCPServer):
    allow_reuse_address = False
    daemon_threads = True

    def __init__(self, address, state):
        self.guard_state = state
        super().__init__(address, ScopeGuardHandler)


def serve(args):
    state = GuardState(
        args.origin,
        args.upstream,
        args.max_transactions,
        args.max_body_bytes,
        args.socket_timeout,
        args.tunnel_seconds,
        args.allow_method,
    )
    address = _listen(args.listen)
    def stop(*_):
        state.stop_event.set()

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    with GuardServer(address, state) as server:
        server.timeout = 0.25
        state.emit(
            "ready",
            listen=args.listen,
            origin=state.origin,
            upstream="http://%s:%d" % (state.upstream_host, state.upstream_port),
            max_transactions=state.max_transactions,
            allowed_methods=sorted(state.allowed_methods),
        )
        while not state.stop_event.is_set():
            server.handle_request()
    print(json.dumps(state.summary(), sort_keys=True, separators=(",", ":")), flush=True)


def parser():
    result = argparse.ArgumentParser(description="Exact-origin Katana shadow proxy guard")
    result.add_argument("--listen", default="127.0.0.1:18789")
    result.add_argument("--upstream", default="http://127.0.0.1:8788")
    result.add_argument("--origin", required=True)
    result.add_argument("--max-transactions", type=int, required=True)
    result.add_argument("--max-body-bytes", type=int, default=1024 * 1024)
    result.add_argument("--socket-timeout", type=int, default=15)
    result.add_argument("--tunnel-seconds", type=int, default=65)
    result.add_argument(
        "--allow-method",
        action="append",
        type=str.upper,
        choices=sorted(SUPPORTED_HTTP_METHODS),
        help="Allowed plain HTTP method; repeat as needed. Defaults to read-only.",
    )
    return result


def main(argv=None):
    args = parser().parse_args(argv)
    if not 1 <= args.max_transactions <= 1000:
        raise SystemExit("max-transactions must be between 1 and 1000")
    if not 0 <= args.max_body_bytes <= 8 * 1024 * 1024:
        raise SystemExit("max-body-bytes is outside the supported range")
    if not 1 <= args.socket_timeout <= 60 or not 1 <= args.tunnel_seconds <= 120:
        raise SystemExit("timeout is outside the supported range")
    try:
        serve(args)
    except (OSError, ValueError) as exc:
        print(json.dumps({"event": "fatal", "error": str(exc)}, sort_keys=True), file=sys.stderr, flush=True)
        raise SystemExit(2) from None


if __name__ == "__main__":
    main()
