import { spawn } from 'node:child_process';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import type { ExecutionSample } from './execution-collect.ts';
export { collectExecutionSample } from './execution-collect.ts';
export type { ExecutionSample } from './execution-collect.ts';

const observationPath = '/v1/executions/observations';
const maximumObservationBytes = 16 * 1024;
type Phase = 'start' | 'finish';

// Direct TypeScript actions load the sibling worker from setup; bundled actions
// ship the same worker beside their entry point. Neither locator uses job input.
function executionWorkerPath(): string {
  const directory = typeof __dirname === 'string' ? __dirname : dirname(fileURLToPath(import.meta.url));
  return join(directory, 'execution-worker.cjs');
}

function sampleFromWorker(source: string): ExecutionSample {
  const value = JSON.parse(source) as ExecutionSample;
  const metric = (value: unknown, zero = false): number | null => {
    if (value === null) return null;
    if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < (zero ? 0 : 1)) throw new Error('Invalid metric');
    return value;
  };
  const knowledge = (value: unknown): 'complete' | 'partial' | 'unknown' => {
    if (value !== 'complete' && value !== 'partial' && value !== 'unknown') throw new Error('Invalid knowledge');
    return value;
  };
  if (!['linux', 'darwin', 'windows', 'other'].includes(value.os) || !['amd64', 'arm64', 'other'].includes(value.arch) ||
      !Array.isArray(value.filesystems) || value.filesystems.length > 4) throw new Error('Invalid sample');
  return {
    os: value.os, arch: value.arch,
    cpu: {
      visibleLogical: metric(value.cpu.visibleLogical), affinityLogical: metric(value.cpu.affinityLogical),
      quotaMilliCPU: metric(value.cpu.quotaMilliCPU), effectiveMilliCPU: metric(value.cpu.effectiveMilliCPU),
      limitKnowledge: knowledge(value.cpu.limitKnowledge),
    },
    memory: {
      visibleTotalBytes: metric(value.memory.visibleTotalBytes), cgroupLimitBytes: metric(value.memory.cgroupLimitBytes),
      effectiveLimitBytes: metric(value.memory.effectiveLimitBytes), limitKnowledge: knowledge(value.memory.limitKnowledge),
    },
    filesystems: value.filesystems.map(volume => {
      if (!/^[0-9a-f]{64}$/.test(volume.id) || !Array.isArray(volume.roles) || volume.roles.length < 1 || volume.roles.length > 2 ||
          volume.roles.some(role => role !== 'workspace' && role !== 'staging')) throw new Error('Invalid filesystem');
      return { id: volume.id, roles: [...new Set(volume.roles)], capacityBytes: metric(volume.capacityBytes),
        freeBytes: metric(volume.freeBytes, true), availableBytes: metric(volume.availableBytes, true) };
    }),
  };
}

// Filesystem probes can leave active libuv work after a Promise timeout. Keep
// them out of the action process, then close pipes and detach our killed child
// without waiting for a possibly blocked filesystem operation to finish.
export function collectExecutionSampleIsolated(env: NodeJS.ProcessEnv, worker = executionWorkerPath(), executable = process.execPath): Promise<ExecutionSample> {
  const input = JSON.stringify(Object.fromEntries(['GITHUB_WORKSPACE', 'RUNNER_TEMP']
    .flatMap(key => typeof env[key] === 'string' && env[key]!.length <= 4096 ? [[key, env[key]]] : [])));
  return new Promise((resolve, reject) => {
    const child = spawn(executable, [worker], { env: {}, stdio: ['pipe', 'pipe', 'ignore'], windowsHide: true });
    let output = '';
    let settled = false;
    const finish = (error?: Error): void => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      for (const cleanup of [() => child.kill('SIGKILL'), () => child.unref(), () => child.stdin?.destroy(), () => child.stdout?.destroy()]) {
        try { cleanup(); } catch { /* Spawn failures can leave no open handle. */ }
      }
      if (error) reject(error);
      else { try { resolve(sampleFromWorker(output)); } catch { reject(new Error('Invalid observation worker output')); } }
    };
    const timer = setTimeout(() => finish(new Error('Observation deadline')), 2000);
    child.on('error', () => finish(new Error('Observation worker unavailable')));
    child.stdin?.on('error', () => finish(new Error('Observation worker input unavailable')));
    child.stdout?.on('data', (chunk: Buffer) => {
      if (Buffer.byteLength(output) + chunk.length > maximumObservationBytes) finish(new Error('Observation worker output exceeds limit'));
      else output += chunk.toString('utf8');
    });
    child.on('close', code => finish(code === 0 ? undefined : new Error('Observation worker unavailable')));
    child.stdin?.end(input);
  });
}

// Discovery is opt-in. The server-provided path cannot select another origin,
// and a failed optional observation never changes native cache authentication.
export async function reportExecutionObservation({ endpoint, project, exchange, env = process.env, phase = 'start', fetcher = fetch, provider, collect = collectExecutionSampleIsolated }: {
  endpoint: string; project: string; exchange: unknown; env?: NodeJS.ProcessEnv; phase?: Phase; fetcher?: typeof fetch; provider?: string;
  collect?: (env: NodeJS.ProcessEnv) => Promise<ExecutionSample>;
}): Promise<void> {
  try {
    if (!/^[\x21-\x7e]{1,1024}$/.test(project) || !exchange || typeof exchange !== 'object' || Array.isArray(exchange)) return;
    const result = exchange as { teamToken?: unknown; executionObservations?: unknown };
    const discovery = result.executionObservations;
    if (!discovery || typeof discovery !== 'object' || Array.isArray(discovery)) return;
    const enabled = discovery as { version?: unknown; path?: unknown };
    if (enabled.version !== 1 || enabled.path !== observationPath ||
        typeof result.teamToken !== 'string' || !result.teamToken || result.teamToken.length > 32 * 1024 ||
        /[\r\n]/.test(result.teamToken)) return;
    const base = new URL(endpoint);
    if (base.username || base.password || base.search || base.hash ||
        (base.protocol !== 'https:' && !(base.protocol === 'http:' && ['localhost', '127.0.0.1', '[::1]'].includes(base.hostname)))) return;
    const target = new URL(observationPath, base);
    const declaredProvider = provider ?? env['INPUT_EXECUTION-PROVIDER'];
    const selectedProvider = ['github', 'ubicloud', 'selfHosted', 'unknown'].includes(declaredProvider ?? '') ? declaredProvider : 'unknown';
    let timer: ReturnType<typeof setTimeout> | undefined;
    let sample: ExecutionSample;
    try {
      sample = await Promise.race([
        collect(env),
        new Promise<never>((_, reject) => { timer = setTimeout(() => reject(new Error('Observation deadline')), 2000); }),
      ]);
    } finally { clearTimeout(timer); }
    const body = JSON.stringify({ version: 1, phase, provider: selectedProvider, sample });
    if (Buffer.byteLength(body) > maximumObservationBytes) return;
    const response = await fetcher(target, {
      method: 'POST', redirect: 'error',
      headers: { Authorization: `Bearer ${result.teamToken}`, 'Content-Type': 'application/json', 'X-LayerCache-Project': project },
      body, signal: AbortSignal.timeout(2000),
    });
    await response.body?.cancel();
  } catch { /* Resource reporting is optional and never exposes error bodies. */ }
}
