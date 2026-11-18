#!/usr/bin/env python3
"""Linux controls for the Swift replay gate's log parsing and comparison
helpers; never a claim about real Xcode or Swift behavior."""
import importlib.util
from pathlib import Path
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True

spec = importlib.util.spec_from_file_location("swift_replay", Path(__file__).with_name("swift-replay-probe.py"))
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


class TaskDiagnostics(unittest.TestCase):
    def test_separates_swift_compile_emit_module_and_clang_by_task(self):
        log = """SwiftEmitModule normal arm64 Emitting module for SwiftReplayLib (in target 'SwiftReplayLib' from project 'Probe')
note: replayed cache hit: SwiftReplayLib
SwiftCompile normal arm64 Compiling\\ Feature1.swift Feature1.swift (in target 'SwiftReplayApp' from project 'Probe')
note: replayed cache miss: Feature1.swift
SwiftCompile normal arm64 Compiling\\ Feature2.swift Feature2.swift (in target 'SwiftReplayApp' from project 'Probe')
note: replayed cache hit: Feature2.swift
CompileC /tmp/control.o /tmp/control.m normal arm64 objective-c
remark: cache hit: control.m
"""
        diagnostics, by_file = probe.task_diagnostics(log, ("SwiftCompile", "SwiftEmitModule", "CompileC"))
        self.assertEqual(diagnostics["SwiftEmitModule"], {"hit": 1, "miss": 0})
        self.assertEqual(diagnostics["SwiftCompile"], {"hit": 1, "miss": 1})
        self.assertEqual(diagnostics["CompileC"], {"hit": 1, "miss": 0})
        self.assertEqual(by_file["Feature1.swift"], {"hit": 0, "miss": 1})
        self.assertEqual(by_file["Feature2.swift"], {"hit": 1, "miss": 0})

    def test_unrelated_task_headers_do_not_leak_into_open_block(self):
        log = """SwiftCompile normal arm64 Compiling\\ main.swift main.swift (in target 'SwiftReplayApp' from project 'Probe')
Ld /tmp/SwiftReplayApp normal
note: replayed cache hit: main.swift
"""
        diagnostics, by_file = probe.task_diagnostics(log, ("SwiftCompile", "SwiftEmitModule", "CompileC"))
        self.assertEqual(diagnostics["SwiftCompile"], {"hit": 0, "miss": 0})
        self.assertEqual(by_file, {})

    def test_requires_exact_task_name_match(self):
        log = """SwiftCompileFoo normal arm64 something
note: replayed cache hit: something
"""
        diagnostics, by_file = probe.task_diagnostics(log, ("SwiftCompile", "SwiftEmitModule", "CompileC"))
        self.assertEqual(diagnostics["SwiftCompile"], {"hit": 0, "miss": 0})
        self.assertEqual(by_file, {})


class NativeCasDiscovery(unittest.TestCase):
    def test_reports_existence_ownership_and_symlink_per_candidate(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            cas_root = root / "native-cas"
            derived = root / "derived"
            (cas_root / "plugin").mkdir(parents=True)
            builtin = cas_root / "builtin"
            builtin.mkdir()
            (derived / "CompilationCache.noindex").mkdir(parents=True)
            candidates, report = probe.discover_native_cas(cas_root, derived)
            self.assertEqual(set(candidates), {"configuredRoot", "configuredPlugin", "configuredBuiltin", "derivedDataBuiltin"})
            self.assertTrue(report["configuredRoot"]["path_existed"] and report["configuredRoot"]["directory"])
            self.assertTrue(report["configuredPlugin"]["path_existed"])
            self.assertTrue(report["configuredBuiltin"]["path_existed"])
            self.assertTrue(report["derivedDataBuiltin"]["path_existed"])

    def test_missing_candidates_report_false_without_raising(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            candidates, report = probe.discover_native_cas(root / "nope", root / "also-nope")
            for entry in report.values():
                self.assertFalse(entry["path_existed"])
                self.assertFalse(entry["directory"])

    def test_clear_native_cas_removes_every_discovered_location_and_derived_data(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            cas_root = root / "native-cas"
            derived = root / "derived"
            (cas_root / "plugin").mkdir(parents=True)
            (derived / "CompilationCache.noindex").mkdir(parents=True)
            (derived / "Build").mkdir()
            result = probe.clear_native_cas(cas_root, derived)
            self.assertFalse(cas_root.exists())
            self.assertFalse(derived.exists())
            self.assertIn("configuredRoot", result["cleared"])
            self.assertIn("configuredPlugin", result["cleared"])

    def test_clear_native_cas_on_already_clean_paths_clears_nothing(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            result = probe.clear_native_cas(root / "native-cas", root / "derived")
            self.assertEqual(result["cleared"], [])


class CompareBuildOutputs(unittest.TestCase):
    def _derived(self, base, name, binary_bytes, object_bytes, module_bytes):
        derived = base / name
        products = derived / "Build/Products/Release"
        products.mkdir(parents=True)
        (products / "SwiftReplayApp").write_bytes(binary_bytes)
        module_dir = products / "SwiftReplayLib.swiftmodule/Project/arm64-apple-macos"
        module_dir.mkdir(parents=True)
        (module_dir / "SwiftReplayLib.swiftmodule").write_bytes(module_bytes)
        objects = derived / "Build/Intermediates.noindex/Probe.build/Release/SwiftReplayApp.build/Objects-normal/arm64"
        objects.mkdir(parents=True)
        (objects / "main.o").write_bytes(object_bytes)
        return derived

    def test_identical_outputs_report_true_for_every_key(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            producer = self._derived(root, "producer", b"bin", b"obj", b"mod")
            consumer = self._derived(root, "consumer", b"bin", b"obj", b"mod")
            comparisons = probe.compare_build_outputs(producer, consumer)
            self.assertTrue(comparisons["allPresentAndIdentical"])
            self.assertTrue(all(value is True for key, value in comparisons.items() if key != "allPresentAndIdentical"))

    def test_divergent_object_fails_only_that_key_and_the_aggregate(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            producer = self._derived(root, "producer", b"bin", b"obj", b"mod")
            consumer = self._derived(root, "consumer", b"bin", b"different-obj", b"mod")
            comparisons = probe.compare_build_outputs(producer, consumer)
            self.assertFalse(comparisons["allPresentAndIdentical"])
            mismatched = [key for key, value in comparisons.items() if key != "allPresentAndIdentical" and value is False]
            self.assertEqual(len(mismatched), 1)

    def test_missing_consumer_output_is_not_identical(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            producer = self._derived(root, "producer", b"bin", b"obj", b"mod")
            consumer = root / "consumer"
            (consumer / "Build/Products/Release").mkdir(parents=True)
            comparisons = probe.compare_build_outputs(producer, consumer)
            self.assertFalse(comparisons["allPresentAndIdentical"])
            self.assertTrue(all(value is False for key, value in comparisons.items() if key != "allPresentAndIdentical"))

    def test_empty_producer_outputs_is_rejected(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            producer = root / "producer"
            (producer / "Build/Products/Release").mkdir(parents=True)
            consumer = self._derived(root, "consumer", b"bin", b"obj", b"mod")
            with self.assertRaises(RuntimeError):
                probe.compare_build_outputs(producer, consumer)


class ExpectedOutput(unittest.TestCase):
    def test_sums_the_three_contributing_values(self):
        self.assertEqual(probe.expected_output(42, 1, 2), "Swift replay probe 45")
        self.assertEqual(probe.expected_output(42, 99, 2), "Swift replay probe 143")


if __name__ == "__main__":
    unittest.main()
