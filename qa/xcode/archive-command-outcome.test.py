#!/usr/bin/env python3
"""Bounded private-log classification controls; no Apple qualification claim."""
import json
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True
from archive_command_outcome import LOG_SCAN_BYTES, archive_command_outcome, archive_failure_code


class ArchiveCommandControls(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="lc-archive-outcome-")
        self.addCleanup(self.temporary.cleanup)
        self.log = Path(self.temporary.name) / "private-command.log"

    def test_contract_is_exact_and_never_returns_private_suffixes_or_types(self):
        for line in ("Snapshot archive operation failed: metadata-invalid", "Snapshot archive operation failed: integrity-failed"):
            self.assertIsNotNone(archive_failure_code(line))
        self.assertEqual(archive_failure_code("Snapshot archive operation failed\r\n"), "unknown")
        for line in ("Snapshot archive operation failed: unknown-private-code", "Snapshot archive operation failed: metadata-invalid /private/path",
                     " Snapshot archive operation failed: metadata-invalid", "TypeError: private-token", "private\u2028Snapshot archive operation failed: metadata-invalid"):
            self.assertIsNone(archive_failure_code(line))

    def test_tail_finds_fixed_diagnostic_after_old_four_kib_prefix(self):
        self.log.write_bytes(b"private-token /private/path\n" * 4000 + b"Snapshot archive operation failed: integrity-failed\n")
        result = archive_command_outcome(self.log, returncode=1)
        self.assertEqual(result["archiveFailureCode"], "integrity-failed")
        self.assertEqual(result["fixedContractScan"], "bounded")
        self.assertEqual(result["logBytes"], self.log.stat().st_size)
        self.assertEqual(result["exitCode"], 1)
        self.assertNotIn("private", json.dumps(result))

    def test_scan_boundaries_cannot_promote_a_partial_or_suffixed_line(self):
        prefix = b"Snapshot archive operation failed: metadata-invalid"
        self.log.write_bytes(prefix + b"x" * LOG_SCAN_BYTES + b"private-suffix\n")
        result = archive_command_outcome(self.log, returncode=1)
        self.assertIsNone(result["archiveFailureCode"])
        self.assertEqual(result["fixedContractScan"], "bounded")
        # An unread middle marker is explicitly inconclusive, never a complete scan.
        self.log.write_bytes(b"x\n" * LOG_SCAN_BYTES + prefix + b"\n" + b"x\n" * LOG_SCAN_BYTES)
        self.assertIsNone(archive_command_outcome(self.log, returncode=1)["archiveFailureCode"])

    def test_missing_directory_and_symlink_logs_are_unavailable_without_path_output(self):
        for path in (self.log, self.log.parent):
            result = archive_command_outcome(path, returncode=1)
            self.assertEqual(result["fixedContractScan"], "unavailable")
            self.assertIsNone(result["logBytes"])
        target = self.log.parent / "target"
        target.write_text("private-token")
        self.log.symlink_to(target)
        self.assertEqual(archive_command_outcome(self.log, returncode=1)["fixedContractScan"], "unavailable")

    def test_real_signal_and_timeout_are_distinct_without_inventing_an_exit_code(self):
        with self.log.open("wb") as output:
            child = subprocess.run([sys.executable, "-c", "import os,signal;os.kill(os.getpid(),signal.SIGTERM)"], stdout=output, stderr=subprocess.STDOUT)
        result = archive_command_outcome(self.log, returncode=child.returncode)
        self.assertEqual(result["processOutcome"], "signaled")
        self.assertEqual(result["signal"], "SIGTERM")
        self.assertEqual(result["signalNumber"], signal.SIGTERM)
        self.assertIsNone(result["exitCode"])
        with self.log.open("wb") as output:
            with self.assertRaises(subprocess.TimeoutExpired):
                subprocess.run([sys.executable, "-c", "import time;time.sleep(2)"], stdout=output, stderr=subprocess.STDOUT, timeout=0.05)
        result = archive_command_outcome(self.log, timed_out=True)
        self.assertEqual(result["processOutcome"], "timed-out")
        self.assertIsNone(result["exitCode"])
        self.assertIsNone(result["signalNumber"])

    @unittest.skipUnless(shutil.which("node"), "Node required for actual fatal callback controls")
    def test_node_monitor_preserves_failure_and_reports_only_fixed_origin(self):
        monitor = Path(__file__).with_name("archive-fatal-monitor.mjs").resolve()
        cases = [
            ("setImmediate(() => { throw new Error('private-token /private/path') })", "uncaughtException"),
            ("Promise.reject(new Error('private-token /private/path'))", "unhandledRejection"),
            ("console.error('Snapshot archive operation failed');process.exitCode=1", None),
        ]
        for program, origin in cases:
            with self.log.open("wb") as output:
                child = subprocess.run(["node", "--import", str(monitor), "--eval", program], stdout=output, stderr=subprocess.STDOUT, timeout=5)
            result = archive_command_outcome(self.log, returncode=child.returncode)
            self.assertEqual(result["processOutcome"], "exited")
            self.assertEqual(result["exitCode"], 1)
            self.assertEqual(result["fatalOrigin"], origin)
            self.assertNotIn("private", json.dumps(result))


if __name__ == "__main__":
    unittest.main()
