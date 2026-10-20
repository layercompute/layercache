#!/usr/bin/env python3
"""QA-only semantic counts from one stopped synthetic producer's native CAS.

The ctypes ABI is independently expressed from LLVM's experimental version 0.1
C interface (Apache-2.0 WITH LLVM-exception):
https://github.com/swiftlang/llvm-project/blob/33957bbc59f4eed39bfee4627ef05e47ef31d801/llvm/include/llvm-c/CAS/PluginAPI_types.h
https://github.com/swiftlang/llvm-project/blob/33957bbc59f4eed39bfee4627ef05e47ef31d801/llvm/include/llvm-c/CAS/PluginAPI_functions.h
License: https://github.com/swiftlang/llvm-project/blob/33957bbc59f4eed39bfee4627ef05e47ef31d801/LICENSE.TXT
The upstream-loading pattern is also used by Tuist (MIT):
https://github.com/tuist/tuist/blob/a4c4bfec2eede5d2ac343aa1134460111eefce56/cas-plugin/src/upstream.rs

This does not decode Apple's wire chunks. Counts cannot establish that a wire
encoding has no indirect references. Never use this tool against a shared CAS.
"""
import argparse
import ctypes as c
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys

PROJECT = "github.com/layercache/xcode-standalone-qa"
MAX_MANIFEST_BYTES = 128 << 10
MAX_REPORT_BYTES = 128 << 10
MAX_TARGETS = 256
MAX_OBJECT_BYTES = 64 << 20
MAX_REFERENCES = 100_000
REASONS = {"available", "invalidInput", "unsupportedABI", "missingSymbol", "loadUnavailable",
           "nativeError", "notFound", "bounds", "timeout", "workerFailed"}


class Digest(c.Structure):
    _fields_ = [("data", c.POINTER(c.c_uint8)), ("size", c.c_size_t)]


class Data(c.Structure):
    _fields_ = [("data", c.c_void_p), ("size", c.c_size_t)]


class ObjectID(c.Structure):
    _fields_ = [("opaque", c.c_uint64)]


class Loaded(c.Structure):
    _fields_ = [("opaque", c.c_uint64)]


class Refs(c.Structure):
    _fields_ = [("opaque_b", c.c_uint64), ("opaque_e", c.c_uint64)]


def unavailable(reason, abi=None, worker_stopped=True):
    return {"version": 1, "available": False, "reason": reason, "abi": abi, "objects": [], "workerStopped": worker_stopped}


def integer(value, minimum, maximum):
    return type(value) is int and minimum <= value <= maximum


def read_manifest(path, cas_path):
    # IDs never enter output. The manifest is private, bounded and fixture-only.
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0)
    fd = os.open(path, flags)
    with os.fdopen(fd, "rb") as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_nlink != 1 or info.st_mode & 0o077 or info.st_size > MAX_MANIFEST_BYTES:
            raise ValueError("invalid manifest")
        raw = source.read(MAX_MANIFEST_BYTES + 1)
    if len(raw) > MAX_MANIFEST_BYTES:
        raise ValueError("manifest bounds")
    value = json.loads(raw)
    if type(value) is not dict or set(value) != {"version", "project", "targets"} or value["version"] != 1 or type(value["version"]) is not int or value["project"] != PROJECT:
        raise ValueError("invalid fixture")
    targets = value["targets"]
    if type(targets) is not list or not 1 <= len(targets) <= MAX_TARGETS:
        raise ValueError("target bounds")
    parent = path.parent.absolute()
    relative = cas_path.absolute().relative_to(parent)
    if relative.parts not in (("native-cas",), ("native-cas", "plugin")) or parent.resolve(strict=True) != parent:
        raise ValueError("fixture path")
    current = parent
    for part in (None, *relative.parts):
        if part is not None:
            current = current / part
        info = current.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
            raise ValueError("private fixture directory")
    seen = set()
    for target in targets:
        if type(target) is not dict or set(target) != {"ordinal", "nativeDigestHex", "wireReferenceCount", "wireReferencesComplete", "wireDataBytes"}:
            raise ValueError("target fields")
        ordinal, digest = target["ordinal"], target["nativeDigestHex"]
        if not integer(ordinal, 1, MAX_TARGETS) or ordinal in seen or type(digest) is not str or re.fullmatch(r"[0-9a-f]{130}", digest) is None:
            raise ValueError("target identity")
        seen.add(ordinal)
        if not integer(target["wireReferenceCount"], 0, MAX_REFERENCES) or type(target["wireReferencesComplete"]) is not bool:
            raise ValueError("target refs")
        if target["wireDataBytes"] is not None and not integer(target["wireDataBytes"], 0, MAX_OBJECT_BYTES):
            raise ValueError("target data")
    return targets


class NativeAPI:
    def __init__(self, path):
        self.lib = c.CDLL(str(path))
        self.bind("llcas_get_plugin_version", None, c.POINTER(c.c_uint), c.POINTER(c.c_uint))
        major, minor = c.c_uint(), c.c_uint()
        self.llcas_get_plugin_version(c.byref(major), c.byref(minor))
        self.abi = {"major": major.value, "minor": minor.value}
        if self.abi != {"major": 0, "minor": 1}:
            return
        self.bind("llcas_string_dispose", None, c.c_void_p)
        self.bind("llcas_cas_options_create", c.c_void_p)
        self.bind("llcas_cas_options_dispose", None, c.c_void_p)
        self.bind("llcas_cas_options_set_client_version", None, c.c_void_p, c.c_uint, c.c_uint)
        self.bind("llcas_cas_options_set_ondisk_path", None, c.c_void_p, c.c_char_p)
        self.bind("llcas_cas_create", c.c_void_p, c.c_void_p, c.POINTER(c.c_void_p))
        self.bind("llcas_cas_dispose", None, c.c_void_p)
        self.bind("llcas_cas_get_objectid", c.c_bool, c.c_void_p, Digest, c.POINTER(ObjectID), c.POINTER(c.c_void_p))
        self.bind("llcas_cas_contains_object", c.c_uint, c.c_void_p, ObjectID, c.c_bool, c.POINTER(c.c_void_p))
        self.bind("llcas_cas_load_object", c.c_uint, c.c_void_p, ObjectID, c.POINTER(Loaded), c.POINTER(c.c_void_p))
        self.bind("llcas_loaded_object_get_data", Data, c.c_void_p, Loaded)
        self.bind("llcas_loaded_object_get_refs", Refs, c.c_void_p, Loaded)
        self.bind("llcas_object_refs_get_count", c.c_size_t, c.c_void_p, Refs)

    def bind(self, name, result, *arguments):
        function = getattr(self.lib, name)
        function.restype, function.argtypes = result, list(arguments)
        setattr(self, name, function)

    def clear_error(self, error):
        failed = bool(error.value)
        if failed:
            # Do not dereference, decode or print SDK error strings.
            self.llcas_string_dispose(error)
            error.value = None
        return failed

    def inspect(self, cas, target):
        result = {"ordinal": target["ordinal"], "available": False, "reason": "nativeError",
                  "dataBytes": None, "referenceCount": None,
                  "wireReferenceCountMatches": None, "wireDataBytesMatches": None}
        raw = bytes.fromhex(target["nativeDigestHex"])
        storage = (c.c_uint8 * len(raw)).from_buffer_copy(raw)
        error, object_id = c.c_void_p(), ObjectID()
        failed = self.llcas_cas_get_objectid(cas, Digest(storage, len(raw)), c.byref(object_id), c.byref(error))
        if self.clear_error(error) or failed:
            return result
        # Restrict lookup to the existing local fixture; no remote option is set.
        found = self.llcas_cas_contains_object(cas, object_id, False, c.byref(error))
        if self.clear_error(error) or found not in (0, 1):
            return result
        if found == 1:
            result["reason"] = "notFound"
            return result
        loaded = Loaded()
        found = self.llcas_cas_load_object(cas, object_id, c.byref(loaded), c.byref(error))
        if self.clear_error(error) or found not in (0, 1):
            return result
        if found == 1:
            result["reason"] = "notFound"
            return result
        data = self.llcas_loaded_object_get_data(cas, loaded)
        refs = self.llcas_loaded_object_get_refs(cas, loaded)
        count = self.llcas_object_refs_get_count(cas, refs)
        if data.size > MAX_OBJECT_BYTES or count > MAX_REFERENCES or (data.size and not data.data):
            result["reason"] = "bounds"
            return result
        result.update(available=True, reason="available", dataBytes=data.size, referenceCount=count)
        if target["wireReferencesComplete"]:
            result["wireReferenceCountMatches"] = count == target["wireReferenceCount"]
        if target["wireDataBytes"] is not None:
            result["wireDataBytesMatches"] = data.size == target["wireDataBytes"]
        return result


def worker(plugin, cas_path, targets):
    api = NativeAPI(plugin)
    if api.abi != {"major": 0, "minor": 1}:
        return unavailable("unsupportedABI", api.abi)
    options, cas = api.llcas_cas_options_create(), None
    if not options:
        return unavailable("nativeError", api.abi)
    try:
        api.llcas_cas_options_set_client_version(options, 0, 1)
        api.llcas_cas_options_set_ondisk_path(options, os.fsencode(cas_path))
        error = c.c_void_p()
        cas = api.llcas_cas_create(options, c.byref(error))
        if api.clear_error(error) or not cas:
            return unavailable("nativeError", api.abi)
        objects, total_bytes = [], 0
        for target in targets:
            item = api.inspect(cas, target)
            if item["reason"] == "bounds":
                return unavailable("bounds", api.abi)
            total_bytes += item["dataBytes"] or 0
            if total_bytes > MAX_OBJECT_BYTES:
                return unavailable("bounds", api.abi)
            objects.append(item)
        return {"version": 1, "available": True, "reason": "available", "abi": api.abi, "objects": objects, "workerStopped": False}
    finally:
        if cas:
            api.llcas_cas_dispose(cas)
        api.llcas_cas_options_dispose(options)


def valid_report(result):
    # Treat even child output as untrusted. Never forward native diagnostics.
    if type(result) is not dict or set(result) != {"version", "available", "reason", "abi", "objects", "workerStopped"} or type(result["workerStopped"]) is not bool or result["version"] != 1 or type(result["available"]) is not bool or result["reason"] not in REASONS:
        return False
    if result["abi"] is not None and (type(result["abi"]) is not dict or set(result["abi"]) != {"major", "minor"} or not all(integer(n, 0, (1 << 32) - 1) for n in result["abi"].values())):
        return False
    if type(result["objects"]) is not list or len(result["objects"]) > MAX_TARGETS:
        return False
    for item in result["objects"]:
        if type(item) is not dict or set(item) != {"ordinal", "available", "reason", "dataBytes", "referenceCount", "wireReferenceCountMatches", "wireDataBytesMatches"}:
            return False
        if not integer(item["ordinal"], 1, MAX_TARGETS) or type(item["available"]) is not bool or item["reason"] not in REASONS:
            return False
        for key, maximum in (("dataBytes", MAX_OBJECT_BYTES), ("referenceCount", MAX_REFERENCES)):
            if item[key] is not None and not integer(item[key], 0, maximum):
                return False
        for key in ("wireReferenceCountMatches", "wireDataBytesMatches"):
            if item[key] is not None and type(item[key]) is not bool:
                return False
    return True


def run_probe(plugin, cas_path, manifest_path, timeout=8):
    plugin, cas_path, manifest_path = Path(plugin), Path(cas_path), Path(manifest_path)
    if not plugin.is_absolute() or type(timeout) not in (int, float) or not 0 < timeout <= 10:
        return unavailable("invalidInput")
    try:
        process = subprocess.Popen([sys.executable, str(Path(__file__).resolve()), "--worker", "--plugin", str(plugin),
                                    "--cas-path", str(cas_path), "--manifest", str(manifest_path)],
                                   stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    except OSError:
        return unavailable("workerFailed")
    try:
        output, _ = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        # Kill only our native worker. Kernel teardown must not turn a
        # diagnostic deadline into an unbounded wait in subprocess.run().
        try:
            process.kill()
        except OSError:
            pass
        stopped = False
        try:
            process.communicate(timeout=0.1)
            stopped = True
        except (subprocess.TimeoutExpired, OSError):
            if process.stdout is not None:
                process.stdout.close()
        return unavailable("timeout", worker_stopped=stopped)
    if process.returncode or len(output) > MAX_REPORT_BYTES:
        return unavailable("workerFailed")
    try:
        value = json.loads(output)
        if not valid_report(value):
            return unavailable("workerFailed")
        value["workerStopped"] = True  # communicate confirmed this worker exited.
        return value
    except (ValueError, TypeError):
        return unavailable("workerFailed")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--plugin", required=True, type=Path)
    parser.add_argument("--cas-path", required=True, type=Path)
    parser.add_argument("--manifest", required=True, type=Path)
    parser.add_argument("--worker", action="store_true", help=argparse.SUPPRESS)
    args = parser.parse_args()
    if not args.worker:
        print(json.dumps(run_probe(args.plugin, args.cas_path, args.manifest), separators=(",", ":")))
        return
    # Native errors/crashes cannot expose paths/IDs or hold the outer caller.
    output = os.dup(1)
    with open(os.devnull, "wb") as sink:
        os.dup2(sink.fileno(), 1)
        os.dup2(sink.fileno(), 2)
    try:
        import resource
        resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
        if sys.platform.startswith("linux"):
            # core(5): piped dump handlers ignore RLIMIT_CORE. Disable the
            # process dumpability too, before any native plugin is loaded.
            # https://man7.org/linux/man-pages/man2/PR_SET_DUMPABLE.2const.html
            prctl = c.CDLL(None).prctl
            prctl.restype = c.c_int
            prctl.argtypes = [c.c_int, c.c_ulong, c.c_ulong, c.c_ulong, c.c_ulong]
            if prctl(4, 0, 0, 0, 0) != 0:  # Linux PR_SET_DUMPABLE.
                raise OSError("dump prevention unavailable")
        try:
            targets = read_manifest(args.manifest, args.cas_path)
            if not args.plugin.is_absolute() or not args.plugin.is_file():
                raise ValueError("plugin path")
        except (OSError, ValueError, TypeError, KeyError):
            targets = None
        value = worker(args.plugin, args.cas_path, targets) if targets is not None else unavailable("invalidInput")
    except AttributeError:
        value = unavailable("missingSymbol")
    except OSError:
        value = unavailable("loadUnavailable")
    except Exception:
        value = unavailable("nativeError")
    value["workerStopped"] = False  # Only the waiting parent can confirm exit.
    encoded = json.dumps(value, separators=(",", ":")).encode()
    if len(encoded) > MAX_REPORT_BYTES:
        encoded = json.dumps(unavailable("bounds", worker_stopped=False), separators=(",", ":")).encode()
    try:
        os.write(output, encoded + b"\n")
    finally:
        os.close(output)


if __name__ == "__main__":
    main()
