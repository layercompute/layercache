#!/usr/bin/env python3
"""Linux controls for evidence interpretation; no Apple compiler claim."""
import importlib.util
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

    def test_evidence_directory_cannot_reuse_existing_tree(self):
        with tempfile.TemporaryDirectory() as root:
            with self.assertRaises(RuntimeError):
                probe.Probe(Path("unused"), Path("unused"), Path(root))


if __name__ == "__main__":
    unittest.main()
