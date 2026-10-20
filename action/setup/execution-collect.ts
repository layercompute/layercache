import { createHash, randomBytes } from 'node:crypto';
import { constants } from 'node:fs';
import { open, stat, statfs } from 'node:fs/promises';
import { availableParallelism, cpus, totalmem } from 'node:os';
import { join } from 'node:path';

type Knowledge = 'complete' | 'partial' | 'unknown';

interface FilesystemMetrics {
  device: string;
  capacityBytes: number | null;
  freeBytes: number | null;
  availableBytes: number | null;
}
interface MetricDependencies {
  platform?: string;
  architecture?: string;
  visibleCPU?: () => number;
  affinityCPU?: () => number;
  visibleMemory?: () => number;
  read?: (path: string, limit: number) => Promise<string | null>;
  filesystem?: (path: string) => Promise<FilesystemMetrics>;
  salt?: string;
}
export interface ExecutionSample {
  os: 'linux' | 'darwin' | 'windows' | 'other';
  arch: 'amd64' | 'arm64' | 'other';
  cpu: {
    visibleLogical: number | null;
    affinityLogical: number | null;
    quotaMilliCPU: number | null;
    effectiveMilliCPU: number | null;
    limitKnowledge: Knowledge;
  };
  memory: {
    visibleTotalBytes: number | null;
    cgroupLimitBytes: number | null;
    effectiveLimitBytes: number | null;
    limitKnowledge: Knowledge;
  };
  filesystems: Array<{
    id: string;
    roles: Array<'workspace' | 'staging'>;
    capacityBytes: number | null;
    freeBytes: number | null;
    availableBytes: number | null;
  }>;
}

function integer(value: unknown, allowZero = false): number | null {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= (allowZero ? 0 : 1) ? value : null;
}
function probe(operation: () => number): number | null {
  try { return integer(operation()); } catch { return null; }
}
async function readMetric(path: string, limit: number): Promise<string | null> {
  let file;
  try {
    file = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW);
    if (!(await file.stat()).isFile()) return null;
    const buffer = Buffer.alloc(limit + 1);
    let length = 0;
    while (length < buffer.length) {
      const result = await file.read(buffer, length, buffer.length - length, null);
      if (!result.bytesRead) break;
      length += result.bytesRead;
    }
    return length <= limit ? buffer.toString('utf8', 0, length) : null;
  } catch { return null; }
  finally { await file?.close().catch(() => {}); }
}
async function filesystemMetrics(path: string): Promise<FilesystemMetrics> {
  const [metadata, volume] = await Promise.all([stat(path, { bigint: true }), statfs(path, { bigint: true })]);
  const bytes = (blocks: bigint, allowZero = false) => integer(Number(blocks * volume.bsize), allowZero);
  return {
    device: metadata.dev.toString(),
    capacityBytes: bytes(volume.blocks),
    freeBytes: bytes(volume.bfree, true),
    availableBytes: bytes(volume.bavail, true),
  };
}

function cgroupDirectories(source: string | null): string[] {
  const path = source?.split('\n').find(line => line.startsWith('0::'))?.slice(3);
  if (!path || !path.startsWith('/') || path.length > 4096 || /[\\\x00-\x1f\x7f]/.test(path)) {
    return ['/sys/fs/cgroup'];
  }
  const parts = path.split('/').filter(Boolean);
  if (parts.some(part => part === '.' || part === '..' || part.length > 255)) {
    return ['/sys/fs/cgroup'];
  }
  const paths: string[] = [];
  for (let index = parts.length; index >= 0 && paths.length < 16; index--) {
    paths.push(join('/sys/fs/cgroup', ...parts.slice(0, index)));
  }
  return paths;
}
function boundedNumber(value: string): number | null {
  if (!/^[0-9]{1,20}$/.test(value)) return null;
  return integer(Number(value));
}
function quota(source: string | null): { known: boolean; value: number | null } {
  const fields = source?.trim().split(/\s+/);
  if (fields?.length !== 2 || boundedNumber(fields[1] || '') === null) return { known: false, value: null };
  if (fields[0] === 'max') return { known: true, value: null };
  const numerator = boundedNumber(fields[0] || '');
  const denominator = boundedNumber(fields[1] || '');
  const value = numerator !== null && denominator !== null ? integer(Math.floor(numerator / denominator * 1000)) : null;
  return { known: value !== null, value };
}
function memoryLimit(source: string | null): { known: boolean; value: number | null } {
  const text = source?.trim();
  if (text === 'max') return { known: true, value: null };
  const value = boundedNumber(text || '');
  return { known: value !== null, value };
}

// Resource counters are caller-reported observations. Missing limits remain
// unknown; they do not imply access to every visible host CPU or memory byte.
export async function collectExecutionSample(env: NodeJS.ProcessEnv, dependencies: MetricDependencies = {}): Promise<ExecutionSample> {
  const platform = dependencies.platform ?? process.platform;
  const architecture = dependencies.architecture ?? process.arch;
  const visibleLogical = probe(dependencies.visibleCPU ?? (() => cpus().length));
  const affinityLogical = probe(dependencies.affinityCPU ?? availableParallelism);
  const visibleTotalBytes = probe(dependencies.visibleMemory ?? totalmem);
  const read = dependencies.read ?? readMetric;
  let quotaMilliCPU: number | null = null;
  let cgroupLimitBytes: number | null = null;
  let cpuLimitObserved = false, memoryLimitObserved = false;
  if (platform === 'linux') {
    const directories = cgroupDirectories(await read('/proc/self/cgroup', 16 * 1024).catch(() => null));
    for (const directory of directories) {
      const cpu = quota(await read(join(directory, 'cpu.max'), 4096).catch(() => null));
      const memory = memoryLimit(await read(join(directory, 'memory.max'), 4096).catch(() => null));
      cpuLimitObserved ||= cpu.known;
      memoryLimitObserved ||= memory.known;
      if (cpu.value !== null) quotaMilliCPU = Math.min(quotaMilliCPU ?? cpu.value, cpu.value);
      if (memory.value !== null) cgroupLimitBytes = Math.min(cgroupLimitBytes ?? memory.value, memory.value);
    }
  }
  const filesystem = dependencies.filesystem ?? filesystemMetrics;
  const salt = dependencies.salt ?? randomBytes(32).toString('hex');
  const volumes = new Map<string, ExecutionSample['filesystems'][number]>();
  for (const [path, role] of [[env.GITHUB_WORKSPACE, 'workspace'], [env.RUNNER_TEMP, 'staging']] as const) {
    if (!path) continue;
    try {
      const facts = await filesystem(path);
      const existing = volumes.get(facts.device);
      if (existing) { if (!existing.roles.includes(role)) existing.roles.push(role); continue; }
      volumes.set(facts.device, {
        id: createHash('sha256').update(salt).update('\0').update(facts.device).digest('hex'),
        roles: [role],
        capacityBytes: integer(facts.capacityBytes),
        freeBytes: integer(facts.freeBytes, true),
        availableBytes: integer(facts.availableBytes, true),
      });
    } catch { /* An unavailable volume supplies no counter or identity. */ }
  }
  return {
    os: platform === 'linux' || platform === 'darwin' ? platform : platform === 'win32' ? 'windows' : 'other',
    arch: architecture === 'x64' ? 'amd64' : architecture === 'arm64' ? 'arm64' : 'other',
    cpu: {
      visibleLogical, affinityLogical, quotaMilliCPU,
      // A readable cgroup namespace root does not establish that tighter
      // outer limits are absent. Do not report an inferred effective budget.
      effectiveMilliCPU: null,
      limitKnowledge: visibleLogical !== null || affinityLogical !== null || cpuLimitObserved ? 'partial' : 'unknown',
    },
    memory: {
      visibleTotalBytes, cgroupLimitBytes,
      effectiveLimitBytes: null,
      limitKnowledge: visibleTotalBytes !== null || memoryLimitObserved ? 'partial' : 'unknown',
    },
    filesystems: [...volumes.values()],
  };
}
