"""Fixed diagnostics for an owned archive subprocess; never return raw log data."""
import os
from pathlib import Path
import signal
import stat


ARCHIVE_FAILURE_CODES = frozenset("""
metadata-invalid integrity-failed archive-path-collision archive-path-unsafe
archive-entry-unsupported archive-permissions-unsupported archive-link-unsafe
archive-link-cycle archive-empty archive-invalid archive-entry-limit
cache-budget-exceeded cache-busy filesystem-exists filesystem-missing
filesystem-permission filesystem-full filesystem-read-only filesystem-path-invalid
filesystem-busy compression-invalid archive-extraction-failed timeout canceled
input-unsupported configuration-invalid
""".split())
LOG_SCAN_BYTES = 64 << 10
FATAL_ORIGINS = ("uncaughtException", "unhandledRejection")
SIGNALS = frozenset(("SIGILL", "SIGABRT", "SIGBUS", "SIGKILL", "SIGSEGV", "SIGTERM"))


def archive_failure_code(log):
    for line in log.split("\n"):
        if line.endswith("\r"):
            line = line[:-1]
        if line == "Snapshot archive operation failed":
            return "unknown"
        prefix = "Snapshot archive operation failed: "
        if line.startswith(prefix) and line[len(prefix):] in ARCHIVE_FAILURE_CODES:
            return line[len(prefix):]
    return None


def _log_contract(path):
    fd = None
    try:
        fd = os.open(Path(path), os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW)
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode):
            return None, "unavailable", None, None
        size = info.st_size
        with os.fdopen(fd, "rb") as source:
            fd = None
            if size <= LOG_SCAN_BYTES:
                data = source.read(LOG_SCAN_BYTES)
                scan = "complete" if len(data) == size else "bounded"
                if scan == "bounded":
                    data = data.rpartition(b"\n")[0]
            else:
                half = LOG_SCAN_BYTES // 2
                head = source.read(half)
                # Discard a partial line at each sampling boundary. Otherwise
                # truncation could turn a private suffixed message into a match.
                head = head.rpartition(b"\n")[0]
                source.seek(size - half - 1)
                tail = source.read(half + 1)
                tail = tail[1:] if tail.startswith(b"\n") else tail.partition(b"\n")[2]
                data = head + b"\n" + tail
                scan = "bounded"
        text = data.decode("ascii", errors="replace")
        fatal = next((origin for origin in FATAL_ORIGINS if f"Archive subprocess fatal origin: {origin}" in text.split("\n")), None)
        return size, scan, archive_failure_code(text), fatal
    except (OSError, ValueError, TypeError):
        return None, "unavailable", None, None
    finally:
        if fd is not None:
            os.close(fd)


def archive_command_outcome(log_path, *, returncode=None, timed_out=False):
    result = {"version": 1, "processOutcome": "unknown", "exitCode": None,
              "signal": None, "signalNumber": None, "logBytes": None,
              "archiveFailureCode": None, "fatalOrigin": None, "fixedContractScan": "unavailable"}
    if timed_out:
        result["processOutcome"] = "timed-out"
    elif type(returncode) is int and 0 <= returncode <= 255:
        result.update(processOutcome="exited", exitCode=returncode)
    elif type(returncode) is int and -127 <= returncode < 0:
        number = -returncode
        try:
            name = signal.Signals(number).name
        except ValueError:
            name = "other"
        result.update(processOutcome="signaled", signal=name if name in SIGNALS else "other", signalNumber=number)
    size, scan, code, fatal = _log_contract(log_path)
    result.update(logBytes=size, fixedContractScan=scan, archiveFailureCode=code, fatalOrigin=fatal)
    return result
