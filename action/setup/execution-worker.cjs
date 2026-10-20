"use strict";

// action/setup/execution-collect.ts
var import_node_crypto = require("node:crypto");
var import_node_fs = require("node:fs");
var import_promises = require("node:fs/promises");
var import_node_os = require("node:os");
var import_node_path = require("node:path");
function integer(value, allowZero = false) {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= (allowZero ? 0 : 1) ? value : null;
}
function probe(operation) {
  try {
    return integer(operation());
  } catch {
    return null;
  }
}
async function readMetric(path, limit) {
  let file;
  try {
    file = await (0, import_promises.open)(path, import_node_fs.constants.O_RDONLY | import_node_fs.constants.O_NOFOLLOW);
    if (!(await file.stat()).isFile()) return null;
    const buffer = Buffer.alloc(limit + 1);
    let length = 0;
    while (length < buffer.length) {
      const result = await file.read(buffer, length, buffer.length - length, null);
      if (!result.bytesRead) break;
      length += result.bytesRead;
    }
    return length <= limit ? buffer.toString("utf8", 0, length) : null;
  } catch {
    return null;
  } finally {
    await file?.close().catch(() => {
    });
  }
}
async function filesystemMetrics(path) {
  const [metadata, volume] = await Promise.all([(0, import_promises.stat)(path, { bigint: true }), (0, import_promises.statfs)(path, { bigint: true })]);
  const bytes = (blocks, allowZero = false) => integer(Number(blocks * volume.bsize), allowZero);
  return {
    device: metadata.dev.toString(),
    capacityBytes: bytes(volume.blocks),
    freeBytes: bytes(volume.bfree, true),
    availableBytes: bytes(volume.bavail, true)
  };
}
function cgroupDirectories(source) {
  const path = source?.split("\n").find((line) => line.startsWith("0::"))?.slice(3);
  if (!path || !path.startsWith("/") || path.length > 4096 || /[\\\x00-\x1f\x7f]/.test(path)) {
    return ["/sys/fs/cgroup"];
  }
  const parts = path.split("/").filter(Boolean);
  if (parts.some((part) => part === "." || part === ".." || part.length > 255)) {
    return ["/sys/fs/cgroup"];
  }
  const paths = [];
  for (let index = parts.length; index >= 0 && paths.length < 16; index--) {
    paths.push((0, import_node_path.join)("/sys/fs/cgroup", ...parts.slice(0, index)));
  }
  return paths;
}
function boundedNumber(value) {
  if (!/^[0-9]{1,20}$/.test(value)) return null;
  return integer(Number(value));
}
function quota(source) {
  const fields = source?.trim().split(/\s+/);
  if (fields?.length !== 2 || boundedNumber(fields[1] || "") === null) return { known: false, value: null };
  if (fields[0] === "max") return { known: true, value: null };
  const numerator = boundedNumber(fields[0] || "");
  const denominator = boundedNumber(fields[1] || "");
  const value = numerator !== null && denominator !== null ? integer(Math.floor(numerator / denominator * 1e3)) : null;
  return { known: value !== null, value };
}
function memoryLimit(source) {
  const text = source?.trim();
  if (text === "max") return { known: true, value: null };
  const value = boundedNumber(text || "");
  return { known: value !== null, value };
}
async function collectExecutionSample(env, dependencies = {}) {
  const platform = dependencies.platform ?? process.platform;
  const architecture = dependencies.architecture ?? process.arch;
  const visibleLogical = probe(dependencies.visibleCPU ?? (() => (0, import_node_os.cpus)().length));
  const affinityLogical = probe(dependencies.affinityCPU ?? import_node_os.availableParallelism);
  const visibleTotalBytes = probe(dependencies.visibleMemory ?? import_node_os.totalmem);
  const read = dependencies.read ?? readMetric;
  let quotaMilliCPU = null;
  let cgroupLimitBytes = null;
  let cpuLimitObserved = false, memoryLimitObserved = false;
  if (platform === "linux") {
    const directories = cgroupDirectories(await read("/proc/self/cgroup", 16 * 1024).catch(() => null));
    for (const directory of directories) {
      const cpu = quota(await read((0, import_node_path.join)(directory, "cpu.max"), 4096).catch(() => null));
      const memory = memoryLimit(await read((0, import_node_path.join)(directory, "memory.max"), 4096).catch(() => null));
      cpuLimitObserved ||= cpu.known;
      memoryLimitObserved ||= memory.known;
      if (cpu.value !== null) quotaMilliCPU = Math.min(quotaMilliCPU ?? cpu.value, cpu.value);
      if (memory.value !== null) cgroupLimitBytes = Math.min(cgroupLimitBytes ?? memory.value, memory.value);
    }
  }
  const filesystem = dependencies.filesystem ?? filesystemMetrics;
  const salt = dependencies.salt ?? (0, import_node_crypto.randomBytes)(32).toString("hex");
  const volumes = /* @__PURE__ */ new Map();
  for (const [path, role] of [[env.GITHUB_WORKSPACE, "workspace"], [env.RUNNER_TEMP, "staging"]]) {
    if (!path) continue;
    try {
      const facts = await filesystem(path);
      const existing = volumes.get(facts.device);
      if (existing) {
        if (!existing.roles.includes(role)) existing.roles.push(role);
        continue;
      }
      volumes.set(facts.device, {
        id: (0, import_node_crypto.createHash)("sha256").update(salt).update("\0").update(facts.device).digest("hex"),
        roles: [role],
        capacityBytes: integer(facts.capacityBytes),
        freeBytes: integer(facts.freeBytes, true),
        availableBytes: integer(facts.availableBytes, true)
      });
    } catch {
    }
  }
  return {
    os: platform === "linux" || platform === "darwin" ? platform : platform === "win32" ? "windows" : "other",
    arch: architecture === "x64" ? "amd64" : architecture === "arm64" ? "arm64" : "other",
    cpu: {
      visibleLogical,
      affinityLogical,
      quotaMilliCPU,
      // A readable cgroup namespace root does not establish that tighter
      // outer limits are absent. Do not report an inferred effective budget.
      effectiveMilliCPU: null,
      limitKnowledge: visibleLogical !== null || affinityLogical !== null || cpuLimitObserved ? "partial" : "unknown"
    },
    memory: {
      visibleTotalBytes,
      cgroupLimitBytes,
      effectiveLimitBytes: null,
      limitKnowledge: visibleTotalBytes !== null || memoryLimitObserved ? "partial" : "unknown"
    },
    filesystems: [...volumes.values()]
  };
}

// action/setup/execution-worker.ts
async function main() {
  let source = "";
  for await (const chunk of process.stdin) {
    source += String(chunk);
    if (Buffer.byteLength(source) > 16 * 1024) throw new Error("Input limit");
  }
  const input = JSON.parse(source);
  const env = {};
  for (const key of ["GITHUB_WORKSPACE", "RUNNER_TEMP"]) {
    const value = input[key];
    if (typeof value === "string" && value.length <= 4096) env[key] = value;
  }
  const output = JSON.stringify(await collectExecutionSample(env));
  if (Buffer.byteLength(output) > 16 * 1024) throw new Error("Output limit");
  process.stdout.write(output, () => process.exit(0));
}
main().catch(() => process.exit(1));
