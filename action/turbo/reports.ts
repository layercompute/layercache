import {
  lstat,
  readdir,
  realpath,
  open,
  constants,
  type FileHandle,
} from 'node:fs/promises';
import { resolve, relative, join, isAbsolute } from 'node:path';

export interface ReportState {
  endpoint: string;
  project: string;
  compatibility: string;
  workspace: string;
  directory: string;
  existing: string[];
  startedAt: number;
}
// Native summaries include complete input maps and environment metadata that
// are discarded below. Match the CLI's raw document limit without increasing
// the server's 8 MiB request limit or retaining those fields in the projection.
const maximumRawSummaryBytes = 64 * 1024 ** 2;
const maximumRawBatchBytes = 64 * 1024 ** 2;
const maximumRequestBytes = 8 * 1024 ** 2;
const maximumTasks = 4096;

interface SummaryReader {
  openSummary?: (path: string, flags: number) => Promise<FileHandle>;
}
export function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value))
    throw new Error('Invalid object');
  return value as Record<string, unknown>;
}
function inside(root: string, path: string) {
  const part = relative(root, path);
  if (part === '..' || part.startsWith('../') || isAbsolute(part))
    throw new Error('Summary directory escapes workspace');
}
async function files(directory: string): Promise<string[]> {
  try {
    return (await readdir(directory)).filter((name) => name.endsWith('.json'));
  } catch (error) {
    if (error instanceof Error && 'code' in error && error.code === 'ENOENT')
      return [];
    throw error;
  }
}
export async function startReport(
  workspace: string,
  workingDirectory: string,
  identity: Pick<ReportState, 'endpoint' | 'project' | 'compatibility'>,
): Promise<ReportState> {
  const root = await realpath(workspace),
    directory = await realpath(resolve(root, workingDirectory));
  inside(root, directory);
  const existing = await files(join(directory, '.turbo/runs'));
  if (existing.length > 4096) throw new Error('Too many existing summaries');
  return {
    ...identity,
    workspace: root,
    directory,
    existing,
    startedAt: Date.now(),
  };
}

export async function completedSummaries(
  state: ReportState,
  { openSummary = open }: SummaryReader = {},
): Promise<Record<string, unknown>[]> {
  const root = await realpath(state.workspace);
  const directory = await realpath(state.directory);
  inside(root, directory);
  const runs = join(directory, '.turbo/runs');
  const names = await files(runs);
  if (!names.length) return [];
  inside(root, await realpath(runs));
  const old = new Set(state.existing),
    fresh = names.filter((name) => !old.has(name)).sort();
  if (fresh.length > 32) throw new Error('Too many new summaries');
  const summaries: Record<string, unknown>[] = [];
  let rawMetadataBytes = 0;
  // Reject an oversized batch before parsing its first potentially large file.
  for (const name of fresh) {
    const path = join(runs, name),
      metadata = await lstat(path);
    if (!metadata.isFile() || metadata.isSymbolicLink())
      throw new Error('Unsafe summary file');
    if (metadata.size > maximumRawSummaryBytes)
      throw new Error('Raw summary byte limit exceeded');
    rawMetadataBytes += metadata.size;
    if (rawMetadataBytes > maximumRawBatchBytes)
      throw new Error('Raw summary batch byte limit exceeded');
  }
  let rawBytes = 0;
  let tasks = 0;
  // Include the exact JSON envelope and commas used by post.ts.
  let requestBytes = Buffer.byteLength('{"summaries":[]}');
  for (const name of fresh) {
    const file = await openSummary(
      join(runs, name),
      constants.O_RDONLY | constants.O_NOFOLLOW,
    );
    let source: Record<string, unknown>;
    try {
      const actual = await file.stat();
      if (!actual.isFile()) throw new Error('Unsafe summary file');
      if (actual.size > maximumRawSummaryBytes)
        throw new Error('Raw summary byte limit exceeded');
      if (rawBytes + actual.size > maximumRawBatchBytes)
        throw new Error('Raw summary batch byte limit exceeded');
      const chunks: Buffer[] = [];
      let length = 0;
      for (;;) {
        // Read one extra byte at either limit to detect growth after stat.
        const buffer = Buffer.alloc(
          Math.min(
            64 * 1024,
            maximumRawSummaryBytes - length + 1,
            maximumRawBatchBytes - rawBytes + 1,
          ),
        );
        const read = await file.read(buffer, 0, buffer.length, null);
        if (read.bytesRead === 0) break;
        length += read.bytesRead;
        rawBytes += read.bytesRead;
        if (length > maximumRawSummaryBytes)
          throw new Error('Raw summary byte limit exceeded');
        if (rawBytes > maximumRawBatchBytes)
          throw new Error('Raw summary batch byte limit exceeded');
        chunks.push(buffer.subarray(0, read.bytesRead));
      }
      source = object(
        JSON.parse(Buffer.concat(chunks, length).toString('utf8')),
      );
    } finally {
      await file.close();
    }
    const execution = object(source.execution);
    if (
      typeof execution.startTime !== 'number' ||
      execution.startTime < state.startedAt - 5000
    )
      throw new Error('Summary predates setup');
    if (!Array.isArray(source.tasks)) throw new Error('Invalid tasks');
    tasks += source.tasks.length;
    if (tasks > maximumTasks) throw new Error('Summary task limit exceeded');
    // Turbo summaries can contain environment and command details. Send only
    // the fields required for measurement, never the full original document.
    const summary = {
      id: source.id,
      version: source.version,
      execution: { startTime: execution.startTime, endTime: execution.endTime },
      tasks: source.tasks.map((value) => {
        const task = object(value),
          cache = object(task.cache),
          execution = object(task.execution),
          definition = object(task.resolvedTaskDefinition);
        return {
          taskId: task.taskId,
          hash: task.hash,
          dependencies: task.dependencies,
          cache: {
            local: cache.local,
            remote: cache.remote,
            status: cache.status,
            source: cache.source,
            timeSaved: cache.timeSaved,
          },
          execution: {
            startTime: execution.startTime,
            endTime: execution.endTime,
            exitCode: execution.exitCode,
          },
          resolvedTaskDefinition: { cache: definition.cache },
        };
      }),
    };
    requestBytes +=
      Buffer.byteLength(JSON.stringify(summary)) + (summaries.length ? 1 : 0);
    if (requestBytes > maximumRequestBytes)
      throw new Error('Summary request byte limit exceeded');
    summaries.push(summary);
  }
  return summaries;
}
