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


def counts(hit=0, miss=0, unknown=0):
    return {"hit": hit, "miss": miss, "unknown": unknown}


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
        diagnostics, by_file, by_target = probe.task_diagnostics(log, ("SwiftCompile", "SwiftEmitModule", "CompileC"))
        self.assertEqual(diagnostics["SwiftEmitModule"], counts(hit=1))
        self.assertEqual(diagnostics["SwiftCompile"], counts(hit=1, miss=1))
        self.assertEqual(diagnostics["CompileC"], counts(hit=1))
        self.assertEqual(by_file["Feature1.swift"], counts(miss=1))
        self.assertEqual(by_file["Feature2.swift"], counts(hit=1))
        self.assertEqual(by_target["SwiftReplayLib"], counts(hit=1))
        self.assertEqual(by_target["SwiftReplayApp"], counts(hit=1, miss=1))

    def test_real_build_double_remark_shape_counts_one_instance_not_two(self):
        """A real Xcode 27.2 Swift replay printed a "Local cache found for
        key: ..." line AND a separate "Replay cache hit ..." line inside the
        very same SwiftCompile task's block, with mixed casing and some
        lines carrying the "(in target '...' from project '...')" suffix.
        Counting remarks instead of task instances would report 2 hits for
        this 1 task."""
        log = """SwiftCompile normal arm64 Compiling\\ Feature1.swift Feature1.swift (in target 'SwiftReplayApp' from project 'Probe')
note: Local cache found for key: abc123 (in target 'SwiftReplayApp' from project 'Probe')
NOTE: REPLAY CACHE HIT for action abc123
SwiftEmitModule normal arm64 Emitting module for SwiftReplayApp (in target 'SwiftReplayApp' from project 'Probe')
note: cache key query hit for module key
Note: Local Cache Found for key: def456
CompileC /tmp/control.o /tmp/control.m normal arm64 objective-c (in target 'SwiftReplayControl' from project 'Probe')
note: cache hit: control.m
remark: replayed cache hit: control.m
"""
        diagnostics, by_file, by_target = probe.task_diagnostics(log, ("SwiftCompile", "SwiftEmitModule", "CompileC"))
        self.assertEqual(diagnostics["SwiftCompile"], counts(hit=1))
        self.assertEqual(diagnostics["SwiftEmitModule"], counts(hit=1))
        self.assertEqual(diagnostics["CompileC"], counts(hit=1))
        self.assertEqual(by_file["Feature1.swift"], counts(hit=1))
        self.assertEqual(by_target["SwiftReplayApp"], counts(hit=2))

    def test_miss_type_remark_wins_over_an_earlier_hit_type_remark(self):
        log = """SwiftCompile normal arm64 Compiling\\ Feature1.swift Feature1.swift (in target 'SwiftReplayApp' from project 'Probe')
note: cache key query hit for module key
note: Local cache not found for key: abc123
"""
        diagnostics, by_file, by_target = probe.task_diagnostics(log, ("SwiftCompile", "SwiftEmitModule", "CompileC"))
        self.assertEqual(diagnostics["SwiftCompile"], counts(miss=1))
        self.assertEqual(by_file["Feature1.swift"], counts(miss=1))

    def test_unrelated_task_headers_do_not_leak_into_open_block(self):
        log = """SwiftCompile normal arm64 Compiling\\ main.swift main.swift (in target 'SwiftReplayApp' from project 'Probe')
Ld /tmp/SwiftReplayApp normal
note: replayed cache hit: main.swift
"""
        diagnostics, by_file, by_target = probe.task_diagnostics(log, ("SwiftCompile", "SwiftEmitModule", "CompileC"))
        self.assertEqual(diagnostics["SwiftCompile"], counts(unknown=1))
        self.assertEqual(by_file["main.swift"], counts(unknown=1))
        self.assertEqual(by_target["SwiftReplayApp"], counts(unknown=1))

    def test_requires_exact_task_name_match(self):
        log = """SwiftCompileFoo normal arm64 something
note: replayed cache hit: something
"""
        diagnostics, by_file, by_target = probe.task_diagnostics(log, ("SwiftCompile", "SwiftEmitModule", "CompileC"))
        self.assertEqual(diagnostics["SwiftCompile"], counts())
        self.assertEqual(by_file, {})
        self.assertEqual(by_target, {})

    def test_local_cache_found_not_found_forms_count_as_hit_and_miss(self):
        log = """SwiftEmitModule normal arm64 Emitting module for SwiftReplayLib (in target 'SwiftReplayLib' from project 'Probe')
note: Local cache found: SwiftReplayLib
SwiftCompile normal arm64 Compiling\\ Feature1.swift Feature1.swift (in target 'SwiftReplayApp' from project 'Probe')
note: Local cache not found: Feature1.swift
"""
        diagnostics, by_file, by_target = probe.task_diagnostics(log, ("SwiftCompile", "SwiftEmitModule", "CompileC"))
        self.assertEqual(diagnostics["SwiftEmitModule"], counts(hit=1))
        self.assertEqual(diagnostics["SwiftCompile"], counts(miss=1))
        self.assertEqual(by_target["SwiftReplayLib"], counts(hit=1))
        self.assertEqual(by_target["SwiftReplayApp"], counts(miss=1))

    def test_compilec_target_is_not_folded_into_by_target(self):
        log = """CompileC /tmp/control.o /tmp/control.m normal arm64 objective-c (in target 'SwiftReplayControl' from project 'Probe')
remark: cache hit: control.m
"""
        diagnostics, by_file, by_target = probe.task_diagnostics(log, ("SwiftCompile", "SwiftEmitModule", "CompileC"))
        self.assertEqual(diagnostics["CompileC"], counts(hit=1))
        self.assertNotIn("SwiftReplayControl", by_target)

    def test_task_with_no_matching_remark_is_unknown_not_dropped(self):
        log = """SwiftEmitModule normal arm64 Emitting module for SwiftReplayApp (in target 'SwiftReplayApp' from project 'Probe')
note: some unrelated diagnostic
"""
        diagnostics, by_file, by_target = probe.task_diagnostics(log, ("SwiftCompile", "SwiftEmitModule", "CompileC"))
        self.assertEqual(diagnostics["SwiftEmitModule"], counts(unknown=1))
        self.assertEqual(by_target["SwiftReplayApp"], counts(unknown=1))


class RequireMatched(unittest.TestCase):
    def test_passes_and_returns_total_when_something_matched(self):
        total = probe.require_matched("some phase", [{"hit": 1, "miss": 0}, {"hit": 0, "miss": 2}])
        self.assertEqual(total, 3)

    def test_raises_on_all_zero_counts_naming_the_phase_and_patterns(self):
        with self.assertRaisesRegex(RuntimeError, r"some phase matched zero Swift cache remarks"):
            probe.require_matched("some phase", [{"hit": 0, "miss": 0}])

    def test_raises_on_empty_iterable(self):
        with self.assertRaises(RuntimeError):
            probe.require_matched("empty phase", [])

    def test_unknown_only_counts_do_not_count_as_matched(self):
        with self.assertRaises(RuntimeError):
            probe.require_matched("some phase", [counts(unknown=5)])


class CasPathEvidence(unittest.TestCase):
    def test_parses_every_cas_path_argument(self):
        log = "xcodebuild ... -cas-path /tmp/derived/CompilationCache.noindex/plugin -cas-path /tmp/native-cas/plugin\n"
        self.assertEqual(probe.parse_cas_paths(log),
                          [Path("/tmp/derived/CompilationCache.noindex/plugin"), Path("/tmp/native-cas/plugin")])

    def test_reports_relative_form_under_whichever_root_matched(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            derived, cas = root / "derived", root / "native-cas"
            derived.mkdir()
            cas.mkdir()
            log = f"-cas-path {derived / 'CompilationCache.noindex' / 'plugin'}\n"
            found = probe.cas_path_evidence(log, derivedData=derived, nativeCasPath=cas)
            self.assertEqual(found, ["derivedData/CompilationCache.noindex/plugin"])

    def test_reports_under_the_native_cas_root_too(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            derived, cas = root / "derived", root / "native-cas"
            derived.mkdir()
            cas.mkdir()
            log = f"-cas-path {cas / 'plugin'}\n"
            found = probe.cas_path_evidence(log, derivedData=derived, nativeCasPath=cas)
            self.assertEqual(found, ["nativeCasPath/plugin"])

    def test_fails_when_no_cas_path_falls_under_either_root(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            derived, cas = root / "derived", root / "native-cas"
            derived.mkdir()
            cas.mkdir()
            log = "-cas-path /somewhere/else/plugin\n"
            with self.assertRaisesRegex(RuntimeError, r"native reuse location is unproven"):
                probe.cas_path_evidence(log, derivedData=derived, nativeCasPath=cas)

    def test_fails_when_no_cas_path_argument_is_present_at_all(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            derived, cas = root / "derived", root / "native-cas"
            derived.mkdir()
            cas.mkdir()
            with self.assertRaises(RuntimeError):
                probe.cas_path_evidence("no cas path arguments here\n", derivedData=derived, nativeCasPath=cas)


class ChangedSourceVerdict(unittest.TestCase):
    """Exercises the same decision the probe makes in qualify(), given
    synthetic per-module/per-file counts, without needing a Mac."""

    def verdict(self, by_file, by_target):
        feature_one_misses = by_file.get("Feature1.swift", counts()).get("miss", 0)
        lib_target = by_target.get("SwiftReplayLib", counts())
        probe.require_matched("changed-source phase (SwiftReplayLib target)", [lib_target])
        other_module_files_hit = lib_target["miss"] == 0 and lib_target["unknown"] == 0 and lib_target["hit"] > 0
        return feature_one_misses > 0 and other_module_files_hit

    def test_qualifies_when_edited_file_misses_and_other_module_all_hits(self):
        by_file = {"Feature1.swift": counts(miss=1), "Feature2.swift": counts(hit=1), "main.swift": counts(miss=1)}
        by_target = {"SwiftReplayLib": counts(hit=1), "SwiftReplayApp": counts(hit=1, miss=1)}
        self.assertTrue(self.verdict(by_file, by_target))

    def test_fails_when_the_other_module_also_misses(self):
        by_file = {"Feature1.swift": counts(miss=1)}
        by_target = {"SwiftReplayLib": counts(miss=1)}
        self.assertFalse(self.verdict(by_file, by_target))

    def test_fails_when_the_edited_file_did_not_miss(self):
        by_file = {"Feature1.swift": counts(hit=1)}
        by_target = {"SwiftReplayLib": counts(hit=1)}
        self.assertFalse(self.verdict(by_file, by_target))

    def test_fails_when_the_other_module_is_unknown_rather_than_a_clean_hit(self):
        by_file = {"Feature1.swift": counts(miss=1)}
        by_target = {"SwiftReplayLib": counts(hit=1, unknown=1)}
        self.assertFalse(self.verdict(by_file, by_target))

    def test_raises_rather_than_vacuously_passing_when_lib_target_unmatched(self):
        by_file = {"Feature1.swift": counts(miss=1)}
        by_target = {}
        with self.assertRaises(RuntimeError):
            self.verdict(by_file, by_target)

    def test_raises_rather_than_vacuously_passing_when_lib_target_is_all_unknown(self):
        by_file = {"Feature1.swift": counts(miss=1)}
        by_target = {"SwiftReplayLib": counts(unknown=3)}
        with self.assertRaises(RuntimeError):
            self.verdict(by_file, by_target)


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
