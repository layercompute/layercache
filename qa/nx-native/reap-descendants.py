"""Reap orphaned descendants from one Linux-only process-group regression."""

import ctypes
import os
import signal
import subprocess
import sys
import time


libc = ctypes.CDLL(None, use_errno=True)
if libc.prctl(36, 1, 0, 0, 0) != 0:  # PR_SET_CHILD_SUBREAPER, scoped to this fixture.
    raise OSError(ctypes.get_errno(), "cannot become the fixture subreaper")

child = subprocess.Popen(sys.argv[1:], start_new_session=True)
result = None
deadline = time.monotonic() + 10
cleaning_since = None


def request_cleanup(_signal, _frame):
    global deadline
    deadline = 0


for stop_signal in (signal.SIGINT, signal.SIGTERM):
    signal.signal(stop_signal, request_cleanup)

while True:
    try:
        pid, status = os.waitpid(-1, os.WNOHANG)
    except ChildProcessError:
        break
    if pid == child.pid:
        result = child.returncode = os.waitstatus_to_exitcode(status)
    if time.monotonic() >= deadline:
        if cleaning_since is None:
            cleaning_since = time.monotonic()
        # Kill direct children, then repeat as their orphaned descendants are
        # adopted here. Each controller has a separate session, so killing its
        # group cannot kill the fixture which must reap it.
        owned = f"/proc/{os.getpid()}/task/{os.getpid()}/children"
        with open(owned, encoding="ascii") as children:
            for value in children.read().split():
                try:
                    os.killpg(os.getpgid(int(value)), signal.SIGKILL)
                except ProcessLookupError:
                    pass
        if time.monotonic() - cleaning_since > 2:
            raise TimeoutError("descendant fixture cleanup exceeded two seconds")
    if pid == 0:
        time.sleep(0.005)
sys.exit(124 if cleaning_since is not None else result if result is not None else 1)
