#!/usr/bin/env python3
"""Linux controls for evidence interpretation; no Apple compiler claim."""
import importlib.util
import hashlib
import http.client
import http.server
import io
import json
import threading
from pathlib import Path
import tempfile
import sys
import unittest

sys.dont_write_bytecode = True

spec = importlib.util.spec_from_file_location("standalone", Path(__file__).with_name("standalone-snapshot-probe.py"))
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


class EvidenceControls(unittest.TestCase):
    def test_clang_pch_and_source_diagnostics_are_distinct_from_swift(self):
        log = """ProcessPCH /tmp/Prefix.pch.pch /tmp/Prefix.pch normal arm64
note: replayed cache hit: pch
CompileC /tmp/main.o /tmp/main.m normal arm64 objective-c
note: replayed cache hit: compile
SwiftCompile normal arm64 /tmp/irrelevant.swift
remark: Replay cache miss for key 'swift'
CompileC /tmp/other.o /tmp/other.m normal arm64 objective-c
remark: Replay cache miss for key 'other'
"""
        self.assertEqual(probe.clang_diagnostics(log), {"compile": {"hit": 1, "miss": 0}, "pch": {"hit": 1, "miss": 0}})

    def test_changed_source_needs_compile_miss_not_only_pch_miss(self):
        log = """ProcessPCH /tmp/Prefix.pch.pch /tmp/Prefix.pch normal arm64
note: cache miss: pch
CompileC /tmp/main.o /tmp/main.m normal arm64 objective-c
note: replayed cache hit: compile
"""
        self.assertEqual(probe.clang_diagnostics(log)["compile"]["miss"], 0)

    def test_local_key_lookup_is_not_a_compile_replay(self):
        log = "CompileC /tmp/main.o /tmp/main.m normal arm64 objective-c\nnote: Local cache miss for key: key\n"
        self.assertEqual(probe.clang_diagnostics(log)["compile"], {"hit": 0, "miss": 0})

    def test_output_check_uses_probe_return_register(self):
        assembly = """fixture.o:
(__TEXT,__text) section
_lc_probe_value:
0000000000000000\tmov\tw0, #0x2a
0000000000000004\tret
_other:
0000000000000008\tmov\tw0, #0x2b
000000000000000c\tret
"""
        probe.validate_output(assembly, 42)
        with self.assertRaises(RuntimeError):
            probe.validate_output(assembly, 43)

    def test_old_value_cannot_match_prefix_of_different_immediate(self):
        with self.assertRaises(RuntimeError):
            probe.validate_output("_lc_probe_value:\n00000000 mov w0, #0x2ab\n00000004 ret\n", 42)

    def test_sized_request_hashing_never_reads_beyond_declared_body(self):
        source = io.BytesIO(b"bodyNEXT")
        body = probe.RequestBody(source, 4)
        self.assertEqual(body.read(2), b"bo")
        self.assertEqual(body.read(10), b"dy")
        self.assertEqual(body.read(), b"")
        self.assertEqual(source.read(), b"NEXT")
        self.assertEqual(body.hash.digest(), hashlib.sha256(b"body").digest())
        with self.assertRaises(RuntimeError):
            probe.RequestBody(io.BytesIO(b""), 1).read()

    def test_digest_report_separates_partial_mismatched_and_unpublished_bytes(self):
        value = hashlib.sha256(b"test").digest()
        header = "sha256:" + value.hex()
        complete = probe.body_verification(4, 4, value, header, value)
        self.assertTrue(all(complete.values()))
        self.assertFalse(any(probe.body_verification(5, 4, value, header, value).values()))
        bad_header = probe.body_verification(4, 4, value, "sha256:" + "0" * 64, value)
        self.assertFalse(bad_header["responseDigestMatchesHeader"])
        self.assertTrue(bad_header["responseMatchesPublishedPUT"])
        self.assertFalse(probe.body_verification(4, 4, value, header, None)["responseMatchesPublishedPUT"])
        self.assertNotIn(value.hex(), json.dumps(complete))

    def test_real_proxy_streaming_matches_published_request_without_digest_output(self):
        payload = b"compiler fixture" * 65536

        class Backend(http.server.BaseHTTPRequestHandler):
            body = b""

            def log_message(self, *_):
                pass

            def do_PUT(self):
                Backend.body = self.rfile.read(int(self.headers["Content-Length"]))
                self.send_response(200)
                self.send_header("Content-Length", "0")
                self.end_headers()

            def do_GET(self):
                self.send_response(200)
                self.send_header("Content-Length", str(len(Backend.body)))
                self.send_header("X-LayerCache-Digest", "sha256:" + hashlib.sha256(Backend.body).hexdigest())
                self.end_headers()
                self.wfile.write(Backend.body)

        backend = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Backend)
        audit = probe.Audit(backend.server_address)
        for server in (backend, audit):
            threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            for method in ("PUT", "GET"):
                connection = http.client.HTTPConnection(*audit.server_address, timeout=5)
                connection.request(method, "/v8/artifacts/synthetic", body=payload if method == "PUT" else None)
                response = connection.getresponse()
                self.assertEqual(response.status, 200)
                body = response.read()
                if method == "GET":
                    self.assertEqual(body, payload)
                connection.close()
            # The final response body completes before the proxy appends its record.
            import time
            deadline = time.monotonic() + 2
            while len(audit.records) < 2 and time.monotonic() < deadline:
                time.sleep(0.01)
            self.assertEqual(len(audit.records), 2)
            self.assertTrue(audit.records[0]["publishedRequestComplete"])
            self.assertTrue(audit.records[1]["responseBodyComplete"])
            self.assertTrue(audit.records[1]["responseDigestMatchesHeader"])
            self.assertTrue(audit.records[1]["responseMatchesPublishedPUT"])
            self.assertNotIn(hashlib.sha256(payload).hexdigest(), json.dumps(audit.records))
        finally:
            for server in (audit, backend):
                server.shutdown()
                server.server_close()

    def test_only_allowlisted_native_failure_classification_can_be_published(self):
        self.assertEqual(probe.archive_failure_code("Snapshot archive operation failed: integrity-failed\n"), "integrity-failed")
        self.assertEqual(probe.archive_failure_code("Snapshot archive operation failed\n"), "unknown")
        self.assertIsNone(probe.archive_failure_code("Snapshot archive operation failed: secret:/private/file\n"))
        self.assertIsNone(probe.archive_failure_code("Snapshot archive operation failed: filesystem-missing /private/file\n"))
        self.assertIsNone(probe.archive_failure_code("filesystem-missing"))

    def test_evidence_directory_cannot_reuse_existing_tree(self):
        with tempfile.TemporaryDirectory() as root:
            with self.assertRaises(RuntimeError):
                probe.Probe(Path("unused"), Path("unused"), Path(root))


if __name__ == "__main__":
    unittest.main()
