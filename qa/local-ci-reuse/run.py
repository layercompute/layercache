#!/usr/bin/env python3
"""Exercise an installed LayerCache and real Turbo without production services."""

import argparse
import hashlib
import http.server
import json
import os
import pathlib
import re
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request

REPOSITORY = "https://github.com/layercache/local-ci-reuse-fixture.git"
READY = "Team Cache ready: 2/2 successful tasks"


class Experiment:
    def __init__(self, args):
        self.args = args
        self.root = pathlib.Path(tempfile.mkdtemp(prefix="layercache-local-ci-reuse-"))
        self.processes = []
        self.proxy = None
        self.block_publication = False
        self.environment = {
            key: value for key, value in os.environ.items()
            if not key.startswith(("TURBO_", "LAYER_CACHE_", "ACTIONS_", "GITHUB_"))
        }
        self.environment.update({
            "CI": "true", "NO_COLOR": "1", "TURBO_TELEMETRY_DISABLED": "1",
            "GIT_TERMINAL_PROMPT": "0", "TEST_PROFILE": "profile-one",
            "EXECUTION_COUNTER": str(self.root / "executions.jsonl"),
        })
        self.evidence = {
            "schemaVersion": "local-ci-reuse-v1", "scenarios": [],
            "binarySha256": self.digest(pathlib.Path(args.binary)),
            "platform": sys.platform, "passed": False,
        }

    @staticmethod
    def digest(path):
        return hashlib.sha256(path.read_bytes()).hexdigest()

    def execute(self, args, workspace=None, expected=0, profile="profile-one"):
        result = subprocess.run(
            args, cwd=workspace, env=self.environment | {"TEST_PROFILE": profile},
            text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=40,
        )
        if expected is not None and result.returncode != expected:
            # Setup arguments and output can contain temporary credentials.
            details = "" if len(args) > 1 and args[1] == "setup" else result.stdout + result.stderr
            raise AssertionError(f"{pathlib.Path(args[0]).name} exited {result.returncode}, expected {expected}\n{details}")
        return result

    @staticmethod
    def write_json(path, value):
        path.write_text(json.dumps(value) + "\n")

    def template(self):
        root = self.root / "template"
        shutil.copytree(self.args.fixture, root, ignore=shutil.ignore_patterns("node_modules", ".turbo"))
        for command in [
            ["git", "init", "--quiet", "-b", "main"],
            ["git", "remote", "add", "origin", REPOSITORY],
            ["git", "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"],
            ["git", "add", "."],
            ["git", "-c", "user.name=LayerCache QA", "-c", "user.email=qa@layercache.dev", "commit", "--quiet", "-m", "Reuse fixture"],
        ]:
            self.execute(command, root)
        self.template_root = root

    def clone(self, name, source="one", fail=False, dependency="dependency-one"):
        workspace = self.root / name
        self.execute(["git", "clone", "--quiet", "--no-hardlinks", str(self.template_root), str(workspace)])
        self.execute(["git", "remote", "set-url", "origin", REPOSITORY], workspace)
        self.input(workspace, source, fail, dependency)
        return workspace

    def input(self, workspace, source="one", fail=False, dependency="dependency-one"):
        self.write_json(workspace / "packages/app/fixture.json", {"source": source, "fail": fail})
        self.write_json(workspace / "dependency.json", {"version": dependency})

    @staticmethod
    def address():
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            return "127.0.0.1:" + str(listener.getsockname()[1])

    def setup(self, name, workspace, team=None, role="local", compatibility=None):
        config_path = self.root / (name + ".json")
        command = [
            self.args.binary, "setup", "--config", str(config_path),
            "--data-dir", str(self.root / (name + "-cache")),
            "--listen", self.address(), "--max-size", "268435456", "--role", role,
            "--remote-metadata-timeout", "300ms", "--remote-transfer-idle-timeout", "300ms",
            "--non-interactive", "--json",
        ]
        if team:
            command.extend(["--team-url", self.proxy_url, "--team-token", team["localToken"]])
        if compatibility:
            command.extend(["--compatibility-id", compatibility])
        self.execute(command, workspace)
        config = json.loads(config_path.read_text())
        config["_path"] = str(config_path)
        config["_workspace"] = str(workspace)
        return config

    @staticmethod
    def status(config):
        request = urllib.request.Request(
            "http://" + config["listen"] + "/v1/status",
            headers={"Authorization": "Bearer " + config["localToken"]},
        )
        with urllib.request.urlopen(request, timeout=2) as response:
            return json.load(response)

    def start(self, config):
        log_path = self.root / (pathlib.Path(config["_path"]).stem + "-daemon.log")
        log = open(log_path, "a")
        process = subprocess.Popen(
            [self.args.binary, "serve", "--config", config["_path"]],
            cwd=config["_workspace"], env=self.environment, stdout=log, stderr=log,
        )
        self.processes.append((process, log))
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            try:
                if self.status(config):
                    return process
            except (OSError, urllib.error.URLError):
                pass
            if process.poll() is not None:
                raise AssertionError("fixture daemon exited before becoming ready")
            time.sleep(0.05)
        raise AssertionError("fixture daemon did not become ready")

    @staticmethod
    def stop(process):
        if process.poll() is None:
            process.send_signal(signal.SIGINT)
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
                raise AssertionError("fixture daemon required forced shutdown")

    def start_proxy(self, team):
        experiment = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def forward(self):
                if self.command == "PUT" and experiment.block_publication:
                    self.send_error(503, "fixture publication is unavailable")
                    return
                length = int(self.headers.get("Content-Length", "0"))
                body = self.rfile.read(length) if length else None
                headers = {key: value for key, value in self.headers.items() if key.lower() not in ("host", "connection")}
                request = urllib.request.Request(
                    "http://" + team["listen"] + self.path,
                    data=body, method=self.command, headers=headers,
                )
                try:
                    response = urllib.request.urlopen(request, timeout=2)
                except urllib.error.HTTPError as error:
                    response = error
                except urllib.error.URLError:
                    self.send_error(503, "fixture Team is offline")
                    return
                with response:
                    payload = response.read()
                    self.send_response(response.status)
                    for key, value in response.headers.items():
                        if key.lower() not in ("connection", "transfer-encoding", "server", "date"):
                            self.send_header(key, value)
                    self.end_headers()
                    if self.command != "HEAD":
                        self.wfile.write(payload)

            do_GET = forward
            do_HEAD = forward
            do_PUT = forward
            do_POST = forward

        self.proxy = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.proxy.daemon_threads = True
        self.proxy_url = "http://127.0.0.1:" + str(self.proxy.server_port)
        threading.Thread(target=self.proxy.serve_forever, daemon=True).start()

    def executions(self):
        path = self.root / "executions.jsonl"
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def run(self, name, config, workspace, executions, tasks=("build", "test"),
            profile="profile-one", expected=0, sync=True, ready=True, cache=None,
            source="one", dependency="dependency-one"):
        before = len(self.executions())
        started = time.monotonic()
        command = [self.args.binary, "run", "--config", config["_path"]]
        if sync:
            command.extend(["--sync-team", "--sync-timeout", "1s"])
        command.extend(["--", self.args.turbo, "run", *tasks, "--summarize"])
        if cache:
            command.append("--cache=" + cache)
        result = self.execute(command, workspace, expected, profile)
        summaries = list((workspace / ".turbo/runs").glob("*.json"))
        if not summaries:
            raise AssertionError(name + " produced no real Turbo summary")
        summary = json.loads(max(summaries, key=lambda path: path.stat().st_mtime_ns).read_text())
        output = result.stdout + result.stderr
        scenario = {
            "name": name, "exitCode": result.returncode,
            "compatibility": config["compatibilityId"],
            "elapsedSeconds": round(time.monotonic() - started, 3),
            "executedTasks": len(self.executions()) - before,
            "teamReady": READY in output, "pendingUploads": self.status(config).get("pendingUploads"),
            "stdout": result.stdout, "stderr": result.stderr,
            "tasks": [{"taskId": task["taskId"], "hash": task["hash"], "cache": task["cache"],
                       "execution": task.get("execution")} for task in summary["tasks"]],
            "outputs": {},
        }
        self.evidence["scenarios"].append(scenario)
        run = re.search(r"^Layer Cache run: (run-\S+)$", result.stderr, re.MULTILINE)
        if not run:
            raise AssertionError(name + " did not identify its measured LayerCache run")
        report = self.execute([
            self.args.binary, "report", "--config", config["_path"],
            "--run", run.group(1), "--json",
        ], workspace, expected=0 if expected == 0 else None)
        # Failed tasks are ineligible for reusable-result measurement. A run
        # consisting only of failed tasks can therefore have no report.
        scenario["report"] = json.loads(report.stdout) if report.returncode == 0 else {"unavailable": True}
        if scenario["executedTasks"] != executions:
            raise AssertionError(f"{name} executed {scenario['executedTasks']} tasks, expected {executions}")
        if ready and READY not in output:
            raise AssertionError(name + " did not confirm the successful task hashes in Team")
        if not ready and "Team Cache ready:" in output:
            raise AssertionError(name + " falsely claimed Team readiness")
        if sync and not ready and expected == 0 and "Team Cache publication unconfirmed:" not in output:
            raise AssertionError(name + " omitted the publication warning")
        if executions == 0:
            if not all(task["cache"]["status"] == "HIT" and task["cache"]["remote"] for task in summary["tasks"]):
                raise AssertionError(name + " did not restore all tasks through LayerCache")
        elif not all(task["cache"]["status"] == "MISS" for task in summary["tasks"]):
            raise AssertionError(name + " did not report misses for executed tasks")
        for task in tasks:
            path = workspace / ("packages/app/.test/result.json" if task == "test" else "packages/app/dist/result.json")
            if expected != 0:
                if path.exists():
                    raise AssertionError(name + " produced a passing test receipt after failure")
                continue
            receipt = json.loads(path.read_text())
            wanted = {"task": task, "source": source, "dependency": dependency, "profile": profile, "passed": True}
            if receipt != wanted:
                raise AssertionError(name + " restored different output: " + json.dumps(receipt))
            scenario["outputs"][task] = {"receipt": receipt, "sha256": self.digest(path)}
        return scenario

    @staticmethod
    def task_hashes(scenario):
        return {task["taskId"]: task["hash"] for task in scenario["tasks"]}

    @staticmethod
    def source(scenario, expected):
        hits = [outcome for outcome in scenario["report"].get("outcomes", []) if outcome.get("result") == "hit"]
        if len(hits) != 2 or any(outcome.get("source") != expected for outcome in hits):
            raise AssertionError(scenario["name"] + " did not measure both hits from " + expected)

    def fresh(self, name, team, **inputs):
        workspace = self.clone(name, **inputs)
        config = self.setup(name + "-runtime", workspace, team)
        self.start(config)
        return workspace, config

    @staticmethod
    def archive(config, task_hash):
        request = urllib.request.Request(
            "http://" + config["listen"] + "/v8/artifacts/" + task_hash,
            headers={
                "Authorization": "Bearer " + config["localToken"],
                "x-layercache-compatibility": config["compatibilityId"],
            },
        )
        with urllib.request.urlopen(request, timeout=2) as response:
            payload = response.read()
        return {"sha256": hashlib.sha256(payload).hexdigest(), "bytes": len(payload)}

    def exercise(self):
        self.evidence["turboVersion"] = self.execute([self.args.turbo, "--version"]).stdout.strip()
        self.evidence["nodeVersion"] = self.execute(["node", "--version"]).stdout.strip()
        self.evidence["pnpmVersion"] = self.execute(["pnpm", "--version"]).stdout.strip()
        if self.evidence["turboVersion"] != "2.10.12":
            raise AssertionError("acceptance requires the pinned Turbo 2.10.12 client")
        if self.evidence["pnpmVersion"] != "10.34.5":
            raise AssertionError("acceptance requires the pinned pnpm 10.34.5 client")
        self.template()
        producer = self.clone("producer")
        team = self.setup("team", producer, role="team")
        team_process = self.start(team)
        self.start_proxy(team)
        producer_config = self.setup("producer-runtime", producer, team)
        self.start(producer_config)
        cold = self.run("local-cold-published", producer_config, producer, 2)
        baseline = self.task_hashes(cold)
        for path in (producer / "packages/app/dist", producer / "packages/app/.test"):
            shutil.rmtree(path)
        warm = self.run("local-warm-restored", producer_config, producer, 0)
        self.source(warm, "localCache")
        consumer, consumer_config = self.fresh("fresh-ci", team)
        hit = self.run("fresh-ci-read-only-restored", consumer_config, consumer, 0, cache="local:,remote:r")
        self.source(hit, "teamCache")
        if self.task_hashes(hit) != baseline:
            raise AssertionError("fresh consumer did not request the producer's task hashes")
        main, main_config = self.fresh("main-recheck", team)
        self.run("main-fresh-no-task-cache-reads-or-writes", main_config, main, 2,
                 sync=False, ready=False, cache="local:,remote:")
        if self.status(main_config)["entries"] != 0:
            raise AssertionError("main's read-disabled policy wrote task artifacts")
        for name, inputs, profile in [
            ("source-change", {"source": "two"}, "profile-one"),
            ("declared-dependency-change", {"dependency": "dependency-two"}, "profile-one"),
            ("declared-environment-change", {}, "profile-two"),
        ]:
            workspace, config = self.fresh(name, team, **inputs)
            miss = self.run(name, config, workspace, 2, profile=profile,
                            source=inputs.get("source", "one"), dependency=inputs.get("dependency", "dependency-one"))
            if any(self.task_hashes(miss)[key] == value for key, value in baseline.items()):
                raise AssertionError(name + " retained an unchanged task hash")
        compatible = self.clone("compatibility-change")
        config = self.setup("compatibility-runtime", compatible, team,
                            compatibility="linux-amd64-local-ci-reuse-other-schema1")
        self.start(config)
        miss = self.run("compatibility-namespace-change", config, compatible, 2)
        if self.task_hashes(miss) != baseline:
            raise AssertionError("compatibility test changed native task hashes instead of only the daemon namespace")
        failing, failing_config = self.fresh("failing-test", team, source="broken", fail=True)
        for name in ("failed-test-first", "failed-test-repeated"):
            self.run(name, failing_config, failing, 1, tasks=("test",), expected=7, ready=False)
        failing, failing_config = self.fresh("failing-test-fresh", team, source="broken", fail=True)
        self.run("failed-test-fresh-consumer", failing_config, failing, 1,
                 tasks=("test",), expected=7, ready=False)
        changed_logs, changed_logs_config = self.fresh("same-hash-different-logs", team)
        rerun = self.run("same-hash-log-difference-confirms-existing-result", changed_logs_config,
                         changed_logs, 2, cache="local:,remote:w")
        if self.task_hashes(rerun) != baseline:
            raise AssertionError("log difference test changed task hashes")
        for task in ("build", "test"):
            if rerun["outputs"][task]["sha256"] != cold["outputs"][task]["sha256"]:
                raise AssertionError("same-hash rerun changed declared task output bytes")
        rerun["archives"] = {}
        for task_id, task_hash in baseline.items():
            local = self.archive(changed_logs_config, task_hash)
            remote_config = team | {"compatibilityId": changed_logs_config["compatibilityId"]}
            remote = self.archive(remote_config, task_hash)
            rerun["archives"][task_id] = {"local": local, "team": remote}
            if local["sha256"] == remote["sha256"]:
                raise AssertionError("same-hash log difference did not produce different opaque archives")
        deadline = time.monotonic() + 10
        while self.status(changed_logs_config).get("pendingUploads") != 0 and time.monotonic() < deadline:
            time.sleep(0.05)
        rerun["pendingUploads"] = self.status(changed_logs_config).get("pendingUploads")
        if rerun["pendingUploads"] != 0:
            raise AssertionError("verified immutable archive conflicts retained pending uploads")
        self.block_publication = True
        self.input(producer, source="unrelated")
        queued = self.run("unrelated-publication-pending", producer_config, producer, 2,
                          ready=False, source="unrelated")
        if not queued["pendingUploads"]:
            raise AssertionError("publication block did not queue the unrelated results")
        self.input(producer)
        available = self.run("matching-results-ready-despite-unrelated-queue", producer_config, producer, 0)
        if not available["pendingUploads"]:
            raise AssertionError("unrelated publication stopped being blocked during confirmation")
        self.block_publication = False
        offline, offline_config = self.fresh("offline-producer", team, source="offline")
        offline_process = self.processes[-1][0]
        self.stop(team_process)
        queued = self.run("offline-local-success-unconfirmed", offline_config, offline, 2,
                          ready=False, source="offline")
        if queued["pendingUploads"] != 2:
            raise AssertionError("offline run did not durably queue both successful tasks")
        local = self.run("offline-local-warm-still-usable", offline_config, offline, 0,
                         ready=False, source="offline")
        self.source(local, "localCache")
        self.stop(offline_process)
        self.start(team)
        self.start(offline_config)
        deadline = time.monotonic() + 15
        while self.status(offline_config).get("pendingUploads") != 0 and time.monotonic() < deadline:
            time.sleep(0.1)
        if self.status(offline_config).get("pendingUploads") != 0:
            raise AssertionError("restarted daemon did not finish queued publication")
        restored, restored_config = self.fresh("recovered-fresh-ci", team, source="offline")
        recovered = self.run("restarted-queue-result-restored-by-fresh-ci", restored_config, restored, 0, source="offline")
        self.source(recovered, "teamCache")
        self.evidence["offlineRecovery"] = {"pendingBeforeStop": 2, "pendingAfterRestart": 0}
        self.evidence["executions"] = self.executions()
        self.evidence["passed"] = True

    def close(self):
        errors = []
        for process, log in reversed(self.processes):
            try:
                self.stop(process)
            except Exception as error:
                errors.append(str(error))
            log.close()
        if self.proxy:
            self.proxy.shutdown()
            self.proxy.server_close()
        if not self.args.keep_workspace:
            shutil.rmtree(self.root)
        if errors:
            raise AssertionError("; ".join(errors))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, help="installed LayerCache candidate")
    parser.add_argument("--turbo", required=True, help="pinned real Turbo 2.10.12 executable")
    parser.add_argument("--output", help="write sanitized JSON evidence to this file")
    parser.add_argument("--keep-workspace", action="store_true", help="retain the isolated fixture directory for diagnosis")
    args = parser.parse_args()
    args.binary = str(pathlib.Path(args.binary).resolve())
    args.turbo = str(pathlib.Path(args.turbo).resolve())
    args.fixture = pathlib.Path(__file__).resolve().parents[1] / "fixtures/local-ci-reuse"
    experiment = Experiment(args)
    try:
        experiment.exercise()
    except Exception as error:
        experiment.evidence["error"] = str(error)
    finally:
        try:
            experiment.close()
        except Exception as error:
            experiment.evidence["passed"] = False
            experiment.evidence["cleanupError"] = str(error)
    document = json.dumps(experiment.evidence, indent=2) + "\n"
    if args.output:
        path = pathlib.Path(args.output)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(document)
        print(json.dumps({"passed": experiment.evidence["passed"], "scenarios": len(experiment.evidence["scenarios"]), "output": str(path)}))
    else:
        print(document, end="")
    if args.keep_workspace:
        print("Fixture directory: " + str(experiment.root), file=sys.stderr)
    if not experiment.evidence["passed"]:
        print(experiment.evidence.get("error", experiment.evidence.get("cleanupError", "acceptance failed")), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
