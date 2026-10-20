#!/usr/bin/env python3
"""Exercise the QA probe against real, small C ABI fixtures, without an SDK."""

import copy
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock


sys.dont_write_bytecode = True
PROBE_PATH = Path(__file__).with_name("plugin-semantics.py")
SPEC = importlib.util.spec_from_file_location("layercache_plugin_semantics_tested", PROBE_PATH)
PROBE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PROBE)
PROJECT = "github.com/layercache/xcode-standalone-qa"
PRIVATE_NATIVE_TEXT = "DO-NOT-REPORT-native-error-/sdk/private/Header.h-" + "ab" * 65

# The declarations below follow the pinned LLVM 0.1 headers named by the probe.
# Deliberately large opaque values catch truncated ctypes return/argument types.
# Data.data is deliberately unreadable: this probe must inspect sizes only.
FAKE_C = r"""
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/resource.h>
#include <unistd.h>
#if defined(__linux__)
#include <sys/prctl.h>
#endif

typedef struct { const uint8_t *data; size_t size; } Digest;
typedef struct { const void *data; size_t size; } Data;
typedef struct { uint64_t opaque; } ObjectID;
typedef struct { uint64_t opaque; } Loaded;
typedef struct { uint64_t opaque_b; uint64_t opaque_e; } Refs;

#define OPTIONS ((void *)(uintptr_t)UINT64_C(0x12345678abcdef00))
#define CAS ((void *)(uintptr_t)UINT64_C(0x23456789abcdef00))
#define OBJECT_BASE UINT64_C(0x3456789abcde0000)
#define LOADED_BASE UINT64_C(0x456789abcdef0000)
#define REFS_BEGIN UINT64_C(0x56789abcdef00000)
#define REFS_END UINT64_C(0x6789abcdef000000)

static unsigned client_major = 99, client_minor = 99;
static char on_disk_path[4096];

static void audit(char call) {
    FILE *file = fopen(AUDIT_PATH, "a");
    if (file) { fputc(call, file); fclose(file); }
}

static void fail(char **error) { *error = strdup(PRIVATE_TEXT); }

__attribute__((constructor)) static void on_load(void) {
    audit('L');
    if (SCENARIO == 12) {
        struct rlimit limit;
        if (getrlimit(RLIMIT_CORE, &limit) != 0 || limit.rlim_cur != 0 || limit.rlim_max != 0) {
            audit('!'); _exit(88);
        }
#if defined(__linux__)
        if (prctl(PR_GET_DUMPABLE, 0L, 0L, 0L, 0L) != 0) {
            audit('!'); _exit(88);
        }
#endif
        audit('Z');
        abort();
    }
    puts(PRIVATE_TEXT);
    fprintf(stderr, "%s\n", PRIVATE_TEXT);
}

void llcas_get_plugin_version(unsigned *major, unsigned *minor) {
    audit('V');
    if (SCENARIO == 1) sleep(20);
    *major = ABI_MAJOR; *minor = ABI_MINOR;
}

void llcas_string_dispose(char *string) { audit('E'); free(string); }
void *llcas_cas_options_create(void) { audit('O'); return OPTIONS; }
void llcas_cas_options_dispose(void *options) { audit(options == OPTIONS ? 'D' : '!'); }

void llcas_cas_options_set_client_version(void *options, unsigned major, unsigned minor) {
    audit(options == OPTIONS ? 'A' : '!');
    client_major = major; client_minor = minor;
}

void llcas_cas_options_set_ondisk_path(void *options, const char *path) {
    audit(options == OPTIONS ? 'P' : '!');
    snprintf(on_disk_path, sizeof(on_disk_path), "%s", path);
}

void *llcas_cas_create(void *options, char **error) {
    audit('C');
    if (SCENARIO == 3 || options != OPTIONS || client_major != 0 || client_minor != 1 ||
        !strstr(on_disk_path, "/native-cas")) {
        fail(error); return NULL;
    }
    return CAS;
}

void llcas_cas_dispose(void *cas) { audit(cas == CAS ? 'X' : '!'); }

bool llcas_cas_get_objectid(void *cas, Digest digest, ObjectID *id, char **error) {
    audit('G');
    if (cas != CAS || digest.size != 65 || digest.data == NULL || digest.data[64] == 254) {
        fail(error); return true;
    }
    id->opaque = OBJECT_BASE | digest.data[64];
    return false;
}

unsigned llcas_cas_contains_object(void *cas, ObjectID id, bool globally, char **error) {
    audit('I');
    if (cas != CAS || globally || (id.opaque & ~UINT64_C(255)) != OBJECT_BASE || SCENARIO == 9) {
        fail(error); return 2;
    }
    if ((id.opaque & 255) == 253) return 1;
    return 0;
}

unsigned llcas_cas_load_object(void *cas, ObjectID id, Loaded *loaded, char **error) {
    audit('Q');
    if (cas != CAS || (id.opaque & ~UINT64_C(255)) != OBJECT_BASE || SCENARIO == 10) {
        fail(error); return 2;
    }
    loaded->opaque = LOADED_BASE | (id.opaque & 255);
    return 0;
}

Data llcas_loaded_object_get_data(void *cas, Loaded loaded) {
    audit('B');
    if (cas != CAS || (loaded.opaque & ~UINT64_C(255)) != LOADED_BASE)
        return (Data){NULL, 1};
    size_t index = loaded.opaque & 255;
    size_t bytes = index == 1 ? 3145729 : (index == 2 ? 33 : 17);
    if (SCENARIO == 6) bytes = (64 * 1024 * 1024) + 1;
    if (SCENARIO == 11) bytes = 40 * 1024 * 1024;
    return (Data){SCENARIO == 8 ? NULL : (const void *)(uintptr_t)1, bytes};
}

Refs llcas_loaded_object_get_refs(void *cas, Loaded loaded) {
    audit('R');
    if (cas != CAS || (loaded.opaque & ~UINT64_C(255)) != LOADED_BASE)
        return (Refs){0, 0};
    return (Refs){REFS_BEGIN | (loaded.opaque & 255), REFS_END | (loaded.opaque & 255)};
}

#if SCENARIO != 2
size_t llcas_object_refs_get_count(void *cas, Refs refs) {
    audit('N');
    if (cas != CAS || (refs.opaque_b & ~UINT64_C(255)) != REFS_BEGIN ||
        (refs.opaque_e & ~UINT64_C(255)) != REFS_END ||
        (refs.opaque_b & 255) != (refs.opaque_e & 255)) return SIZE_MAX;
    if (SCENARIO == 7) return 100001;
    size_t index = refs.opaque_b & 255;
    return index == 1 ? 3 : (index == 2 ? 1 : 0);
}
#endif
"""


def target(ordinal=1, index=0, references=0, complete=True, data_bytes=None):
    return {
        "ordinal": ordinal,
        "nativeDigestHex": "00" * 64 + f"{index:02x}",
        "wireReferenceCount": references,
        "wireReferencesComplete": complete,
        "wireDataBytes": data_bytes,
    }


class PluginSemanticsTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.compiler = shutil.which("cc")
        if cls.compiler is None:
            raise unittest.SkipTest("the C ABI fixture requires cc")

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="lc-plugin-semantics-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.cas = self.root / "native-cas"
        self.cas.mkdir(mode=0o700)
        self.manifest = self.root / "targets.json"
        self.audit = self.root / "native-calls"
        self.write_manifest()

    def write_manifest(self, value=None):
        if value is None:
            value = {"version": 1, "project": PROJECT, "targets": [target()]}
        self.manifest.write_text(json.dumps(value), encoding="utf-8")
        self.manifest.chmod(0o600)

    def library(self, scenario=0, major=0, minor=1):
        path = self.root / f"plugin-{scenario}-{major}-{minor}.so"
        source = path.with_suffix(".c")
        definitions = (
            f"#define SCENARIO {scenario}\n#define ABI_MAJOR {major}\n#define ABI_MINOR {minor}\n"
            f"#define AUDIT_PATH {json.dumps(str(self.audit))}\n"
            f"#define PRIVATE_TEXT {json.dumps(PRIVATE_NATIVE_TEXT)}\n"
        )
        source.write_text(definitions + FAKE_C, encoding="utf-8")
        source.chmod(0o600)
        shared = "-dynamiclib" if sys.platform == "darwin" else "-shared"
        subprocess.run(
            [self.compiler, "-std=c11", "-D_POSIX_C_SOURCE=200809L", shared, "-fPIC", "-O0", "-o", str(path), str(source)],
            check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=15,
        )
        return path

    def calls(self):
        return self.audit.read_text(encoding="ascii") if self.audit.exists() else ""

    def assert_private_report(self, value):
        serialized = json.dumps(value)
        self.assertNotIn(PRIVATE_NATIVE_TEXT, serialized)
        self.assertNotIn(str(self.root), serialized)
        self.assertNotIn("00" * 65, serialized)
        self.assertTrue(PROBE.valid_report(value), value)

    def test_actual_abi_counts_comparisons_and_handle_cleanup(self):
        self.write_manifest({"version": 1, "project": PROJECT, "targets": [
            target(1, 0, 0, True, 17),
            target(2, 1, 3, True, 3145728),
            target(3, 2, 0, False, None),
        ]})
        result = PROBE.run_probe(self.library(), self.cas, self.manifest)
        self.assertEqual((result["version"], result["available"], result["reason"], result["abi"]),
                         (1, True, "available", {"major": 0, "minor": 1}))
        expected = [(17, 0, True, True), (3145729, 3, True, False), (33, 1, None, None)]
        for ordinal, (item, values) in enumerate(zip(result["objects"], expected, strict=True), 1):
            self.assertEqual((item["ordinal"], item["available"], item["reason"]), (ordinal, True, "available"))
            self.assertEqual(tuple(item[key] for key in ("dataBytes", "referenceCount", "wireReferenceCountMatches", "wireDataBytesMatches")), values)
        self.assertEqual(self.calls().count("G"), 3)
        self.assertEqual(self.calls().count("Q"), 3)
        self.assertEqual(self.calls().count("X"), 1)
        self.assertEqual(self.calls().count("D"), 1)
        self.assertNotIn("!", self.calls(), "an opaque ABI handle was truncated or replaced")
        self.assert_private_report(result)

    def test_native_stdout_and_stderr_do_not_escape_the_calling_process(self):
        library = self.library()
        code = (
            "import importlib.util,json,pathlib,sys;sys.dont_write_bytecode=True;"
            "s=importlib.util.spec_from_file_location('probe',sys.argv[1]);"
            "m=importlib.util.module_from_spec(s);s.loader.exec_module(m);"
            "print(json.dumps(m.run_probe(pathlib.Path(sys.argv[2]),pathlib.Path(sys.argv[3]),pathlib.Path(sys.argv[4]))))"
        )
        environment = dict(os.environ, PYTHONDONTWRITEBYTECODE="1")
        completed = subprocess.run(
            [sys.executable, "-c", code, str(PROBE_PATH), str(library), str(self.cas), str(self.manifest)],
            capture_output=True, text=True, timeout=5, check=True, env=environment,
        )
        self.assertEqual(completed.stderr, "")
        self.assertNotIn(PRIVATE_NATIVE_TEXT, completed.stdout)
        result = json.loads(completed.stdout)
        self.assertTrue(result["available"], result)
        self.assert_private_report(result)

    def test_bogus_digest_and_native_error_strings_are_disposed_without_disclosure(self):
        for scenario in (0, 9, 10):
            with self.subTest(scenario=scenario):
                self.audit.unlink(missing_ok=True)
                self.write_manifest({"version": 1, "project": PROJECT, "targets": [target(index=254 if scenario == 0 else 0)]})
                result = PROBE.run_probe(self.library(scenario=scenario), self.cas, self.manifest)
                self.assertTrue(result["available"], result)
                self.assertEqual((result["objects"][0]["available"], result["objects"][0]["reason"]), (False, "nativeError"))
                self.assertEqual(self.calls().count("E"), 1, "native error pointer was not disposed once")
                self.assertEqual(self.calls().count("X"), 1)
                self.assertEqual(self.calls().count("D"), 1)
                self.assert_private_report(result)

    def test_not_found_is_an_object_outcome_and_never_loads_it(self):
        self.write_manifest({"version": 1, "project": PROJECT, "targets": [target(index=253)]})
        result = PROBE.run_probe(self.library(), self.cas, self.manifest)
        self.assertTrue(result["available"], result)
        self.assertEqual((result["objects"][0]["available"], result["objects"][0]["reason"]), (False, "notFound"))
        self.assertNotIn("Q", self.calls())
        self.assert_private_report(result)

    def test_missing_symbol_and_unknown_abi_do_not_create_a_cas(self):
        for scenario, major, minor, reason, abi in (
            (2, 0, 1, "missingSymbol", None),
            (0, 7, 0, "unsupportedABI", {"major": 7, "minor": 0}),
        ):
            with self.subTest(reason=reason):
                self.audit.unlink(missing_ok=True)
                result = PROBE.run_probe(self.library(scenario, major, minor), self.cas, self.manifest)
                self.assertEqual((result["available"], result["reason"], result["abi"], result["objects"]), (False, reason, abi, []))
                self.assertNotIn("O", self.calls())
                self.assertNotIn("C", self.calls())
                self.assert_private_report(result)

    def test_native_create_error_releases_options_and_disposes_its_error(self):
        result = PROBE.run_probe(self.library(scenario=3), self.cas, self.manifest)
        self.assertEqual((result["available"], result["reason"]), (False, "nativeError"))
        self.assertEqual(self.calls().count("E"), 1)
        self.assertEqual(self.calls().count("D"), 1)
        self.assertNotIn("X", self.calls())
        self.assert_private_report(result)

    def test_data_reference_and_null_pointer_bounds_do_not_dereference_payloads(self):
        self.write_manifest({"version": 1, "project": PROJECT, "targets": [
            target(1, 0), target(2, 1), target(3, 2),
        ]})
        for scenario in (6, 7, 8):
            with self.subTest(scenario=scenario):
                self.audit.unlink(missing_ok=True)
                result = PROBE.run_probe(self.library(scenario=scenario), self.cas, self.manifest)
                self.assertEqual((result["available"], result["reason"], result["objects"]), (False, "bounds", []))
                self.assertEqual(self.calls().count("G"), 1)
                self.assertEqual(self.calls().count("Q"), 1)
                self.assertEqual(self.calls().count("B"), 1)
                self.assertEqual(self.calls().count("X"), 1)
                self.assertEqual(self.calls().count("D"), 1)
                self.assert_private_report(result)

    def test_native_stall_has_a_bounded_child_timeout(self):
        library = self.library(scenario=1)
        started = time.monotonic()
        result = PROBE.run_probe(library, self.cas, self.manifest, timeout=0.2)
        elapsed = time.monotonic() - started
        self.assertEqual((result["available"], result["reason"], result["objects"]), (False, "timeout", []))
        self.assertIs(result["workerStopped"], True)
        self.assertLess(elapsed, 3, "native sleep escaped the caller's timeout")
        self.assertIn("V", self.calls(), "the timeout did not reach the native fixture")
        self.assert_private_report(result)

    def test_unconfirmed_worker_teardown_returns_without_an_unbounded_wait(self):
        process = mock.Mock()
        process.communicate.side_effect = [
            subprocess.TimeoutExpired("owned-native-worker", 0.25),
            subprocess.TimeoutExpired("owned-native-worker", 0.1),
        ]
        with mock.patch.object(PROBE.subprocess, "Popen", return_value=process):
            result = PROBE.run_probe(self.root / "not-loaded.so", self.cas, self.manifest, timeout=0.25)
        self.assertEqual((result["available"], result["reason"], result["workerStopped"], result["objects"]),
                         (False, "timeout", False, []))
        self.assertEqual(process.communicate.call_args_list, [mock.call(timeout=0.25), mock.call(timeout=0.1)])
        process.kill.assert_called_once_with()
        process.stdout.close.assert_called_once_with()
        process.wait.assert_not_called()
        self.assert_private_report(result)

    def test_crashing_native_constructor_has_no_core_and_a_fixed_private_failure(self):
        library = self.library(scenario=12)
        code = (
            "import importlib.util,json,pathlib,sys;sys.dont_write_bytecode=True;"
            "s=importlib.util.spec_from_file_location('probe',sys.argv[1]);"
            "m=importlib.util.module_from_spec(s);s.loader.exec_module(m);"
            "print(json.dumps(m.run_probe(pathlib.Path(sys.argv[2]),pathlib.Path(sys.argv[3]),pathlib.Path(sys.argv[4]),timeout=1)))"
        )
        started = time.monotonic()
        completed = subprocess.run(
            [sys.executable, "-c", code, str(PROBE_PATH.resolve()), str(library), str(self.cas), str(self.manifest)],
            cwd=self.root, capture_output=True, text=True, timeout=4, check=True,
            env=dict(os.environ, PYTHONDONTWRITEBYTECODE="1"),
        )
        self.assertLess(time.monotonic() - started, 3)
        self.assertEqual(completed.stderr, "")
        result = json.loads(completed.stdout)
        self.assertEqual((result["available"], result["reason"], result["objects"]), (False, "workerFailed", []))
        self.assertEqual(self.calls(), "LZ", "native crash did not first verify core limits and native dump protection")
        cores = [path.name for path in self.root.rglob("*") if path.name == "core" or path.name.startswith("core.")]
        self.assertEqual(cores, [], "native crash wrote a core into the private working directory")
        self.assert_private_report(result)

    def test_aggregate_data_bound_stops_before_loading_another_object(self):
        self.write_manifest({"version": 1, "project": PROJECT, "targets": [
            target(1, 0), target(2, 1), target(3, 2),
        ]})
        # Every object is individually valid (40 MiB < 64 MiB). The second
        # crosses the aggregate bound, so the third native load must not start.
        result = PROBE.run_probe(self.library(scenario=11), self.cas, self.manifest)
        self.assertEqual((result["available"], result["reason"], result["objects"]), (False, "bounds", []))
        self.assertEqual(self.calls().count("G"), 2)
        self.assertEqual(self.calls().count("Q"), 2)
        self.assertEqual(self.calls().count("B"), 2)
        self.assertEqual(self.calls().count("X"), 1)
        self.assertEqual(self.calls().count("D"), 1)
        self.assert_private_report(result)

    def test_invalid_manifest_shapes_are_rejected_before_loading_native_code(self):
        library = self.library()
        valid = {"version": 1, "project": PROJECT, "targets": [target()]}
        cases = {}
        for name, key, value in (
            ("wrong version", "version", 2),
            ("boolean version", "version", True),
            ("foreign project", "project", "github.com/foreign/project"),
            ("too many targets", "targets", [target()] * 257),
        ):
            case = copy.deepcopy(valid)
            case[key] = value
            cases[name] = case
        for name, key, value in (
            ("short digest", "nativeDigestHex", "00" * 64),
            ("nonhex digest", "nativeDigestHex", "zz" * 65),
            ("boolean ordinal", "ordinal", True),
            ("negative data", "wireDataBytes", -1),
            ("oversized data", "wireDataBytes", (64 << 20) + 1),
            ("oversized refs", "wireReferenceCount", 100001),
            ("nonboolean completeness", "wireReferencesComplete", 1),
        ):
            case = copy.deepcopy(valid)
            case["targets"][0][key] = value
            cases[name] = case
        for name, value in cases.items():
            with self.subTest(name=name):
                self.write_manifest(value)
                result = PROBE.run_probe(library, self.cas, self.manifest)
                self.assertEqual((result["available"], result["reason"]), (False, "invalidInput"))
                self.assertFalse(self.audit.exists(), "invalid input loaded the native library")
                self.assert_private_report(result)

    def test_manifest_permissions_links_and_fifo_reject_before_native_load(self):
        library = self.library()
        self.manifest.chmod(0o644)
        result = PROBE.run_probe(library, self.cas, self.manifest)
        self.assertEqual(result["reason"], "invalidInput")
        self.manifest.chmod(0o600)
        linked = self.root / "hardlinked.json"
        os.link(self.manifest, linked)
        result = PROBE.run_probe(library, self.cas, self.manifest)
        self.assertEqual(result["reason"], "invalidInput")
        linked.unlink()
        symlink = self.root / "symlinked.json"
        symlink.symlink_to(self.manifest)
        result = PROBE.run_probe(library, self.cas, symlink)
        self.assertEqual(result["reason"], "invalidInput")
        fifo = self.root / "manifest.fifo"
        os.mkfifo(fifo, 0o600)
        started = time.monotonic()
        result = PROBE.run_probe(library, self.cas, fifo, timeout=0.5)
        self.assertEqual(result["reason"], "invalidInput")
        self.assertLess(time.monotonic() - started, 2)
        self.assertFalse(self.audit.exists(), "unsafe manifest loaded the native library")

    def test_fixture_path_and_manifest_byte_bounds_reject_before_native_load(self):
        library = self.library()
        unrelated = self.root / "unrelated-cas"
        unrelated.mkdir(mode=0o700)
        result = PROBE.run_probe(library, unrelated, self.manifest)
        self.assertEqual(result["reason"], "invalidInput")
        self.manifest.write_bytes(b" " * ((128 << 10) + 1))
        result = PROBE.run_probe(library, self.cas, self.manifest)
        self.assertEqual(result["reason"], "invalidInput")
        self.assertFalse(self.audit.exists(), "out-of-scope fixture loaded the native library")

    def test_nested_plugin_cas_fixture_is_supported(self):
        nested = self.cas / "plugin"
        nested.mkdir(mode=0o700)
        result = PROBE.run_probe(self.library(), nested, self.manifest)
        self.assertTrue(result["available"], result)
        self.assertTrue(result["objects"][0]["available"], result)
        self.assert_private_report(result)


if __name__ == "__main__":
    unittest.main()
