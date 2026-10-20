#!/usr/bin/env python3
"""Linux controls for evidence interpretation; no Apple compiler claim."""
import importlib.util
import copy
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
from unittest import mock

sys.dont_write_bytecode = True

spec = importlib.util.spec_from_file_location("standalone", Path(__file__).with_name("standalone-snapshot-probe.py"))
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


class EvidenceControls(unittest.TestCase):
    def test_archive_node_version_is_fixed_semver_only(self):
        self.assertEqual(probe.node_runtime_version("v24.13.0\n"), "v24.13.0")
        for output in ("v24.13.0 secret", "v24.13.0\n/private/secret", "24.13.0", "v024.13.0", "v24.13.0-canary"):
            with self.assertRaises(RuntimeError):
                probe.node_runtime_version(output)

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

    def test_producer_semantics_capture_private_manifest_before_native_cleanup(self):
        with tempfile.TemporaryDirectory() as temporary:
            parent = Path(temporary)
            fixture = probe.Probe(Path("unused"), Path("unused"), parent / "evidence")
            try:
                developer = parent / "Developer"
                library = developer / "usr/lib/libToolchainCASPlugin.dylib"
                library.parent.mkdir(parents=True)
                library.write_text("fake library never loaded")
                fixture.env["DEVELOPER_DIR"] = str(developer)
                fixture.profile = "xcode-standalone-v1-test"
                fixture.shape_helper = fixture.private / "fake-helper"
                store = fixture.private / "producer-store"
                store.mkdir()
                plugin = fixture.private / "native-cas/plugin"
                plugin.mkdir(parents=True)
                marker = plugin / "producer-only-marker"
                marker.write_text("private SDK bytes not for output")
                (fixture.root / "producer-build.log").write_text(f"ValidateCAS {plugin} actual-tool\n")
                called = []

                def run(name, arguments, **_):
                    called.append(name)
                    self.assertTrue(marker.is_file())
                    if name == "producer-semantic-manifest":
                        self.assertEqual(arguments[1], store)
                        manifest = Path(arguments[-1])
                        self.assertEqual(manifest.parent, fixture.private)
                        manifest.write_text(json.dumps({"version": 1, "project": probe.PROJECT,
                            "targets": [{"ordinal": 1, "nativeDigestHex": "0" * 130, "wireReferenceCount": 0,
                                         "wireReferencesComplete": True, "wireDataBytes": None}]}))
                        manifest.chmod(0o600)
                        return "{}"
                    self.assertEqual(name, "producer-plugin-semantics")
                    self.assertEqual(arguments[arguments.index("--cas-path") + 1], plugin)
                    return json.dumps({"version": 1, "available": True, "reason": "available", "abi": {"major": 0, "minor": 1},
                        "workerStopped": True, "objects": [{"ordinal": 1, "available": False, "reason": "notFound", "dataBytes": None,
                            "referenceCount": None, "wireReferenceCountMatches": None, "wireDataBytesMatches": None,
                            "comparisonComplete": False, "referenceObjectsChecked": 0, "referenceDataBytes": 0,
                            "inlineReferenceCountMatches": None, "inlineReferencesMatch": None,
                            "inlineReferenceMultisetMatches": None, "referencedObjectsAreLeaves": None,
                            "indexedOneBasedReferencesMatch": None, "indexedZeroBasedReferencesMatch": None}]})

                with mock.patch.object(fixture, "run", side_effect=run):
                    self.assertTrue(fixture.collect_producer_semantics(store))
                self.assertEqual(called, ["producer-semantic-manifest", "producer-plugin-semantics"])
                result = json.loads((fixture.root / "plugin-semantics.json").read_text())
                self.assertTrue(result["available"])
                self.assertFalse(result["objects"][0]["available"])
                locations = json.loads((fixture.root / "native-cas-locations.json").read_text())
                self.assertEqual(locations["phase"], "producer-closed-before-consumer")
                self.assertTrue(locations["storeConfirmedByBuild"])
                self.assertEqual(plugin.stat().st_mode & 0o777, 0o700)
                self.assertTrue(marker.is_file())
                public = json.dumps(result) + json.dumps(locations)
                self.assertNotIn("0" * 130, public)
                self.assertNotIn(str(parent), public)
                self.assertNotIn(marker.read_text(), public)
            finally:
                fixture.close()

    def test_indexed_mapping_gate_requires_every_declared_native_target(self):
        shapes = {"available": True, "objects": [{"ordinal": 7, "expectedNativeNode": True, "node": {
            "sequenceComplete": True, "onlyTopLevelChunks": True, "markerCount": 1, "literalChunkCount": 3,
            "chunks": [{"field": {"number": 2}, "leadingUniqueMarker": True,
                        "canonicalPermutationOneBased": True, "indexCountMatchesLiterals": True}]}}]}
        semantics = {"available": True, "workerStopped": True, "objects": [{"ordinal": 7,
            "available": True, "comparisonComplete": True, "inlineReferenceCountMatches": True,
            "inlineReferenceMultisetMatches": True, "referencedObjectsAreLeaves": True,
            "indexedOneBasedReferencesMatch": True, "indexedZeroBasedReferencesMatch": None,
            "dataBytes": 0, "referenceCount": 3, "referenceObjectsChecked": 3}]}
        self.assertTrue(probe.semantic_mapping_qualified(shapes, semantics))
        for key in ("available", "comparisonComplete", "inlineReferenceCountMatches",
                    "inlineReferenceMultisetMatches", "referencedObjectsAreLeaves", "indexedOneBasedReferencesMatch"):
            changed = copy.deepcopy(semantics)
            changed["objects"][0][key] = False
            self.assertFalse(probe.semantic_mapping_qualified(shapes, changed), key)
        for key in ("sequenceComplete", "onlyTopLevelChunks"):
            changed = copy.deepcopy(shapes)
            changed["objects"][0]["node"][key] = False
            self.assertFalse(probe.semantic_mapping_qualified(changed, semantics), key)
        for key in ("leadingUniqueMarker", "canonicalPermutationOneBased", "indexCountMatchesLiterals"):
            changed = copy.deepcopy(shapes)
            changed["objects"][0]["node"]["chunks"][0][key] = False
            self.assertFalse(probe.semantic_mapping_qualified(changed, semantics), key)
        for value in ([], [semantics["objects"][0]] * 2, [{**semantics["objects"][0], "ordinal": 8}]):
            self.assertFalse(probe.semantic_mapping_qualified(shapes, {**semantics, "objects": value}))
        zero = copy.deepcopy(semantics)
        zero["objects"][0].update(indexedOneBasedReferencesMatch=None, indexedZeroBasedReferencesMatch=True)
        self.assertFalse(probe.semantic_mapping_qualified(shapes, zero))
        self.assertFalse(probe.semantic_mapping_qualified({"available": True, "objects": []}, semantics))

    def test_unqualified_mapping_prevents_publication_and_consumer_start(self):
        with tempfile.TemporaryDirectory() as temporary:
            fixture = probe.Probe(Path("unused"), Path("unused"), Path(temporary) / "evidence")
            try:
                with mock.patch.object(fixture, "fingerprint"), mock.patch.object(fixture, "prepare_project"), \
                     mock.patch.object(fixture, "team"), mock.patch.object(fixture, "adapter", return_value="producer"), \
                     mock.patch.object(fixture, "build", return_value={}) as build, \
                     mock.patch.object(fixture, "stop", return_value={}), \
                     mock.patch.object(fixture, "collect_producer_semantics", return_value=True), \
                     mock.patch.object(fixture, "archive") as archive:
                    with self.assertRaisesRegex(RuntimeError, "indexed leaf mapping is not qualified"):
                        fixture.qualify()
                    self.assertEqual(build.call_count, 1)
                    archive.assert_not_called()
            finally:
                fixture.close()

    def test_archive_command_failure_and_timeout_keep_safe_outcomes(self):
        with tempfile.TemporaryDirectory() as temporary:
            fixture = probe.Probe(Path("unused"), Path("unused"), Path(temporary) / "evidence")
            try:
                def failure(*args, **kwargs):
                    kwargs["stdout"].write(b"private failure /private/sdk/path\nSnapshot archive operation failed: integrity-failed\n")
                    return probe.subprocess.CompletedProcess(args[0], -6)
                with mock.patch.object(probe.subprocess, "run", side_effect=failure):
                    with self.assertRaises(RuntimeError):
                        fixture.run("reader-restore", ["unused"])
                self.assertEqual(fixture.last_archive_outcome["processOutcome"], "signaled")
                self.assertEqual(fixture.last_archive_failure_code, "integrity-failed")
                self.assertNotIn("/private/sdk", json.dumps(fixture.last_archive_outcome))
                with mock.patch.object(probe.subprocess, "run", side_effect=probe.subprocess.TimeoutExpired("unused", 1)):
                    with self.assertRaises(probe.subprocess.TimeoutExpired):
                        fixture.run("reader-restore", ["unused"], timeout=1)
                self.assertEqual(fixture.last_archive_outcome["processOutcome"], "timed-out")
                self.assertIsNone(fixture.last_archive_outcome["exitCode"])
                self.assertIn("reader-restore", fixture.timings)
            finally:
                fixture.close()

    def test_missing_producer_native_store_is_unavailable_without_sdk_load(self):
        with tempfile.TemporaryDirectory() as temporary:
            fixture = probe.Probe(Path("unused"), Path("unused"), Path(temporary) / "evidence")
            try:
                developer = fixture.private / "Developer"
                library = developer / "usr/lib/libToolchainCASPlugin.dylib"
                library.parent.mkdir(parents=True)
                library.write_text("fake library never loaded")
                fixture.env["DEVELOPER_DIR"] = str(developer)
                with mock.patch.object(fixture, "run") as run:
                    self.assertTrue(fixture.collect_producer_semantics(fixture.private / "producer-store"))
                    run.assert_not_called()
                result = json.loads((fixture.root / "plugin-semantics.json").read_text())
                self.assertFalse(result["available"])
                self.assertFalse((fixture.private / "native-cas").exists())
            finally:
                fixture.close()

    def test_unconfirmed_native_worker_prevents_consumer_cleanup_or_publication(self):
        with tempfile.TemporaryDirectory() as temporary:
            fixture = probe.Probe(Path("unused"), Path("unused"), Path(temporary) / "evidence")
            try:
                native = fixture.private / "native-cas/plugin"
                native.mkdir(parents=True)
                marker = native / "preserve-while-worker-unconfirmed"
                marker.write_text("owned native data")
                with mock.patch.object(fixture, "fingerprint"), mock.patch.object(fixture, "prepare_project"), \
                     mock.patch.object(fixture, "team"), mock.patch.object(fixture, "adapter", return_value="producer"), \
                     mock.patch.object(fixture, "build", return_value={}) as build, \
                     mock.patch.object(fixture, "stop", return_value={}), \
                     mock.patch.object(fixture, "collect_producer_semantics", return_value=False), \
                     mock.patch.object(fixture, "archive") as archive:
                    with self.assertRaisesRegex(RuntimeError, "worker exit is unconfirmed"):
                        fixture.qualify()
                    self.assertEqual(build.call_count, 1)
                    self.assertEqual(build.call_args.args[0], "producer")
                    archive.assert_not_called()
                self.assertTrue(marker.is_file())
            finally:
                fixture.close()

    def test_evidence_directory_cannot_reuse_existing_tree(self):
        with tempfile.TemporaryDirectory() as root:
            with self.assertRaises(RuntimeError):
                probe.Probe(Path("unused"), Path("unused"), Path(root))


if __name__ == "__main__":
    unittest.main()
