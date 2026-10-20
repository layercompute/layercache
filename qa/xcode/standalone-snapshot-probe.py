#!/usr/bin/env python3
"""Opt-in real Clang/PCH proof. This is never a simulator/runtime or Swift proof."""
import argparse
import importlib.util
import hashlib
import http.client
import http.server
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import socket
import stat
import sys
import subprocess
import tempfile
import threading
import time
import urllib.request

sys.dont_write_bytecode = True

PROJECT = "github.com/layercache/xcode-standalone-qa"
GENERATION = "xcode-snapshots/qualification-1"
BUDGET = 512 << 20
EXPECTED = {"xcodeBuild": "27A266a", "sdkBuild": "24A430", "hostBuild": "26A434"}


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n")
    path.chmod(0o600)


def digest(path):
    value = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1 << 20), b""):
            value.update(block)
    return value.hexdigest()


def node_runtime_version(output):
    value = output.strip()
    require(re.fullmatch(r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)", value) is not None,
            "archive Node runtime did not return a fixed semver")
    return value


def clang_diagnostics(log):
    """Associate remarks with CompileC/main.m and ProcessPCH, never Swift tasks."""
    result = {name: {"hit": 0, "miss": 0} for name in ("compile", "pch")}
    current = None
    for line in log.splitlines():
        if line.startswith("CompileC "):
            current = "compile" if re.search(r"(?:^|[/ ])main\.m(?: |$)", line) else None
        elif line.startswith("ProcessPCH ") or line.startswith("ProcessPCH++ "):
            current = "pch"
        elif re.match(r"^[A-Z][A-Za-z0-9]+ ", line):
            current = None
        diagnostic = re.match(r"^(?:note|remark):\s+(?:replay(?:ed)? )?cache (hit|miss)\b", line, re.I)
        if current and diagnostic:
            result[current][diagnostic.group(1).lower()] += 1
    return result


def validate_output(disassembly, value):
    # otool prints only this tiny function before the next symbol. Require the
    # actual return register's immediate, rather than a marker in unrelated data.
    function = re.search(r"^_lc_probe_value:\s*\n(.*?)(?=^\S[^\n]*:\s*$|\Z)", disassembly, re.M | re.S)
    require(function is not None, "compiled probe symbol missing")
    immediate = rf"(?:0x{value:x}|{value})(?![0-9a-f])"
    require(re.search(rf"\bmov\s+w0,\s*#{immediate}", function.group(1), re.I), "compiled function returns stale/wrong value")
    require(re.search(r"\bret\b", function.group(1)), "compiled function has no return")


_archive_spec = importlib.util.spec_from_file_location("layercache_archive_outcome", Path(__file__).with_name("archive_command_outcome.py"))
_archive_module = importlib.util.module_from_spec(_archive_spec)
_archive_spec.loader.exec_module(_archive_module)
ARCHIVE_FAILURE_CODES = _archive_module.ARCHIVE_FAILURE_CODES
archive_failure_code = _archive_module.archive_failure_code
archive_command_outcome = _archive_module.archive_command_outcome


def semantic_mapping_qualified(shapes, semantics):
    """A candidate grammar needs actual indexed leaf mapping before replay."""
    if not isinstance(shapes, dict) or shapes.get("available") is not True or not isinstance(semantics, dict) or semantics.get("available") is not True or semantics.get("workerStopped") is not True:
        return False
    declared = {}
    for item in shapes.get("objects", []):
        if item.get("expectedNativeNode") is not True:
            continue
        ordinal, node = item.get("ordinal"), item.get("node")
        if type(ordinal) is not int or ordinal in declared or not isinstance(node, dict):
            return False
        markers = [chunk for chunk in node.get("chunks", []) if chunk.get("field", {}).get("number") == 2]
        if node.get("sequenceComplete") is not True or node.get("onlyTopLevelChunks") is not True or node.get("markerCount") != 1 or len(markers) != 1:
            return False
        marker = markers[0]
        if not all(marker.get(key) is True for key in ("leadingUniqueMarker", "canonicalPermutationOneBased", "indexCountMatchesLiterals")):
            return False
        declared[ordinal] = node.get("literalChunkCount")
    if not declared:
        return False
    observed = set()
    for item in semantics.get("objects", []):
        ordinal = item.get("ordinal")
        if type(ordinal) is not int or ordinal in observed or ordinal not in declared:
            return False
        observed.add(ordinal)
        if not all(item.get(key) is True for key in ("available", "comparisonComplete", "inlineReferenceCountMatches", "inlineReferenceMultisetMatches", "referencedObjectsAreLeaves", "indexedOneBasedReferencesMatch")):
            return False
        if item.get("dataBytes") != 0 or type(item.get("referenceCount")) is not int or not 1 <= item["referenceCount"] <= 256 or item["referenceCount"] != declared[ordinal] or item.get("referenceObjectsChecked") != item["referenceCount"]:
            return False
    return observed == set(declared)


class Audit(http.server.ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, target):
        super().__init__(("127.0.0.1", 0), AuditHandler)
        self.target = target
        self.phase = "setup"
        self.records = []
        self.published_digests = {}
        self.lock = threading.Lock()


class RequestBody:
    """Stream one explicitly sized request; digests never enter public reports."""
    def __init__(self, source, length):
        self.source, self.remaining = source, length
        self.hash = hashlib.sha256()

    def read(self, size=-1):
        if not self.remaining:
            return b""
        size = self.remaining if size < 0 else min(size, self.remaining)
        block = self.source.read(size)
        require(bool(block) and len(block) <= self.remaining, "fixture request was partial")
        self.remaining -= len(block)
        self.hash.update(block)
        return block


def body_verification(length, received, digest_value, header, published_digest):
    complete = length is not None and length == received
    valid_header = isinstance(header, str) and re.fullmatch(r"sha256:[a-f0-9]{64}", header) is not None
    return {"responseBodyComplete": complete,
            "responseDigestMatchesHeader": bool(complete and valid_header and digest_value.hex() == header[7:]),
            "responseMatchesPublishedPUT": bool(complete and published_digest is not None and digest_value == published_digest)}


class AuditHandler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_):
        pass  # Never log credentials, URL query, request bodies or headers.

    def forward(self):
        started = time.monotonic()
        length = int(self.headers.get("Content-Length", "0"))
        require(0 <= length <= BUDGET, "fixture request exceeds bound")
        body = RequestBody(self.rfile, length) if length else None
        connection = http.client.HTTPConnection(*self.server.target, timeout=120)
        headers = {name: value for name, value in self.headers.items()
                   if name.lower() not in {"host", "connection", "transfer-encoding"}}
        sent = 0
        response_hash = hashlib.sha256()
        artifact = self.path.startswith("/v8/artifacts/")
        with self.server.lock:
            phase = self.server.phase
        try:
            connection.request(self.command, self.path, body=body, headers=headers)
            require(body is None or body.remaining == 0, "fixture request was partial")
            response = connection.getresponse()
            self.send_response(response.status)
            for name, value in response.getheaders():
                if name.lower() not in {"connection", "transfer-encoding"}:
                    self.send_header(name, value)
            self.send_header("Connection", "close")
            self.end_headers()
            if self.command != "HEAD":
                while block := response.read(256 << 10):
                    sent += len(block)
                    response_hash.update(block)
                    require(sent <= BUDGET, "fixture response exceeds bound")
                    self.wfile.write(block)
            with self.server.lock:
                verification = {}
                if artifact and self.command == "PUT" and 200 <= response.status < 300 and body is not None:
                    self.server.published_digests[self.path] = body.hash.digest()
                    verification["publishedRequestComplete"] = body.remaining == 0
                if artifact and self.command == "GET" and response.status == 200:
                    declared_size = response.getheader("Content-Length")
                    declared_size = int(declared_size) if declared_size and declared_size.isdigit() else None
                    verification = body_verification(declared_size, sent, response_hash.digest(),
                        response.getheader("X-LayerCache-Digest"), self.server.published_digests.get(self.path))
                self.server.records.append({"phase": phase, "method": self.command,
                    "artifact": artifact, "status": response.status,
                    "requestBytes": length, "responseBytes": sent,
                    "elapsedMs": round((time.monotonic() - started) * 1000, 3), **verification})
        finally:
            connection.close()
            self.close_connection = True

    do_GET = do_HEAD = do_POST = do_PUT = forward


class Probe:
    def __init__(self, binary, native, root):
        self.binary, self.native, self.root = binary, native, root
        require(not root.exists(), "evidence directory must be new")
        root.mkdir(parents=True, mode=0o700)
        self.private = root / ".private"
        self.private.mkdir(mode=0o700)
        self.controls = Path(tempfile.mkdtemp(prefix="lc-xs-", dir="/tmp"))
        self.controls.chmod(0o700)
        self.processes = []
        # Retain only platform/tool discovery and Ruby's installed gem path.
        self.env = {key: os.environ[key] for key in ("PATH", "HOME", "DEVELOPER_DIR", "GEM_HOME", "GEM_PATH", "TMPDIR") if key in os.environ}
        self.env.update({"LANG": "en_US.UTF-8", "LC_ALL": "en_US.UTF-8", "NODE_NO_WARNINGS": "1", "GOMAXPROCS": "2",
                         "XDG_CONFIG_HOME": str(self.private / "config"), "XDG_CACHE_HOME": str(self.private / "cache"),
                         "XDG_STATE_HOME": str(self.private / "state")})
        self.timings = {}
        self.diagnostic_store = None
        self.shape_helper = None
        self.last_archive_failure_code = None
        self.last_archive_outcome = None
        self.producer_shapes = None
        self.producer_semantics = None

    def run(self, name, args, timeout=120, public=False):
        started = time.monotonic()
        path = (self.root if public else self.private) / f"{name}.log"
        returncode, timed_out = None, False
        try:
            with path.open("wb") as output:
                process = subprocess.run([str(value) for value in args], cwd=self.private, env=self.env,
                                         stdout=output, stderr=subprocess.STDOUT, timeout=timeout, check=False)
                returncode = process.returncode
        except subprocess.TimeoutExpired:
            timed_out = True
            raise
        finally:
            self.timings[name] = round((time.monotonic() - started) * 1000, 3)
            if name in ("writer-save", "reader-restore") and (returncode != 0 or timed_out):
                self.last_archive_outcome = archive_command_outcome(path, returncode=returncode, timed_out=timed_out)
                self.last_archive_failure_code = self.last_archive_outcome["archiveFailureCode"]
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
                    process.kill()  # Only this fixture's unreaped child.
                    process.wait(timeout=5)
        shutil.rmtree(self.controls)

    def fingerprint(self):
        def capture(name, args):
            return self.run(name, args).strip()
        clang = Path(capture("clang-path", ["xcrun", "--sdk", "iphonesimulator", "--find", "clang"]))
        xcodebuild = Path(capture("xcodebuild-path", ["xcrun", "--find", "xcodebuild"]))
        xcode = capture("xcode-version", ["xcodebuild", "-version"])
        facts = {"xcodeVersion": xcode.splitlines()[0], "xcodeBuild": xcode.splitlines()[1].removeprefix("Build version "),
                 "clangVersion": capture("clang-version", [clang, "--version"]), "clangSHA256": digest(clang),
                 "xcodebuildSHA256": digest(xcodebuild), "sdk": "iphonesimulator",
                 "sdkVersion": capture("sdk-version", ["xcrun", "--sdk", "iphonesimulator", "--show-sdk-version"]),
                 "sdkBuild": capture("sdk-build", ["xcrun", "--sdk", "iphonesimulator", "--show-sdk-build-version"]),
                 "hostVersion": capture("host-version", ["sw_vers", "-productVersion"]),
                 "hostBuild": capture("host-build", ["sw_vers", "-buildVersion"]),
                 "hostArchitecture": capture("host-architecture", ["uname", "-m"]),
                 "targetArchitecture": "arm64", "targetPlatform": "ios-simulator", "minimumIOS": "16.4",
                 "configuration": "Release", "clangOptimization": "-O2", "swiftUsed": False}
        facts["archiveNodeVersion"] = node_runtime_version(capture("node-version", ["node", "--version"]))
        write_json(self.root / "toolchain.json", facts)
        require(facts["archiveNodeVersion"] == "v24.13.0", "unexpected archive Node runtime")
        require(facts["hostArchitecture"] == "arm64" and facts["xcodeVersion"] == "Xcode 27.0", "expected Xcode 27 arm64 host")
        require(all(facts[key] == value for key, value in EXPECTED.items()), "runner differs from requested Xcode Cloud build/SDK/OS profile")
        require(facts["sdkVersion"] == "27.0" and facts["hostVersion"] == "27.0.1", "unexpected SDK/host version")
        compiler_facts = {key: value for key, value in facts.items() if key != "archiveNodeVersion"}
        self.profile = "xcode-standalone-v1-" + hashlib.sha256(json.dumps(compiler_facts, sort_keys=True).encode()).hexdigest()

    def prepare_project(self):
        project = self.private / "project"
        project.mkdir()
        (project / "Prefix.pch").write_text("#include <stdint.h>\nstruct LCProbe { int32_t value; };\n")
        self.source = project / "main.m"
        self.write_source(42)
        script = r'''
require 'xcodeproj'
root = ARGV.fetch(0)
project = Xcodeproj::Project.new("#{root}/Probe.xcodeproj")
target = project.new_target(:static_library, 'Probe', :ios, '16.4')
target.source_build_phase.add_file_reference(project.main_group.new_file('main.m'))
target.build_configurations.each do |config|
  config.build_settings['ARCHS'] = 'arm64'
  config.build_settings['ONLY_ACTIVE_ARCH'] = 'YES'
  config.build_settings['SDKROOT'] = 'iphonesimulator'
  config.build_settings['SUPPORTED_PLATFORMS'] = 'iphonesimulator'
  config.build_settings['CODE_SIGNING_ALLOWED'] = 'NO'
  config.build_settings['GCC_PRECOMPILE_PREFIX_HEADER'] = 'YES'
  config.build_settings['GCC_PREFIX_HEADER'] = '$(SRCROOT)/Prefix.pch'
  config.build_settings['GCC_OPTIMIZATION_LEVEL'] = '2'
end
project.save
scheme = Xcodeproj::XCScheme.new
scheme.add_build_target(target)
scheme.save_as(project.path, 'Probe', true)
'''
        generator = self.private / "project.rb"
        generator.write_text(script)
        self.run("generate-project", ["ruby", generator, project])
        self.project = project

    def write_source(self, value):
        self.source.write_text(f'__attribute__((noinline)) int lc_probe_value(void) {{ return {value}; }}\nconst char lc_probe_marker[] = "LC_VALUE_{value}";\n')

    def team(self):
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        address = f"127.0.0.1:{port}"
        config = self.private / "team.json"
        self.run("setup-team", [self.binary, "setup", "--config", config, "--data-dir", self.private / "team-store", "--listen", address,
            "--role", "team", "--project", PROJECT, "--compatibility-id", self.profile, "--max-size", BUDGET, "--non-interactive", "--json"])
        grants = []
        for role in ("writer", "reader"):
            token = f"lcs1.fixture-{role}.{secrets.token_hex(32)}"
            credential = self.private / f"{role}.credential"
            credential.write_text(token + "\n")
            credential.chmod(0o600)
            grants.append({"id": f"fixture-{role}", "secretSha256": hashlib.sha256(token.encode()).hexdigest(), "project": PROJECT,
                "compatibility": self.profile, "keyPrefix": "xcode-snapshots/", "role": role,
                "expiresAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() + 3600))})
        grant_file = self.private / "grants.json"
        write_json(grant_file, {"version": 1, "credentials": grants})
        value = json.loads(config.read_text())
        value["snapshotCredentialsFile"] = str(grant_file)
        write_json(config, value)
        self.start("team", [self.binary, "serve", "--config", config])
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            try:
                with urllib.request.urlopen(f"http://{address}/healthz", timeout=0.25) as response:
                    if response.status == 200:
                        break
            except (OSError, TimeoutError):
                time.sleep(0.05)
        else:
            raise RuntimeError("embedded Team did not become healthy")
        self.audit = Audit(("127.0.0.1", port))
        threading.Thread(target=self.audit.serve_forever, daemon=True).start()
        self.endpoint = f"http://127.0.0.1:{self.audit.server_port}"

    def adapter(self, name, store):
        socket_path, ready = self.controls / f"{name}.sock", self.controls / f"{name}.json"
        process = self.start(name, [self.binary, "xcode", "serve", "--data-dir", store, "--socket", socket_path, "--project", PROJECT,
            "--compatibility", self.profile, "--max-size", BUDGET, "--ready-file", ready, "--stats-file", self.root / f"{name}-stats.json"])
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
        require(state["phase"] == "stopped" and state["storeClosed"] and state["ownershipReleased"] and not state["historical"] and state["instanceId"] == instance,
                "stop did not acknowledge exact closed store")
        require(process.wait(timeout=10) == 0 and not socket_path.exists(), "adapter did not exit/remove RPC socket after confirmed stop")
        # Public receipt deliberately omits instance identities and filesystem paths.
        write_json(self.root / f"{name}-closure.json", {"phase": state["phase"], "storeClosed": True,
            "ownershipReleased": True, "historical": False, "processExited": True, "exactInstanceMatched": True, "rpcSocketAbsent": True})
        return json.loads((self.root / f"{name}-stats.json").read_text())

    def build(self, name, adapter, value):
        derived = self.private / f"derived-{name}"
        cas = self.private / "native-cas"
        if cas.exists():
            shutil.rmtree(cas)  # This fixture's native CAS only, never a shared cache.
        require(not derived.exists() and not cas.exists(), "consumer must start with fresh DerivedData and native CAS")
        log = self.run(f"{name}-build", ["xcodebuild", "-project", self.project / "Probe.xcodeproj", "-scheme", "Probe", "-configuration", "Release",
            "-sdk", "iphonesimulator", "-destination", "generic/platform=iOS Simulator", "-jobs", "1", "-derivedDataPath", derived,
            "ARCHS=arm64", "ONLY_ACTIVE_ARCH=YES", "CODE_SIGNING_ALLOWED=NO", "COMPILER_INDEX_STORE_ENABLE=NO",
            f"COMPILATION_CACHE_CAS_PATH={cas}", "COMPILATION_CACHE_ENABLE_CACHING=YES", "COMPILATION_CACHE_ENABLE_PLUGIN=YES",
            f"COMPILATION_CACHE_REMOTE_SERVICE_PATH={adapter[2]}", "COMPILATION_CACHE_ENABLE_DETACHED_KEY_QUERIES=YES",
            "COMPILATION_CACHE_ENABLE_DIAGNOSTIC_REMARKS=YES", "build"], timeout=300, public=True)
        require("** BUILD SUCCEEDED **" in log and "CompileC " in log and "ProcessPCH " in log, "actual Clang/PCH build absent")
        objects = list(derived.glob("Build/Intermediates.noindex/Probe.build/Release-iphonesimulator/Probe.build/Objects-normal/arm64/main.o"))
        require(len(objects) == 1, "expected one arm64 simulator object")
        object_path = objects[0]
        require(f"LC_VALUE_{value}".encode() in object_path.read_bytes(), "compiled object marker is stale")
        assembly = self.run(f"{name}-disassembly", ["xcrun", "otool", "-tvV", object_path], public=True)
        validate_output(assembly, value)
        return {"sourceSHA256": digest(self.source), "objectSHA256": digest(object_path), "value": value,
                "freshDerivedData": True, "freshNativeCAS": True, "clangDiagnostics": clang_diagnostics(log)}

    def archive(self, role, operation, destination):
        with self.audit.lock:
            self.audit.phase = role
        monitor = Path(__file__).with_name("archive-fatal-monitor.mjs")
        require(monitor.is_file(), "archive diagnostic monitor unavailable")
        output = self.run(f"{role}-{operation}", ["node", "--import", monitor, self.native, operation, "--endpoint", self.endpoint,
            "--credential-file", self.private / f"{role}.credential", "--project", PROJECT, "--compatibility", self.profile,
            "--key", GENERATION, "--path", destination, "--cache-dir", self.private / f"{role}-archive-cache",
            "--max-bytes", BUDGET, "--timeout-seconds", "120"], timeout=150)
        result = json.loads(output)
        write_json(self.root / f"{role}-archive.json", {key: result[key] for key in ("hit", "source", "digest", "bytes", "elapsedMs")})
        return result

    def collect_producer_semantics(self, store):
        """Query only the stopped producer, before consumer native-CAS removal."""
        unavailable = {"version": 1, "available": False, "reason": "invalidInput", "abi": None, "objects": [], "workerStopped": True}
        sdk_started, worker_stopped = False, True
        root, plugin = self.private / "native-cas", self.private / "native-cas/plugin"
        candidates = {"configuredRoot": root, "configuredPlugin": plugin,
                      "configuredBuiltin": root / "builtin",
                      "producerDerivedBuiltin": self.private / "derived-producer/CompilationCache.noindex/builtin"}
        locations = {"version": 1, "phase": "producer-closed-before-consumer", "candidates": {},
                     "producerStoreClosed": True, "storeConfirmedByBuild": False, "selectedPluginLibrary": None}
        for name, path in candidates.items():
            try:
                info = path.lstat()
                locations["candidates"][name] = {"exists": True, "directory": stat.S_ISDIR(info.st_mode),
                    "owned": info.st_uid == os.geteuid(), "symlink": stat.S_ISLNK(info.st_mode)}
            except OSError:
                locations["candidates"][name] = {"exists": False, "directory": False, "owned": False, "symlink": False}
        try:
            developer = Path(self.env["DEVELOPER_DIR"]) if self.env.get("DEVELOPER_DIR") else Path(self.run("selected-developer-path", ["xcode-select", "-p"]).strip())
            libraries = {"developerLibrary": developer / "usr/lib/libToolchainCASPlugin.dylib",
                         "toolchainLibrary": developer / "Toolchains/XcodeDefault.xctoolchain/usr/lib/libToolchainCASPlugin.dylib"}
            locations["libraries"] = {name: path.is_file() for name, path in libraries.items()}
            existing = [(name, path) for name, path in libraries.items() if path.is_file()]
            require(existing and (len(existing) == 1 or existing[0][1].samefile(existing[1][1])), "semantic plugin location is unavailable or ambiguous")
            selected, library = existing[0]
            locations["selectedPluginLibrary"] = selected
            for name in ("configuredRoot", "configuredPlugin"):
                location = locations["candidates"][name]
                require(location["directory"] and location["owned"] and not location["symlink"], "producer native CAS is unavailable")
            with (self.root / "producer-build.log").open() as log:
                locations["storeConfirmedByBuild"] = any(line.startswith("ValidateCAS ") and str(plugin) in line for line in log)
            require(locations["storeConfirmedByBuild"], "producer did not validate the selected native CAS")
            # Xcode can create these exact owned fixture directories as 0755.
            # Tighten metadata before the worker's independent private-path gate.
            for path in (root, plugin):
                path.chmod(0o700)
            probe = Path(__file__).with_name("plugin-semantics.py")
            require(self.shape_helper and probe.is_file(), "semantic fixture helper unavailable")
            manifest = self.private / "producer-semantic-manifest.json"
            shape_output = self.run("producer-semantic-manifest", [self.shape_helper, store, PROJECT, self.profile,
                "--semantic-manifest", manifest], timeout=15)
            require(len(shape_output.encode()) <= 256 << 10, "producer shape output exceeds fixed bound")
            self.producer_shapes = json.loads(shape_output)
            sdk_started = True
            output = self.run("producer-plugin-semantics", [sys.executable, probe, "--plugin", library,
                "--cas-path", plugin, "--manifest", manifest], timeout=15)
            require(len(output.encode()) <= 128 << 10, "semantic output exceeds fixed bound")
            value = json.loads(output)
            # Reuse the probe owner's payload-free schema validation before
            # saving stdout as public evidence. Importing this module loads no SDK.
            spec = importlib.util.spec_from_file_location("layercache_plugin_semantics", probe)
            module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(module)
            require(module.valid_report(value), "invalid semantic result schema")
            worker_stopped = value["workerStopped"]
        except Exception:
            worker_stopped = not sdk_started
            value = {**unavailable, "workerStopped": worker_stopped, "reason": "workerFailed" if sdk_started else "invalidInput"}
        write_json(self.root / "native-cas-locations.json", locations)
        write_json(self.root / "plugin-semantics.json", value)
        self.producer_semantics = value
        return worker_stopped

    def qualify(self):
        self.fingerprint()
        self.prepare_project()
        self.team()
        producer_store = self.private / "producer-store"
        producer = self.adapter("producer", producer_store)
        cold = self.build("producer", producer, 42)
        cold_stats = self.stop("producer", producer)
        self.diagnostic_store = producer_store
        worker_stopped = self.collect_producer_semantics(producer_store)
        require(worker_stopped, "semantic native worker exit is unconfirmed; preserve producer native CAS")
        require(semantic_mapping_qualified(self.producer_shapes, self.producer_semantics),
                "producer native indexed leaf mapping is not qualified")
        require(cold_stats["actionWrites"] > 0 and cold_stats["casWrites"] > 0 and cold_stats["cacheErrors"] == 0,
                "producer did not populate healthy external compiler store")
        require(not any(path.is_socket() for path in producer_store.rglob("*")), "closed snapshot contains a socket")
        published = self.archive("writer", "save", producer_store)
        require(published["source"] == "team" and published["bytes"] > 0 and re.fullmatch(r"[a-f0-9]{64}", published["digest"]),
                "Team snapshot publication was not acknowledged with a complete digest")
        # Preserve the exact closed producer only for failure diagnosis. Its old
        # path disappears, and no consumer adapter ever opens this renamed tree.
        self.diagnostic_store = self.private / "closed-producer-diagnostic"
        producer_store.rename(self.diagnostic_store)
        require(not producer_store.exists(), "producer path must be absent before remote restore")
        destination = self.private / "restored"
        restored = self.archive("reader", "restore", destination)
        require(restored["hit"] and restored["source"] == "team" and restored["digest"] == published["digest"]
                and restored["bytes"] == published["bytes"], "fresh reader did not restore exact remote archive")
        consumer_store = destination / "producer-store"
        require(consumer_store.is_dir(), "archive did not preserve expected top-level store")
        consumer = self.adapter("consumer", consumer_store)
        warm = self.build("consumer", consumer, 42)
        warm_stats = self.stop("consumer", consumer)
        require(warm["sourceSHA256"] == cold["sourceSHA256"], "reader source changed")
        require(warm["clangDiagnostics"]["compile"]["hit"] > 0 and warm["clangDiagnostics"]["pch"]["hit"] > 0,
                "actual Clang compilation and PCH did not both report cache hits")
        require(warm_stats["actionHits"] > 0 and warm_stats["casHits"] > 0 and warm_stats["graphChecks"] > 0
                and warm_stats["cacheErrors"] == 0, "fresh remote compiler replay lacks healthy action/CAS/graph evidence")
        self.write_source(43)
        changed_adapter = self.adapter("changed", consumer_store)
        changed = self.build("changed", changed_adapter, 43)
        changed_stats = self.stop("changed", changed_adapter)
        require(changed["sourceSHA256"] != cold["sourceSHA256"] and changed["objectSHA256"] != warm["objectSHA256"], "changed source reused stale bytes")
        require(changed["clangDiagnostics"]["compile"]["miss"] > 0 and changed_stats["actionReads"] > changed_stats["actionHits"]
                and changed_stats["actionWrites"] > 0 and changed_stats["cacheErrors"] == 0, "changed source did not compile on a real cache miss")
        records = self.audit.records
        puts = [row for row in records if row["method"] == "PUT"]
        gets = [row for row in records if row["artifact"] and row["method"] == "GET"]
        require(len(puts) == 1 and puts[0]["phase"] == "writer" and 200 <= puts[0]["status"] < 300, "unexpected archive writer requests")
        require(len(gets) == 1 and gets[0]["phase"] == "reader" and gets[0]["status"] == 200, "reader did not download one remote archive")
        require(gets[0]["responseBodyComplete"] and gets[0]["responseDigestMatchesHeader"] and gets[0]["responseMatchesPublishedPUT"],
                "remote reader bytes differ from acknowledged publication")
        write_json(self.root / "transfer-audit.json", records)
        write_json(self.root / "result.json", {"passed": True, "compiler": "Clang/PCH", "swiftReplayQualified": False,
            "simulatorRuntimeExecuted": False, "signedDeviceBuildQualified": False, "xcodeCloudAdoptionQualified": False,
            "remote": "isolated-loopback-Team", "profile": self.profile, "producer": cold, "consumer": warm, "changed": changed,
            "readerPUTs": 0, "closedBeforeSave": True, "producerStoreAbsentBeforeRestore": True,
            "producerPreservedOnlyForFailureDiagnostic": True,
            "archiveSHA256": restored["digest"], "archiveBytes": restored["bytes"], "commandElapsedMs": self.timings,
            "timingBoundary": "archive save/restore includes auth, packing/verification/transfer/extraction; not pure network bandwidth"})


    def capture_failure_shapes(self):
        """Called after all owned children stop; never inspect a live store."""
        if self.shape_helper and self.diagnostic_store and self.diagnostic_store.is_dir():
            try:
                output = self.run("failure-shapes", [self.shape_helper, self.diagnostic_store, PROJECT, self.profile], timeout=15)
                require(len(output.encode()) <= 256 << 10, "shape report exceeds fixed limit")
                value = json.loads(output)
            except Exception:
                value = {"version": 1, "available": False}
        else:
            value = {"version": 1, "available": False}
        encoded = json.dumps(value, separators=(",", ":")).encode() + b"\n"
        require(len(encoded) <= 256 << 10, "shape report exceeds fixed limit")
        (self.root / "failure-shapes.json").write_bytes(encoded)
        (self.root / "failure-shapes.json").chmod(0o600)
        write_json(self.root / "failure-summary.json", {"passed": False,
            "confirmedClosedProducerPreserved": self.diagnostic_store is not None,
            "archiveFailureCode": self.last_archive_failure_code,
            "archiveCommandOutcome": self.last_archive_outcome,
            "commandElapsedMs": self.timings})
        if hasattr(self, "audit"):
            write_json(self.root / "transfer-audit.json", self.audit.records)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("native_cli", type=Path)
    parser.add_argument("evidence", type=Path)
    parser.add_argument("--shape-helper", type=Path, help="QA-only bounded closed-store diagnostic executable")
    args = parser.parse_args()
    require(os.uname().sysname == "Darwin", "real Apple compiler gate requires macOS")
    os.umask(0o077)
    probe = Probe(args.binary.resolve(strict=True), args.native_cli.resolve(strict=True), args.evidence.resolve())
    probe.shape_helper = args.shape_helper.resolve(strict=True) if args.shape_helper else None
    failed = False
    try:
        probe.qualify()
        print("Qualified standalone remote Clang/PCH snapshot replay and changed-input miss; Swift/Cloud adoption remain unqualified.")
    except BaseException:
        failed = True
        raise
    finally:
        if hasattr(probe, "audit"):
            probe.audit.shutdown()
            probe.audit.server_close()
        probe.close()
        if failed:
            probe.capture_failure_shapes()


if __name__ == "__main__":
    main()
