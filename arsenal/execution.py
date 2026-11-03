"""Nonblocking scanner execution with per-tool ownership and a shared budget.

Cancellation belongs to one invocation, not to whichever scan happens to be
active later. A cancelled await signals its worker thread; that thread kills
and reaps its process tree before releasing either execution slot.
"""

import asyncio
import threading

from arsenal.contracts import BUSY, CAPS
from arsenal.execmgr import Runner


class ScanExecutor:
    def __init__(self, max_parallel=2):
        if max_parallel < 1:
            raise ValueError("max_parallel must be positive")
        self._slots = threading.BoundedSemaphore(max_parallel)
        self._runners = {tool: Runner() for tool in CAPS}

    def run_sync(self, tool, argv, timeout, cancel_event=None, on_line=None, env=None, cwd=None):
        if not self._slots.acquire(blocking=False):
            return {"exit_state": BUSY, "stdout": "", "stderr": "shared scan limit reached", "truncated": False}
        try:
            return self._runners[tool].run(argv, timeout, cancel_event=cancel_event,
                                           on_line=on_line, env=env, cwd=cwd)
        finally:
            self._slots.release()

    async def run(self, tool, argv, timeout, env=None, cwd=None):
        stop = threading.Event()
        try:
            return await asyncio.to_thread(self.run_sync, tool, argv, timeout, stop, None, env, cwd)
        except asyncio.CancelledError:
            stop.set()
            raise
