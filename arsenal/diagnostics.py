"""Bounded native error-log capture and conservative error classification.

The engine's aggregate error counter and error-log entries are different
measurements. A timeout or connection reset alone never establishes a ban.
"""

import json
import threading

from arsenal.contracts import OUTPUT_CAP_BYTES


def error_record(line):
    try:
        data = json.loads(line)
        if not isinstance(data, dict) or not isinstance(data.get("error"), str):
            raise ValueError("expected a JSON object with error text")
    except ValueError:
        return {"category":"unparsed", "protocol":"", "text":line, "parsed":False}
    message = data["error"].lower()
    category = "other"
    for name, markers in (
        ("dns", ("could not resolve", "no such host", "no address found")),
        ("tls", ("tls:", "tls handshake", "certificate", "x509:")),
        ("connection_refused", ("connection refused",)),
        ("connection_reset", ("connection reset",)),
        ("closed_or_filtered", ("port closed or filtered",)),
        ("timeout", ("timeout", "deadline exceeded")),
        ("eof", ("eof",)),
        ("network_unreachable", ("network is unreachable", "no route to host")),
        ("template", ("unresolved variable", "missing variable", "template logic")),
    ):
        if any(marker in message for marker in markers):
            category = name
            break
    protocol = data.get("type", "")
    if protocol not in ("", "http", "tcp", "ssl", "dns", "javascript", "headless", "file", "websocket", "whois", "code"):
        protocol = "unknown"
    return {"category":category, "protocol":protocol,
            "text":line, "parsed":True, "detail":data}


class ErrorLogCapture:
    """Tail a native JSONL file while the process runs; stop on capture failure.

    Native Nuclei requires a filename for -elog. The transient file is monitored,
    read in 64 KiB chunks, and archived incrementally in the job database. At the
    byte cap the invocation is cancelled to prevent disk growth; terminal scan
    status must remain incomplete. A final unterminated line is still retained.
    """

    def __init__(self, path, stop_scan, emit, cap=OUTPUT_CAP_BYTES):
        self.path = path
        self.stop_scan = stop_scan
        self.emit = emit
        self.cap = cap
        self.truncated = False
        self.failure = ""
        self.bytes_read = 0
        self._done = threading.Event()
        self._thread = threading.Thread(target=self._read, daemon=True)

    def start(self):
        self._thread.start()

    def finish(self):
        self._done.set()
        self._thread.join(5)
        if self._thread.is_alive():
            self.failure = "error-log reader did not stop"
            self.stop_scan.set()
            raise RuntimeError(self.failure)

    def _read(self):
        pending = bytearray()
        try:
            # The file exists before launch; Nuclei opens/truncates it itself.
            with self.path.open("rb") as source:
                while True:
                    if self.path.stat().st_size > self.cap:
                        self.truncated = True
                        self.stop_scan.set()
                    remaining = self.cap - self.bytes_read
                    chunk = source.read(min(65536, remaining)) if remaining else b""
                    if chunk:
                        self.bytes_read += len(chunk)
                        pending.extend(chunk)
                        while b"\n" in pending:
                            line, _, tail = pending.partition(b"\n")
                            pending = bytearray(tail)
                            if line.strip():
                                self.emit(line.decode(errors="replace"))
                        continue
                    if self._done.is_set():
                        break
                    self._done.wait(0.02)
                if pending.strip():
                    self.emit(pending.decode(errors="replace"))
        except Exception as exc:
            self.failure = str(exc)
            self.stop_scan.set()
