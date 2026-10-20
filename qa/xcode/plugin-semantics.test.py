#!/usr/bin/env python3
"""Exercise the QA probe against real, small C ABI fixtures, without an SDK."""

import copy
import hashlib
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
CHILD_BYTES = (PRIVATE_NATIVE_TEXT.encode(), b"safe child two", b"third child")
OMITTED = object()

# The declarations below follow the pinned LLVM 0.1 headers named by the probe.
# Deliberately large opaque values catch truncated ctypes return/argument types.
# Root Data.data is unreadable. Only explicit child comparisons may read bytes.
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
#define HANDLE_MASK UINT64_C(65535)
#define CHILD_BASE UINT64_C(32768)

static unsigned client_major = 99, client_minor = 99;
static char on_disk_path[4096];
_Alignas(8) static const unsigned char child_zero[] = PRIVATE_TEXT;
_Alignas(8) static const unsigned char child_one[] = "safe child two";
_Alignas(8) static const unsigned char child_one_wrong[] = "fake child two";
_Alignas(8) static const unsigned char child_two[] = "third child";
_Alignas(8) static const unsigned char large_child[65536] = {0};

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
    if (cas != CAS || globally || (id.opaque & ~HANDLE_MASK) != OBJECT_BASE || SCENARIO == 9) {
        fail(error); return 2;
    }
    if ((id.opaque & HANDLE_MASK) == 253) return 1;
    if (SCENARIO == 23 && (id.opaque & HANDLE_MASK) == CHILD_BASE + 1) return 1;
    if (SCENARIO == 32 && (id.opaque & HANDLE_MASK) == CHILD_BASE + 1) {
        fail(error); return 2;
    }
    return 0;
}

unsigned llcas_cas_load_object(void *cas, ObjectID id, Loaded *loaded, char **error) {
    audit('Q');
    if (cas != CAS || (id.opaque & ~HANDLE_MASK) != OBJECT_BASE || SCENARIO == 10) {
        fail(error); return 2;
    }
    if (SCENARIO == 33 && (id.opaque & HANDLE_MASK) == CHILD_BASE + 1) return 1;
    loaded->opaque = LOADED_BASE | (id.opaque & HANDLE_MASK);
    return 0;
}

Data llcas_loaded_object_get_data(void *cas, Loaded loaded) {
    audit('B');
    if (cas != CAS || (loaded.opaque & ~HANDLE_MASK) != LOADED_BASE)
        return (Data){NULL, 1};
    size_t index = loaded.opaque & HANDLE_MASK;
    if (index >= CHILD_BASE) {
        index -= CHILD_BASE;
        if (SCENARIO == 27) return (Data){large_child, sizeof(large_child)};
        if (SCENARIO == 28) return (Data){NULL, 0};
        if (SCENARIO == 24 && index == 1) return (Data){NULL, 7};
        if (SCENARIO == 29 && index == 1) return (Data){(const void *)(uintptr_t)1, (64 * 1024 * 1024) + 1};
        if (SCENARIO == 30 && index == 1) return (Data){(const void *)(uintptr_t)1, 65537};
        if (SCENARIO == 35 && index == 1) return (Data){NULL, 0};
        if (index == 0) return (Data){child_zero, sizeof(child_zero) - 1};
        if (index == 1) return (Data){SCENARIO == 21 ? child_one_wrong : child_one, sizeof(child_one) - 1};
        if (index == 2) return (Data){child_two, sizeof(child_two) - 1};
        return (Data){NULL, 1};
    }
    if (SCENARIO >= 20) return (Data){(const void *)(uintptr_t)1, 17};
    size_t bytes = index == 1 ? 3145729 : (index == 2 ? 33 : 17);
    if (SCENARIO == 6) bytes = (64 * 1024 * 1024) + 1;
    if (SCENARIO == 11) bytes = 40 * 1024 * 1024;
    return (Data){SCENARIO == 8 ? NULL : (const void *)(uintptr_t)1, bytes};
}

Refs llcas_loaded_object_get_refs(void *cas, Loaded loaded) {
    audit('R');
    if (cas != CAS || (loaded.opaque & ~HANDLE_MASK) != LOADED_BASE)
        return (Refs){0, 0};
    return (Refs){REFS_BEGIN | (loaded.opaque & HANDLE_MASK), REFS_END | (loaded.opaque & HANDLE_MASK)};
}

#if SCENARIO != 2
size_t llcas_object_refs_get_count(void *cas, Refs refs) {
    audit('N');
    if (cas != CAS || (refs.opaque_b & ~HANDLE_MASK) != REFS_BEGIN ||
        (refs.opaque_e & ~HANDLE_MASK) != REFS_END ||
        (refs.opaque_b & HANDLE_MASK) != (refs.opaque_e & HANDLE_MASK)) return SIZE_MAX;
    if (SCENARIO == 7) return 100001;
    size_t index = refs.opaque_b & HANDLE_MASK;
    if (index >= CHILD_BASE) {
        if (SCENARIO == 34 && index == CHILD_BASE + 1) return 100001;
        return SCENARIO == 22 && index == CHILD_BASE + 1 ? 1 : 0;
    }
    if (SCENARIO == 27 || SCENARIO == 28) return index == 4 ? 0 : 256;
    if (SCENARIO >= 20) return 3;
    return index == 1 ? 3 : (index == 2 ? 1 : 0);
}
#endif

#if SCENARIO != 36
ObjectID llcas_object_refs_get_id(void *cas, Refs refs, size_t index) {
    audit('H');
    if (cas != CAS || (refs.opaque_b & ~HANDLE_MASK) != REFS_BEGIN ||
        (refs.opaque_e & ~HANDLE_MASK) != REFS_END ||
        (refs.opaque_b & HANDLE_MASK) != (refs.opaque_e & HANDLE_MASK) || index >= 256) {
        audit('!'); return (ObjectID){0};
    }
    if (SCENARIO == 26) index = index == 0 ? 1 : (index == 1 ? 0 : index);
    if (SCENARIO == 31 && index == 1) index = 0;
    return (ObjectID){OBJECT_BASE | (CHILD_BASE + index)};
}
#endif
"""


def chunks_for(payloads=CHILD_BYTES):
    return [{"sha256": hashlib.sha256(value).hexdigest(), "dataBytes": len(value)} for value in payloads]


def target(ordinal=1, index=0, references=0, complete=True, data_bytes=None, inline_chunks=OMITTED, reference_order=OMITTED):
    value = {
        "ordinal": ordinal,
        "nativeDigestHex": "00" * 64 + f"{index:02x}",
        "wireReferenceCount": references,
        "wireReferencesComplete": complete,
        "wireDataBytes": data_bytes,
    }
    if inline_chunks is not OMITTED:
        value["wireInlineChunks"] = inline_chunks
    if reference_order is not OMITTED:
        value["wireReferenceOrder"] = reference_order
    return value


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
            self.assertIs(item["comparisonComplete"], False)
            self.assertEqual((item["referenceObjectsChecked"], item["referenceDataBytes"]), (0, 0))
            for key in ("inlineReferenceCountMatches", "inlineReferencesMatch", "inlineReferenceMultisetMatches", "referencedObjectsAreLeaves",
                        "indexedOneBasedReferencesMatch", "indexedZeroBasedReferencesMatch"):
                self.assertIsNone(item[key])
        self.assertEqual(self.calls().count("G"), 3)
        self.assertEqual(self.calls().count("Q"), 3)
        self.assertEqual(self.calls().count("X"), 1)
        self.assertEqual(self.calls().count("D"), 1)
        self.assertNotIn("!", self.calls(), "an opaque ABI handle was truncated or replaced")
        self.assertNotIn("H", self.calls(), "legacy manifest dereferenced semantic children")
        self.assert_private_report(result)

    def test_child_bytes_match_without_root_payload_or_private_hash_disclosure(self):
        chunks = chunks_for()
        self.write_manifest({"version": 1, "project": PROJECT, "targets": [target(inline_chunks=chunks)]})
        result = PROBE.run_probe(self.library(scenario=20), self.cas, self.manifest)
        self.assertTrue(result["available"], result)
        item = result["objects"][0]
        self.assertEqual((item["available"], item["reason"], item["dataBytes"], item["referenceCount"]),
                         (True, "available", 17, 3))
        self.assertEqual((item["comparisonComplete"], item["referenceObjectsChecked"], item["referenceDataBytes"]),
                         (True, 3, sum(map(len, CHILD_BYTES))))
        for key in ("inlineReferenceCountMatches", "inlineReferencesMatch", "inlineReferenceMultisetMatches", "referencedObjectsAreLeaves"):
            self.assertIs(item[key], True, key)
        self.assertIsNone(item["indexedOneBasedReferencesMatch"])
        self.assertIsNone(item["indexedZeroBasedReferencesMatch"])
        self.assertIs(item["wireReferenceCountMatches"], False, "envelope references and semantic references were conflated")
        self.assertEqual((self.calls().count("G"), self.calls().count("H"), self.calls().count("Q"), self.calls().count("B")),
                         (1, 3, 4, 4))
        self.assertNotIn("!", self.calls(), "a by-value ObjectID or Refs handle was truncated")
        for chunk in chunks:
            self.assertNotIn(chunk["sha256"], json.dumps(result))
        self.assert_private_report(result)

    def test_indexed_reference_orders_use_declared_base_and_direction_without_extra_reads(self):
        # indices[semantic_index] selects the inline chunk. The three-cycle
        # distinguishes that convention from its inverse, unlike a swap.
        rotated = (CHILD_BYTES[2], CHILD_BYTES[0], CHILD_BYTES[1])
        for scenario, payloads, base, indices, matched in (
            (26, CHILD_BYTES, 0, [1, 0, 2], True),
            (26, CHILD_BYTES, 1, [2, 1, 3], True),
            (20, rotated, 0, [1, 2, 0], True),
            (20, rotated, 1, [2, 3, 1], True),
            (20, rotated, 0, [2, 0, 1], False),
            (20, rotated, 1, [3, 1, 2], False),
            (21, CHILD_BYTES, 0, [0, 1, 2], False),
            (21, CHILD_BYTES, 1, [1, 2, 3], False),
        ):
            with self.subTest(scenario=scenario, base=base, indices=indices):
                self.audit.unlink(missing_ok=True)
                self.write_manifest({"version": 1, "project": PROJECT, "targets": [
                    target(inline_chunks=chunks_for(payloads), reference_order={"base": base, "indices": indices}),
                ]})
                result = PROBE.run_probe(self.library(scenario=scenario), self.cas, self.manifest)
                self.assertTrue(result["available"], result)
                item = result["objects"][0]
                self.assertEqual((item["available"], item["comparisonComplete"], item["referenceObjectsChecked"]), (True, True, 3))
                self.assertIs(item["inlineReferencesMatch"], False)
                self.assertIs(item["inlineReferenceMultisetMatches"], scenario != 21)
                selected = "indexedOneBasedReferencesMatch" if base == 1 else "indexedZeroBasedReferencesMatch"
                other = "indexedZeroBasedReferencesMatch" if base == 1 else "indexedOneBasedReferencesMatch"
                self.assertIs(item[selected], matched)
                self.assertIsNone(item[other], "the undeclared base was guessed")
                self.assertEqual((self.calls().count("G"), self.calls().count("H"), self.calls().count("Q"),
                                  self.calls().count("B"), self.calls().count("R"), self.calls().count("N")),
                                 (1, 3, 4, 4, 4, 4), "indexed comparison added native reads")
                serialized = json.dumps(result)
                self.assertNotIn("wireReferenceOrder", serialized)
                self.assertNotIn('"indices"', serialized)
                for chunk in chunks_for(payloads):
                    self.assertNotIn(chunk["sha256"], serialized)
                self.assert_private_report(result)

    def test_absent_or_null_reference_order_does_not_infer_a_base(self):
        for order in (OMITTED, None):
            with self.subTest(order="omitted" if order is OMITTED else "null"):
                self.audit.unlink(missing_ok=True)
                self.write_manifest({"version": 1, "project": PROJECT, "targets": [
                    target(inline_chunks=chunks_for(), reference_order=order),
                ]})
                result = PROBE.run_probe(self.library(scenario=20), self.cas, self.manifest)
                item = result["objects"][0]
                self.assertIs(item["inlineReferencesMatch"], True)
                self.assertIs(item["indexedOneBasedReferencesMatch"], None)
                self.assertIs(item["indexedZeroBasedReferencesMatch"], None)
                self.assertEqual((self.calls().count("H"), self.calls().count("Q"), self.calls().count("B")), (3, 4, 4))
                self.assert_private_report(result)

    def test_positional_multiset_and_nonleaf_outcomes_remain_distinct(self):
        for scenario, positional, multiset, leaves in (
            (21, False, False, True),  # Same-size changed bytes.
            (26, False, True, True),   # The same leaves in a different order.
            (31, False, False, True),  # Duplicate replaces a distinct leaf.
            (22, True, True, False),   # Equal bytes do not establish a leaf.
        ):
            with self.subTest(scenario=scenario):
                self.audit.unlink(missing_ok=True)
                self.write_manifest({"version": 1, "project": PROJECT, "targets": [target(inline_chunks=chunks_for())]})
                result = PROBE.run_probe(self.library(scenario=scenario), self.cas, self.manifest)
                item = result["objects"][0]
                self.assertEqual((item["available"], item["comparisonComplete"], item["referenceObjectsChecked"]), (True, True, 3))
                self.assertEqual((item["inlineReferenceCountMatches"], item["inlineReferencesMatch"],
                                  item["inlineReferenceMultisetMatches"], item["referencedObjectsAreLeaves"]),
                                 (True, positional, multiset, leaves))
                self.assertEqual((self.calls().count("H"), self.calls().count("Q")), (3, 4))
                self.assert_private_report(result)

    def test_missing_or_error_child_never_claims_complete_comparison(self):
        for scenario, reason, loads, errors in ((23, "notFound", 2, 0), (32, "nativeError", 2, 1), (33, "notFound", 3, 0)):
            with self.subTest(scenario=scenario):
                self.audit.unlink(missing_ok=True)
                self.write_manifest({"version": 1, "project": PROJECT, "targets": [
                    target(inline_chunks=chunks_for(), reference_order={"base": 0, "indices": [0, 1, 2]}),
                ]})
                result = PROBE.run_probe(self.library(scenario=scenario), self.cas, self.manifest)
                item = result["objects"][0]
                self.assertTrue(result["available"], result)
                self.assertEqual((item["available"], item["reason"], item["comparisonComplete"]), (False, reason, False))
                self.assertEqual((item["referenceObjectsChecked"], item["referenceDataBytes"]), (1, len(CHILD_BYTES[0])))
                self.assertIs(item["inlineReferenceCountMatches"], True)
                for key in ("inlineReferencesMatch", "inlineReferenceMultisetMatches", "referencedObjectsAreLeaves",
                            "indexedOneBasedReferencesMatch", "indexedZeroBasedReferencesMatch"):
                    self.assertIsNone(item[key])
                self.assertEqual((self.calls().count("H"), self.calls().count("Q"), self.calls().count("E")), (2, loads, errors))
                self.assertEqual((self.calls().count("X"), self.calls().count("D")), (1, 1))
                self.assert_private_report(result)

    def test_count_mismatch_and_absent_mapping_never_load_children(self):
        for mapping, count_match, positional in ((None, None, None), ([], False, False), (chunks_for()[:2], False, False)):
            with self.subTest(mapping=mapping):
                self.audit.unlink(missing_ok=True)
                order = {"base": 0, "indices": list(range(len(mapping)))} if mapping else OMITTED
                self.write_manifest({"version": 1, "project": PROJECT, "targets": [
                    target(inline_chunks=mapping, reference_order=order),
                ]})
                result = PROBE.run_probe(self.library(scenario=20), self.cas, self.manifest)
                item = result["objects"][0]
                self.assertEqual((item["available"], item["comparisonComplete"], item["referenceObjectsChecked"], item["referenceDataBytes"]),
                                 (True, False, 0, 0))
                self.assertEqual((item["inlineReferenceCountMatches"], item["inlineReferencesMatch"]), (count_match, positional))
                self.assertIsNone(item["inlineReferenceMultisetMatches"])
                self.assertIsNone(item["referencedObjectsAreLeaves"])
                self.assertIsNone(item["indexedOneBasedReferencesMatch"])
                self.assertIsNone(item["indexedZeroBasedReferencesMatch"])
                self.assertNotIn("H", self.calls())
                self.assertEqual(self.calls().count("Q"), 1)
                self.assert_private_report(result)

    def test_null_and_oversized_child_shapes_abort_before_later_targets(self):
        self.write_manifest({"version": 1, "project": PROJECT, "targets": [
            target(inline_chunks=chunks_for()), target(2, 1),
        ]})
        for scenario in (24, 29, 34):
            with self.subTest(scenario=scenario):
                self.audit.unlink(missing_ok=True)
                result = PROBE.run_probe(self.library(scenario=scenario), self.cas, self.manifest)
                self.assertEqual((result["available"], result["reason"], result["objects"]), (False, "bounds", []))
                self.assertEqual((self.calls().count("G"), self.calls().count("H"), self.calls().count("Q"), self.calls().count("B")),
                                 (1, 2, 3, 3))
                self.assertEqual((self.calls().count("X"), self.calls().count("D")), (1, 1))
                self.assert_private_report(result)

    def test_child_larger_than_copy_bound_is_not_dereferenced(self):
        self.write_manifest({"version": 1, "project": PROJECT, "targets": [
            target(inline_chunks=chunks_for()), target(2, 1),
        ]})
        result = PROBE.run_probe(self.library(scenario=30), self.cas, self.manifest)
        self.assertEqual((result["available"], result["reason"], result["objects"]), (False, "bounds", []))
        self.assertEqual((self.calls().count("G"), self.calls().count("H"), self.calls().count("Q"), self.calls().count("B")),
                         (1, 2, 3, 3))
        self.assertEqual((self.calls().count("X"), self.calls().count("D")), (1, 1))
        self.assert_private_report(result)

    def test_zero_sized_null_child_is_a_valid_empty_leaf(self):
        payloads = (CHILD_BYTES[0], b"", CHILD_BYTES[2])
        self.write_manifest({"version": 1, "project": PROJECT, "targets": [target(inline_chunks=chunks_for(payloads))]})
        result = PROBE.run_probe(self.library(scenario=35), self.cas, self.manifest)
        item = result["objects"][0]
        self.assertEqual((item["available"], item["comparisonComplete"], item["referenceObjectsChecked"]), (True, True, 3))
        self.assertEqual(item["referenceDataBytes"], sum(map(len, payloads)))
        for key in ("inlineReferencesMatch", "inlineReferenceMultisetMatches", "referencedObjectsAreLeaves"):
            self.assertIs(item[key], True)
        self.assert_private_report(result)

    def test_child_budget_boundary_and_shared_root_byte_budget(self):
        for scenario, payload, available in ((28, b"", True), (27, bytes(65536), False)):
            with self.subTest(scenario=scenario):
                self.audit.unlink(missing_ok=True)
                chunk = chunks_for((payload,))[0]
                targets = [target(i + 1, i, inline_chunks=[chunk] * 256) for i in range(4)]
                targets.append(target(5, 4, inline_chunks=[]))
                self.write_manifest({"version": 1, "project": PROJECT, "targets": targets})
                result = PROBE.run_probe(self.library(scenario=scenario), self.cas, self.manifest)
                self.assertEqual((result["available"], result["reason"]), (available, "available" if available else "bounds"))
                self.assertEqual(self.calls().count("H"), 1024)
                self.assertNotIn("!", self.calls())
                if available:
                    self.assertEqual(len(result["objects"]), 5)
                    self.assertEqual(sum(item["referenceObjectsChecked"] for item in result["objects"]), 1024)
                    self.assertTrue(all(item["comparisonComplete"] and item["inlineReferencesMatch"] for item in result["objects"]))
                    self.assertEqual((self.calls().count("G"), self.calls().count("Q")), (5, 1029))
                else:
                    self.assertEqual(result["objects"], [])
                    self.assertEqual((self.calls().count("G"), self.calls().count("Q")), (4, 1028))
                self.assert_private_report(result)

    def test_native_stdout_and_stderr_do_not_escape_the_calling_process(self):
        chunks = chunks_for()
        self.write_manifest({"version": 1, "project": PROJECT, "targets": [target(inline_chunks=chunks)]})
        library = self.library(scenario=20)
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
        for chunk in chunks:
            self.assertNotIn(chunk["sha256"], completed.stdout)
        result = json.loads(completed.stdout)
        self.assertTrue(result["available"], result)
        self.assertIs(result["objects"][0]["inlineReferencesMatch"], True)
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
            (36, 0, 1, "missingSymbol", None),
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

    def test_inline_manifest_bounds_and_fields_reject_before_native_load(self):
        library = self.library(scenario=20)
        chunk = chunks_for((b"x",))[0]
        invalid_mappings = {
            "wrong list type": {},
            "too many chunks in one target": [chunk] * 257,
            "wrong chunk type": [None],
            "missing hash": [{"dataBytes": 1}],
            "extra payload field": [dict(chunk, data="private")],
            "short hash": [dict(chunk, sha256="00" * 31)],
            "uppercase hash": [dict(chunk, sha256="A" * 64)],
            "nonhex hash": [dict(chunk, sha256="z" * 64)],
            "nonstring hash": [dict(chunk, sha256=1)],
            "boolean length": [dict(chunk, dataBytes=True)],
            "negative length": [dict(chunk, dataBytes=-1)],
            "fractional length": [dict(chunk, dataBytes=1.0)],
            "oversized chunk": [dict(chunk, dataBytes=65537)],
        }
        cases = {name: [target(inline_chunks=mapping)] for name, mapping in invalid_mappings.items()}
        cases["aggregate chunk count"] = [target(i + 1, i, inline_chunks=[chunk] * 256) for i in range(4)]
        cases["aggregate chunk count"].append(target(5, 4, inline_chunks=[chunk]))
        for name, targets in cases.items():
            with self.subTest(name=name):
                self.write_manifest({"version": 1, "project": PROJECT, "targets": targets})
                result = PROBE.run_probe(library, self.cas, self.manifest)
                self.assertEqual((result["available"], result["reason"]), (False, "invalidInput"))
                self.assertFalse(self.audit.exists(), "invalid inline input loaded native code")
                self.assert_private_report(result)

    def test_invalid_reference_orders_reject_before_native_load(self):
        library = self.library(scenario=20)
        invalid_orders = {
            "wrong type": [],
            "missing fields": {},
            "missing base": {"indices": [0, 1, 2]},
            "missing indices": {"base": 0},
            "extra field": {"base": 0, "indices": [0, 1, 2], "raw": "private"},
            "boolean base": {"base": False, "indices": [0, 1, 2]},
            "fractional base": {"base": 0.0, "indices": [0, 1, 2]},
            "negative base": {"base": -1, "indices": [0, 1, 2]},
            "unknown base": {"base": 2, "indices": [2, 3, 4]},
            "null indices": {"base": 0, "indices": None},
            "wrong indices type": {"base": 0, "indices": "012"},
            "empty permutation": {"base": 0, "indices": []},
            "short permutation": {"base": 0, "indices": [0, 1]},
            "long permutation": {"base": 0, "indices": [0, 1, 2, 3]},
            "duplicate zero based": {"base": 0, "indices": [0, 0, 2]},
            "duplicate one based": {"base": 1, "indices": [1, 2, 2]},
            "negative index": {"base": 0, "indices": [-1, 1, 2]},
            "out of range zero based": {"base": 0, "indices": [1, 2, 3]},
            "out of range one based": {"base": 1, "indices": [0, 1, 2]},
            "boolean index": {"base": 0, "indices": [False, 1, 2]},
            "fractional index": {"base": 0, "indices": [0.0, 1, 2]},
        }
        cases = {name: target(inline_chunks=chunks_for(), reference_order=order) for name, order in invalid_orders.items()}
        order = {"base": 0, "indices": [0, 1, 2]}
        cases["order without chunks"] = target(reference_order=order)
        cases["order with null chunks"] = target(inline_chunks=None, reference_order=order)
        cases["empty order with empty chunks"] = target(inline_chunks=[], reference_order={"base": 0, "indices": []})
        for name, item in cases.items():
            with self.subTest(name=name):
                self.write_manifest({"version": 1, "project": PROJECT, "targets": [item]})
                result = PROBE.run_probe(library, self.cas, self.manifest)
                self.assertEqual((result["available"], result["reason"]), (False, "invalidInput"))
                self.assertFalse(self.audit.exists(), "invalid reference order loaded native code")
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
