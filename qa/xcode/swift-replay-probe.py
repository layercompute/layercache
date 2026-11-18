#!/usr/bin/env python3
"""Qualify (or clearly report the absence of) external Swift compilation replay
on Xcode 27. This never claims C-family/PCH replay; `standalone-snapshot-probe.py`
already covers that. A single real application run reported every SwiftCompile
and SwiftEmitModule task hitting the adapter's closed store on Xcode 27.2, with
every miss confined to C-family actions. Three things were not controlled in
that run: whether Apple's own local CAS survived and supplied some of the hits,
whether the replayed products are identical to a cold build, and whether hits
survive a different checkout path. This probe isolates and answers each one.
"""
import argparse
import hashlib
import json
import os
import re
import shutil
import signal
import stat
import subprocess
import sys
import time
from pathlib import Path

sys.dont_write_bytecode = True

PROJECT = "github.com/layercache/xcode-swift-replay-qa"
BUDGET = 512 << 20
LIB_VALUE = 42
FEATURE_ONE_VALUE = 1
FEATURE_TWO_VALUE = 2
CHANGED_FEATURE_ONE_VALUE = 99

GENERATOR = r'''
require 'xcodeproj'
root = ARGV.fetch(0)
project = Xcodeproj::Project.new("#{root}/Probe.xcodeproj")

lib = project.new_target(:static_library, 'SwiftReplayLib', :osx, '15.0')
lib.source_build_phase.add_file_reference(project.main_group.new_file('Lib.swift'))

control = project.new_target(:static_library, 'SwiftReplayControl', :osx, '15.0')
control.source_build_phase.add_file_reference(project.main_group.new_file('control.m'))

exe = project.new_target(:command_line_tool, 'SwiftReplayApp', :osx, '15.0')
%w[main.swift Feature1.swift Feature2.swift].each do |name|
  exe.source_build_phase.add_file_reference(project.main_group.new_file(name))
end
exe.add_dependency(lib)
exe.frameworks_build_phase.add_file_reference(lib.product_reference)

[lib, control, exe].each do |target|
  target.build_configurations.each do |config|
    config.build_settings['CODE_SIGNING_ALLOWED'] = 'NO'
    config.build_settings['SWIFT_VERSION'] = '5.0'
    config.build_settings['CLANG_ENABLE_MODULES'] = 'YES'
    config.build_settings['ONLY_ACTIVE_ARCH'] = 'YES'
    # Batch mode can merge several small files into one SwiftCompile task,
    # which would hide the single-file miss the changed-source phase requires.
    config.build_settings['SWIFT_COMPILATION_MODE'] = 'singlefile'
    config.build_settings['OTHER_SWIFT_FLAGS'] = '$(inherited) -disable-batch-mode'
  end
end
exe.build_configurations.each do |config|
  # Belt and suspenders beside the implicit shared per-configuration build
  # directory: make the sibling library's .swiftmodule and archive explicit.
  config.build_settings['SWIFT_INCLUDE_PATHS'] = '$(inherited) $(BUILT_PRODUCTS_DIR)'
  config.build_settings['LIBRARY_SEARCH_PATHS'] = '$(inherited) $(BUILT_PRODUCTS_DIR)'
end
project.save

scheme = Xcodeproj::XCScheme.new
scheme.add_build_target(lib)
scheme.add_build_target(control)
scheme.add_build_target(exe)
scheme.save_as(project.path, 'Probe', true)
'''


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n")
    path.chmod(0o600)


def task_diagnostics(log, task_names):
    """Associate cache hit/miss remarks with the task that produced them.

    Mirrors standalone-snapshot-probe.py's clang_diagnostics: a task header line
    opens a block, any other capitalized header line closes it, and only
    `note`/`remark` cache diagnostics inside an open block of one of
    `task_names` are counted. Each SwiftCompile task also records the `.swift`
    paths named on its header line, since non-batched compilation produces one
    task per source file.
    """
    result = {name: {"hit": 0, "miss": 0} for name in task_names}
    by_file = {}
    current = None
    for line in log.splitlines():
        header = re.match(r"^([A-Z][A-Za-z0-9]+)\b.*$", line)
        if header:
            name = header.group(1)
            current = name if name in task_names else None
            if current == "SwiftCompile":
                files = re.findall(r"(\S+\.swift)\b", line)
                # The header line can repeat one file (escaped "Compiling\ X.swift"
                # alongside the real path); dedupe so by-file counts stay exact.
                current = ("SwiftCompile", tuple(dict.fromkeys(Path(f).name for f in files)))
            continue
        diagnostic = re.match(r"^(?:note|remark):\s+(?:replay(?:ed)? )?cache (hit|miss)\b", line, re.I)
        if current and diagnostic:
            verdict = diagnostic.group(1).lower()
            if isinstance(current, tuple):
                result["SwiftCompile"][verdict] += 1
                for name in current[1]:
                    entry = by_file.setdefault(name, {"hit": 0, "miss": 0})
                    entry[verdict] += 1
            else:
                result[current][verdict] += 1
    return result, by_file


def discover_native_cas(cas_root, derived):
    """Candidate native CAS locations, in the same shape standalone-snapshot-probe.py uses.

    `cas_root` is the explicit COMPILATION_CACHE_CAS_PATH directory; `derived`
    is the DerivedData path Xcode would fall back to if that setting were ever
    ignored by a component of the toolchain.
    """
    candidates = {
        "configuredRoot": cas_root,
        "configuredPlugin": cas_root / "plugin",
        "configuredBuiltin": cas_root / "builtin",
        "derivedDataBuiltin": derived / "CompilationCache.noindex",
    }
    report = {}
    for name, path in candidates.items():
        try:
            info = path.lstat()
            report[name] = {"path_existed": True, "directory": stat.S_ISDIR(info.st_mode),
                             "owned": info.st_uid == os.geteuid(), "symlink": stat.S_ISLNK(info.st_mode)}
        except OSError:
            report[name] = {"path_existed": False, "directory": False, "owned": False, "symlink": False}
    return candidates, report


def clear_native_cas(cas_root, derived):
    """Delete every native local CAS location the toolchain uses and report what was removed."""
    candidates, report = discover_native_cas(cas_root, derived)
    cleared = []
    for name, path in candidates.items():
        if report[name]["path_existed"]:
            shutil.rmtree(path, ignore_errors=True)
            cleared.append(name)
    if derived.exists():
        shutil.rmtree(derived)
    return {"candidates": report, "cleared": cleared}


def compare_build_outputs(producer_derived, consumer_derived):
    """Byte-compare the executable, the library's .swiftmodule and every object file.

    Comparisons are keyed by path relative to each DerivedData root's `Build`
    directory, since the two consumers use distinct absolute DerivedData paths.
    """
    def outputs(derived):
        build = derived / "Build"
        found = {}
        for path in build.glob("Products/*/SwiftReplayApp"):
            found[str(path.relative_to(build))] = path
        for path in build.glob("Products/*/SwiftReplayLib.swiftmodule/**/*.swiftmodule"):
            found[str(path.relative_to(build))] = path
        for path in build.rglob("*.o"):
            found[str(path.relative_to(build))] = path
        return found

    producer_outputs = outputs(producer_derived)
    consumer_outputs = outputs(consumer_derived)
    require(producer_outputs, "producer build produced no comparable outputs")
    comparisons = {}
    for key, producer_path in producer_outputs.items():
        consumer_path = consumer_outputs.get(key)
        comparisons[key] = consumer_path is not None and producer_path.read_bytes() == consumer_path.read_bytes()
    comparisons["allPresentAndIdentical"] = bool(comparisons) and all(comparisons.values()) and set(producer_outputs) <= set(consumer_outputs)
    return comparisons


def expected_output(lib_value, feature_one, feature_two):
    return f"Swift replay probe {lib_value + feature_one + feature_two}"


class Probe:
    def __init__(self, binary, root):
        self.binary = binary
        self.root = root
        require(not root.exists(), "evidence directory must be new")
        root.mkdir(parents=True, mode=0o700)
        self.private = root / ".private"
        self.private.mkdir(mode=0o700)
        self.env = {key: os.environ[key] for key in ("PATH", "HOME", "DEVELOPER_DIR", "GEM_HOME", "GEM_PATH", "TMPDIR") if key in os.environ}
        self.env.update({"LANG": "en_US.UTF-8", "LC_ALL": "en_US.UTF-8"})
        self.processes = []
        self.timings = {}

    def run(self, name, args, timeout=180, public=False):
        started = time.monotonic()
        path = (self.root if public else self.private) / f"{name}.log"
        try:
            with path.open("wb") as output:
                process = subprocess.run([str(value) for value in args], cwd=self.private, env=self.env,
                                          stdout=output, stderr=subprocess.STDOUT, timeout=timeout, check=False)
                returncode = process.returncode
        finally:
            self.timings[name] = round((time.monotonic() - started) * 1000, 3)
        require(returncode == 0, f"{name} failed; inspect its retained log")
        return path.read_text()

    def start(self, name, args):
        output = (self.private / f"{name}.log").open("wb")
        try:
            process = subprocess.Popen([str(value) for value in args], cwd=self.private, env=self.env,
                                        stdout=output, stderr=subprocess.STDOUT)
        finally:
            output.close()
        self.processes.append(process)
        return process

    def close(self):
        for process in reversed(self.processes):
            if process.poll() is None:
                process.send_signal(signal.SIGINT)
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)

    def fingerprint(self):
        xcode = self.run("xcode-version", ["xcodebuild", "-version"]).strip()
        version_line, build_line = xcode.splitlines()[0], xcode.splitlines()[1]
        facts = {
            "xcodeVersion": version_line, "xcodeBuild": build_line.removeprefix("Build version "),
            "swiftVersion": self.run("swift-version", ["xcrun", "swift", "--version"]).strip(),
            "hostVersion": self.run("host-version", ["sw_vers", "-productVersion"]).strip(),
            "hostBuild": self.run("host-build", ["sw_vers", "-buildVersion"]).strip(),
            "hostArchitecture": self.run("host-architecture", ["uname", "-m"]).strip(),
            "sdk": "macosx", "configuration": "Release",
        }
        require(re.match(r"^Xcode 27\.", facts["xcodeVersion"]), f"expected an Xcode 27.x toolchain, found {facts['xcodeVersion']}")
        write_json(self.root / "toolchain.json", facts)
        self.facts = facts
        self.profile = "xcode-swift-replay-v1-" + hashlib.sha256(json.dumps(facts, sort_keys=True).encode()).hexdigest()

    def generate_project(self):
        project = self.private / "project"
        project.mkdir()
        (project / "Lib.swift").write_text(f"public func swiftReplayLibValue() -> Int32 {{ {LIB_VALUE} }}\n")
        (project / "Feature1.swift").write_text(f"func swiftReplayFeatureOne() -> Int32 {{ {FEATURE_ONE_VALUE} }}\n")
        (project / "Feature2.swift").write_text(f"func swiftReplayFeatureTwo() -> Int32 {{ {FEATURE_TWO_VALUE} }}\n")
        (project / "main.swift").write_text(
            "import SwiftReplayLib\n"
            "let total = swiftReplayLibValue() + swiftReplayFeatureOne() + swiftReplayFeatureTwo()\n"
            'print("Swift replay probe \\(total)")\n'
        )
        (project / "control.m").write_text("int lc_swift_replay_control_value(void) { return 7; }\n")
        generator = self.private / "project.rb"
        generator.write_text(GENERATOR)
        self.run("generate-project", ["ruby", generator, project])
        self.project = project

    def set_feature_one(self, value):
        (self.project / "Feature1.swift").write_text(f"func swiftReplayFeatureOne() -> Int32 {{ {value} }}\n")

    def adapter(self, name, store, compatibility=None):
        socket_path = self.private / f"{name}.sock"
        ready = self.private / f"{name}.json"
        process = self.start(name, [self.binary, "xcode", "serve", "--data-dir", store, "--socket", socket_path,
            "--project", PROJECT, "--compatibility", compatibility or self.profile, "--max-size", BUDGET,
            "--ready-file", ready, "--stats-file", self.root / f"{name}-stats.json"])
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise RuntimeError(f"{name} exited before readiness")
            if ready.exists():
                state = json.loads(self.run(f"{name}-ready", [self.binary, "xcode", "status", "--ready-file", ready]))
                require(state["phase"] == "ready" and not state["storeClosed"] and not state["ownershipReleased"], "adapter not ready")
                return process, ready, socket_path, state["instanceId"]
            time.sleep(0.05)
        raise RuntimeError(f"{name} readiness timed out")

    def stop(self, name, adapter):
        process, ready, socket_path, instance = adapter
        state = json.loads(self.run(f"{name}-stop", [self.binary, "xcode", "stop", "--ready-file", ready]))
        require(state["phase"] == "stopped" and state["storeClosed"] and state["ownershipReleased"]
                and not state["historical"] and state["instanceId"] == instance,
                "stop did not acknowledge exact closed store")
        require(process.wait(timeout=10) == 0 and not socket_path.exists(),
                "adapter did not exit/remove RPC socket after confirmed stop")
        return json.loads((self.root / f"{name}-stats.json").read_text())

    def build(self, name, adapter, derived, cas_root):
        log = self.run(f"{name}-build", ["xcodebuild", "-project", self.project / "Probe.xcodeproj", "-scheme", "Probe",
            "-configuration", "Release", "-derivedDataPath", derived,
            "CODE_SIGNING_ALLOWED=NO", "COMPILER_INDEX_STORE_ENABLE=NO",
            f"COMPILATION_CACHE_CAS_PATH={cas_root}", "COMPILATION_CACHE_ENABLE_CACHING=YES",
            "COMPILATION_CACHE_ENABLE_PLUGIN=YES", f"COMPILATION_CACHE_REMOTE_SERVICE_PATH={adapter[2]}",
            "COMPILATION_CACHE_ENABLE_DETACHED_KEY_QUERIES=YES", "COMPILATION_CACHE_ENABLE_DIAGNOSTIC_REMARKS=YES",
            "build"], timeout=300, public=True)
        require("** BUILD SUCCEEDED **" in log, f"{name} build did not succeed")
        diagnostics, by_file = task_diagnostics(log, ("SwiftCompile", "SwiftEmitModule", "CompileC"))
        binary = derived / "Build/Products/Release/SwiftReplayApp"
        require(binary.is_file(), "expected executable missing")
        output = self.run(f"{name}-output", [binary], public=True).strip()
        return {"log": log, "diagnostics": diagnostics, "byFile": by_file, "output": output, "binary": binary}

    def qualify(self):
        self.fingerprint()
        self.generate_project()

        producer_store = self.private / "producer-store"
        producer_derived = self.private / "derived-producer"
        producer_cas = self.private / "native-cas-producer"
        producer_adapter = self.adapter("producer", producer_store)
        producer = self.build("producer", producer_adapter, producer_derived, producer_cas)
        producer_stats = self.stop("producer", producer_adapter)
        require(producer["output"] == expected_output(LIB_VALUE, FEATURE_ONE_VALUE, FEATURE_TWO_VALUE), "producer output is wrong")
        require(producer_stats["actionWrites"] > 0 and producer_stats["casWrites"] > 0, "producer did not populate the store")

        def phase_copy(name):
            store = self.private / f"{name}-store"
            shutil.copytree(producer_store, store)
            return store

        same_path_store = phase_copy("same-path")
        same_path_derived = self.private / "derived-same-path"
        same_path_cas = self.private / "native-cas-same-path"
        same_path_clear = clear_native_cas(same_path_cas, same_path_derived)
        same_path_adapter = self.adapter("same-path", same_path_store)
        same_path = self.build("same-path", same_path_adapter, same_path_derived, same_path_cas)
        same_path_stats = self.stop("same-path", same_path_adapter)
        require(same_path["output"] == producer["output"], "same-path consumer produced the wrong value")
        same_path_tasks = {name: counts for name, counts in same_path["diagnostics"].items() if name in ("SwiftCompile", "SwiftEmitModule")}
        same_path_all_swift_hit = all(counts["miss"] == 0 and counts["hit"] > 0 for counts in same_path_tasks.values())
        same_path_action_hits_cover_swift = same_path_stats["actionHits"] >= sum(counts["hit"] for counts in same_path_tasks.values())
        same_path_comparison = compare_build_outputs(producer_derived, same_path_derived)

        changed_store = phase_copy("changed")
        changed_derived = self.private / "derived-changed"
        changed_cas = self.private / "native-cas-changed"
        changed_clear = clear_native_cas(changed_cas, changed_derived)
        self.set_feature_one(CHANGED_FEATURE_ONE_VALUE)
        changed_adapter = self.adapter("changed", changed_store)
        changed = self.build("changed", changed_adapter, changed_derived, changed_cas)
        changed_stats = self.stop("changed", changed_adapter)
        require(changed["output"] == expected_output(LIB_VALUE, CHANGED_FEATURE_ONE_VALUE, FEATURE_TWO_VALUE), "changed-source output is wrong")
        feature_one_misses = changed["byFile"].get("Feature1.swift", {}).get("miss", 0)
        other_files_hit = all(changed["byFile"].get(name, {}).get("hit", 0) > 0 for name in ("Feature2.swift", "main.swift"))
        changed_source_qualified = feature_one_misses > 0 and other_files_hit
        self.set_feature_one(FEATURE_ONE_VALUE)

        moved_root = self.private / "moved"
        moved_project_parent = moved_root / "fixture"
        moved_project_parent.mkdir(parents=True)
        shutil.copytree(self.project, moved_project_parent / "project")
        moved_store = phase_copy("moved")
        moved_derived = moved_root / "derived-moved"
        moved_cas = moved_root / "native-cas-moved"
        moved_project = self.project
        self.project = moved_project_parent / "project"
        moved_adapter = self.adapter("moved", moved_store)
        moved = self.build("moved", moved_adapter, moved_derived, moved_cas)
        moved_stats = self.stop("moved", moved_adapter)
        self.project = moved_project
        moved_tasks = {name: counts for name, counts in moved["diagnostics"].items() if name in ("SwiftCompile", "SwiftEmitModule")}
        moved_swift_hits = sum(counts["hit"] for counts in moved_tasks.values())
        moved_swift_misses = sum(counts["miss"] for counts in moved_tasks.values())
        moved_comparison = compare_build_outputs(producer_derived, moved_derived)

        identity_store = phase_copy("identity")
        identity_derived = self.private / "derived-identity"
        identity_cas = self.private / "native-cas-identity"
        clear_native_cas(identity_cas, identity_derived)
        identity_adapter = self.adapter("identity", identity_store, compatibility=self.profile + "-different")
        identity = self.build("identity", identity_adapter, identity_derived, identity_cas)
        identity_stats = self.stop("identity", identity_adapter)
        identity_tasks = {name: counts for name, counts in identity["diagnostics"].items() if name in ("SwiftCompile", "SwiftEmitModule")}
        identity_all_miss = all(counts["hit"] == 0 for counts in identity_tasks.values())

        write_json(self.root / "result.json", {
            "toolchain": self.facts,
            "nativeCasCleared": {"samePath": same_path_clear, "changed": changed_clear},
            "phases": {
                "producer": {"diagnostics": producer["diagnostics"], "stats": {
                    "actionWrites": producer_stats["actionWrites"], "casWrites": producer_stats["casWrites"],
                    "cacheErrors": producer_stats["cacheErrors"]}},
                "samePath": {"diagnostics": same_path["diagnostics"], "stats": {
                    "actionHits": same_path_stats["actionHits"], "casHits": same_path_stats["casHits"],
                    "graphReferenceRejects": same_path_stats.get("graphReferenceRejects", 0),
                    "cacheErrors": same_path_stats["cacheErrors"]},
                    "allSwiftTasksHit": same_path_all_swift_hit, "actionHitsCoverSwiftTasks": same_path_action_hits_cover_swift,
                    "outputsByteIdentical": same_path_comparison},
                "changedSource": {"diagnostics": changed["diagnostics"], "byFile": changed["byFile"], "stats": {
                    "actionHits": changed_stats["actionHits"], "actionWrites": changed_stats["actionWrites"],
                    "cacheErrors": changed_stats["cacheErrors"]},
                    "changedFileMisses": feature_one_misses, "otherSwiftFilesHit": other_files_hit},
                "movedPath": {"diagnostics": moved["diagnostics"], "stats": {
                    "actionHits": moved_stats["actionHits"], "casHits": moved_stats["casHits"],
                    "cacheErrors": moved_stats["cacheErrors"]},
                    "swiftHits": moved_swift_hits, "swiftMisses": moved_swift_misses,
                    "outputsByteIdentical": moved_comparison, "measuredNotGating": True},
                "toolchainIdentity": {"diagnostics": identity["diagnostics"], "stats": {
                    "actionHits": identity_stats["actionHits"], "actionWrites": identity_stats["actionWrites"],
                    "cacheErrors": identity_stats["cacheErrors"]},
                    "allSwiftTasksMissed": identity_all_miss},
            },
            "qualified": {
                "samePathSwiftReplay": same_path_all_swift_hit and same_path_action_hits_cover_swift
                    and same_path_comparison.get("allPresentAndIdentical", False) and same_path["output"] == producer["output"],
                "changedSourceSwiftMiss": changed_source_qualified,
                "toolchainIdentityMiss": identity_all_miss,
            },
        })
        require(same_path_all_swift_hit, "not every SwiftCompile/SwiftEmitModule task hit the closed store on the same checkout path")
        require(same_path_action_hits_cover_swift, "adapter actionHits do not cover the observed Swift task hits")
        require(same_path_comparison.get("allPresentAndIdentical", False), "same-path consumer outputs are not byte-identical to the producer")
        require(changed_source_qualified, "changed-source phase did not isolate the miss to the edited file")
        require(identity_all_miss, "a different --compatibility value replayed a Swift action; toolchain identity is not keyed correctly")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("evidence", type=Path)
    args = parser.parse_args()
    require(os.uname().sysname == "Darwin", "real Apple compiler gate requires macOS")
    os.umask(0o077)
    probe = Probe(args.binary.resolve(strict=True), args.evidence.resolve())
    try:
        probe.qualify()
        print("Qualified Swift compilation replay on the same checkout path; path portability measured, not qualified.")
    finally:
        probe.close()


if __name__ == "__main__":
    main()
