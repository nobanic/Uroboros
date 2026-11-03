"""Execution manager (P0). Runs a scanner binary with:
- single active scan per Runner (second call -> BUSY), lock always released (C2),
- per-call timeout -> kill the whole process group/tree (POSIX killpg / Windows taskkill /T),
- streaming stdout/stderr caps enforced DURING execution (C2), not after,
- cancellation -> CANCELLED,
- exit-state taxonomy (contracts). OK vs OK_EMPTY is decided by the caller from parsed records.

Named execmgr (not exec) to avoid shadowing the builtin. Stdlib-only.
"""

import os
import subprocess
import threading
import time

from arsenal.contracts import OK, BUSY, TIMEOUT, TOOL_ERROR, CANCELLED, OUTPUT_CAP_BYTES

IS_WIN = os.name == "nt"


class Runner:
    def __init__(self):
        self._lock = threading.Lock()
        self._proc = None
        self._cancel = threading.Event()

    def run(self, argv, timeout, cap=OUTPUT_CAP_BYTES, cancel_event=None, on_line=None, env=None, cwd=None):
        if not self._lock.acquire(blocking=False):
            return {"exit_state": BUSY, "stdout": "", "stderr": "", "truncated": False}
        self._cancel.clear()
        try:
            return self._run_locked(argv, timeout, cap, cancel_event, on_line, env, cwd)
        finally:
            self._proc = None
            self._lock.release()  # always released (C2)

    def cancel(self):
        p = self._proc
        if p is not None:
            self._cancel.set()
            self._kill_tree(p)

    def _popen(self, argv, env=None, cwd=None):
        kw = {"stdout": subprocess.PIPE, "stderr": subprocess.PIPE, "env": env, "cwd": cwd}
        if IS_WIN:
            kw["creationflags"] = subprocess.CREATE_NEW_PROCESS_GROUP
        else:
            kw["start_new_session"] = True  # own process group -> killpg reaps children
        return subprocess.Popen(argv, **kw)

    def _kill_tree(self, p):
        try:
            if IS_WIN:
                subprocess.run(["taskkill", "/F", "/T", "/PID", str(p.pid)],
                               capture_output=True)
            else:
                import signal
                os.killpg(os.getpgid(p.pid), signal.SIGKILL)
        except Exception:
            try:
                p.kill()
            except Exception:
                pass

    def _run_locked(self, argv, timeout, cap, cancel_event, on_line, env, cwd):
        def cancelled():
            return self._cancel.is_set() or (cancel_event is not None and cancel_event.is_set())

        if cancelled():
            return {"exit_state": CANCELLED, "stdout": "", "stderr": "", "truncated": False}
        try:
            p = self._popen(argv, env=env, cwd=cwd)
        except OSError as exc:
            return {"exit_state": TOOL_ERROR, "stdout": "", "stderr": str(exc), "truncated": False}
        self._proc = p
        out, err, truncated = bytearray(), bytearray(), {"v": False}
        callback_errors = []

        def pump(stream, buf, name):
            pending = bytearray()
            try:
                while True:
                    # read() waits for 64 KiB and hides sparse live JSONL output.
                    chunk = stream.read1(65536)
                    if not chunk:
                        break
                    if len(buf) >= cap:
                        truncated["v"] = True
                        self._kill_tree(p)  # stop the flood during execution
                        break
                    accepted = chunk[: cap - len(buf)]
                    buf.extend(accepted)
                    if on_line:
                        pending.extend(accepted)
                        while b"\n" in pending:
                            line, _, tail = pending.partition(b"\n")
                            pending = bytearray(tail)
                            # Pipes preserve CRLF on Windows. Publish logical
                            # lines consistently without retaining the CR.
                            on_line(name, line.removesuffix(b"\r").decode(errors="replace"))
                    if len(buf) >= cap:
                        truncated["v"] = True
                        self._kill_tree(p)
                        break
                if on_line and pending:
                    on_line(name, pending.decode(errors="replace"))
            except Exception as exc:
                callback_errors.append(str(exc))
                self._kill_tree(p)

        to = threading.Thread(target=pump, args=(p.stdout, out, "stdout"))
        te = threading.Thread(target=pump, args=(p.stderr, err, "stderr"))
        to.start()
        te.start()

        timed_out = False
        deadline = time.monotonic() + timeout
        while p.poll() is None:
            if cancelled():
                self._kill_tree(p)
                break
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                timed_out = True
                self._kill_tree(p)
                break
            try:
                p.wait(timeout=min(0.1, remaining))
            except subprocess.TimeoutExpired:
                pass
        try:
            p.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self._kill_tree(p)
        to.join(timeout=5)
        te.join(timeout=5)
        for s in (p.stdout, p.stderr):  # avoid fd leak across many scans
            try:
                s.close()
            except Exception:
                pass

        stdout = bytes(out).decode(errors="replace")
        stderr = bytes(err).decode(errors="replace")
        if callback_errors:
            stderr += "\nstream persistence error: " + "; ".join(callback_errors)
        if cancelled():
            state = CANCELLED
        elif timed_out:
            state = TIMEOUT
        elif truncated["v"] or callback_errors:
            state = TOOL_ERROR
        elif p.returncode == 0:
            state = OK  # caller downgrades to OK_EMPTY when there are no parsed records
        else:
            state = TOOL_ERROR
        return {"exit_state": state, "stdout": stdout, "stderr": stderr, "truncated": truncated["v"]}
