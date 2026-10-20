import { constants } from 'node:fs';
import { open } from 'node:fs/promises';

export const xcodeReportFormat = 'xcode27-replay-v1';
const maximumLogBytes = 64 << 20, maximumLineBytes = 1 << 20, maximumLines = 250000;
const maximumStatsBytes = 16 << 10, maximumElapsedMs = 24 * 60 * 60 * 1000;

export type XcodeCompilerCounters = {
  scope: 'clang-pch' | 'swift' | 'mixed' | 'unknown';
  coverage: 'complete' | 'partial' | 'unsupported' | 'unknown';
  reason: 'missing' | 'truncated' | 'unsupported' | 'ambiguous' | 'none';
  hits: number | null; misses: number | null; skipped: number | null; unknown: number | null;
};
export type XcodeRPCCounters = {
  actionReads: number | null; actionHits: number | null; actionWrites: number | null;
  casReads: number | null; casHits: number | null; casWrites: number | null; cacheErrors: number | null;
};
export type XcodePhaseTiming = {
  operation: number | null; compile: number | null; load: number | null;
  restore: number | null; close: number | null; snapshot: number | null;
};
type LogInput = string | AsyncIterable<Uint8Array>;
type Limits = {bytes?: number; lineBytes?: number; lines?: number};
export type NativeXcodeReport = {
  version: 1; invocationId: string; startedAt: string; finishedAt: string;
  status: 'success' | 'failed' | 'cancelled' | 'unknown'; tasks: [];
  counters: {compiler: XcodeCompilerCounters; rpc?: XcodeRPCCounters}; timingMs: XcodePhaseTiming;
};
export type XcodeReportInput = {
  invocationId: string; startedAt: string; finishedAt: string; status: NativeXcodeReport['status'];
  format: string; log?: LogInput; serviceStats?: string; timingMs?: Partial<XcodePhaseTiming>; limits?: Limits;
};

function unknownCompiler(reason: XcodeCompilerCounters['reason'], coverage: XcodeCompilerCounters['coverage'] = 'unknown'): XcodeCompilerCounters {
  return {scope: 'unknown', coverage, reason, hits: null, misses: null, skipped: null, unknown: null};
}
function bounded(value: number | undefined, fallback: number): number {
  if (value === undefined) return fallback;
  if (!Number.isSafeInteger(value) || value < 1 || value > fallback) throw new Error('Invalid Xcode reporting limits');
  return value;
}

// Only terminal Swift Build replay diagnostics are observations. Bare results,
// detached key lookups and local lookup misses are deliberately ignored. Swift
// headers may describe virtual per-file batch subtasks, so they never supply a
// compiler-job denominator. See docs/xcode-metering-source-notes.md.
export async function parseXcodeCompilerLog(log: LogInput | undefined, format: string, limits: Limits = {}): Promise<XcodeCompilerCounters> {
  if (format !== xcodeReportFormat) return unknownCompiler('unsupported', 'unsupported');
  if (log === undefined) return unknownCompiler('missing');
  const byteLimit = bounded(limits.bytes, maximumLogBytes), lineLimit = bounded(limits.lineBytes, maximumLineBytes), linesLimit = bounded(limits.lines, maximumLines);
  type Operation = {kind: 'clang-pch' | 'swift'; outcome?: 'hit' | 'miss'; diagnostic?: string; ambiguous: boolean; unsupported: boolean};
  let operation: Operation | undefined;
  let clang = false, swift = false, finished = false, ambiguous = false, unsupported = false, truncated = false;
  let hits = 0, misses = 0, unknown = 0, observed = 0;
  const finishOperation = () => {
    if (!operation) return;
    if (operation.ambiguous || operation.unsupported) {
      if (operation.kind === 'clang-pch') unknown++;
      ambiguous ||= operation.ambiguous; unsupported ||= operation.unsupported;
    } else if (operation.outcome) {
      observed++;
      if (operation.outcome === 'hit') hits++; else misses++;
    } else if (operation.kind === 'clang-pch') unknown++;
    operation = undefined;
  };
  const consume = (line: string) => {
    if (/^\*\* (?:BUILD|TEST|ARCHIVE) (?:SUCCEEDED|FAILED) \*\*$/.test(line)) {finishOperation(); finished = true; return;}
    // The same replay is also rendered as this bare result line. It is not
    // another job, and it must not reset the current task's context.
    if (/^Cache (?:hit|miss)$/.test(line) || /^(?:note|remark): (?:[Ll]ocal cache miss for key:|cache key query (?:hit|miss)\b|cache (?:hit|miss)$)/.test(line)) return;
    const kind = /^(?:CompileC|ProcessPCH|ProcessPCH\+\+)\s/.test(line) ? 'clang-pch'
      : /^(?:SwiftCompile|SwiftEmitModule|SwiftDriver\sCompilation(?:\s|$)|SwiftDriver\sCompilation\sRequirements(?:\s|$))/.test(line) ? 'swift' : undefined;
    if (kind) {
      finishOperation();
      if (finished) ambiguous = true;
      finished = false;
      if (kind === 'clang-pch') clang = true; else swift = true;
      operation = {kind, ambiguous: false, unsupported: false};
      return;
    }
    if (/^[A-Z][A-Za-z0-9+]*\s/.test(line)) {finishOperation(); return;}
    const clangFinal = /^(?:note|remark): (replayed cache hit|cache miss): (\S{1,8192})$/.exec(line);
    const swiftFinal = /^(?:note|remark): [Rr]eplay cache (hit|miss)$/.exec(line);
    if (clangFinal || swiftFinal) {
      const expected = clangFinal ? 'clang-pch' : 'swift';
      if (!operation || operation.kind !== expected) {ambiguous = true; return;}
      const outcome = clangFinal ? clangFinal[1] === 'replayed cache hit' ? 'hit' : 'miss' : swiftFinal![1] === 'hit' ? 'hit' : 'miss';
      if (operation.diagnostic !== undefined) {
        // Two terminal messages in one unstructured task block cannot prove
        // two native jobs. Suppress this block instead of inflating its rate.
        operation.ambiguous = true;
      } else {operation.diagnostic = line; operation.outcome = outcome;}
      return;
    }
    if (/^(?:note|remark): .*cache (?:hit|miss|skipped)\b/i.test(line)) {
      if (operation) operation.unsupported = true; else unsupported = true;
    }
  };
  const chunks: AsyncIterable<Uint8Array> = typeof log === 'string' ? (async function* () {
    for (let start = 0; start < log.length;) {
      let end = Math.min(start + (64 << 10), log.length);
      if (end < log.length && /[\uD800-\uDBFF]/.test(log[end - 1]!)) end--;
      yield Buffer.from(log.slice(start, end), 'utf8'); start = end;
    }
  })() : log;
  const decoder = new TextDecoder('utf-8', {fatal: true});
  let bytes = 0, lineBytes = 0, lines = 0, pending = '';
  try {
    outer: for await (const chunk of chunks) {
      if (!(chunk instanceof Uint8Array)) {unsupported = true; break;}
      const remaining = byteLimit - bytes;
      const boundedChunk = chunk.subarray(0, remaining);
      bytes += boundedChunk.byteLength;
      pending += decoder.decode(boundedChunk, {stream: true});
      let newline: number;
      while ((newline = pending.indexOf('\n')) >= 0) {
        const line = pending.slice(0, newline).replace(/\r$/, '');
        if (++lines > linesLimit || Buffer.byteLength(line, 'utf8') > lineLimit) {truncated = true; break outer;}
        if (/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/.test(line)) {unsupported = true; break outer;}
        consume(line); pending = pending.slice(newline + 1);
      }
      lineBytes = Buffer.byteLength(pending, 'utf8');
      if (lineBytes > lineLimit || chunk.byteLength > remaining) {truncated = true; break;}
    }
    if (!truncated && !unsupported) {
      pending += decoder.decode();
      if (pending !== '') {
        if (++lines > linesLimit || Buffer.byteLength(pending, 'utf8') > lineLimit) truncated = true;
        else if (/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/.test(pending)) unsupported = true;
        else consume(pending.replace(/\r$/, ''));
      }
    }
  } catch {unsupported = true;}
  finishOperation();
  const scope = clang && swift ? 'mixed' : clang ? 'clang-pch' : swift ? 'swift' : 'unknown';
  if (scope === 'unknown') return unknownCompiler(truncated ? 'truncated' : unsupported ? 'unsupported' : ambiguous ? 'ambiguous' : 'missing', unsupported ? 'unsupported' : 'unknown');
  const complete = finished && !truncated && !unsupported && !ambiguous && !swift && unknown === 0 && observed > 0;
  const reason = truncated ? 'truncated' : unsupported ? 'unsupported' : ambiguous || swift ? 'ambiguous' : !finished || unknown > 0 ? 'missing' : 'none';
  return {scope, coverage: complete ? 'complete' : 'partial', reason, hits, misses, skipped: null, unknown: swift ? null : unknown};
}

const rpcFields = ['actionReads', 'actionHits', 'actionWrites', 'casReads', 'casHits', 'casWrites', 'cacheErrors'] as const;
const extraStatsFields = new Set(['actionLookupMisses', 'actionValueRejects', 'actionReferenceRejects', 'graphObjectMisses', 'graphObjectCorrupt', 'graphReferenceRejects', 'graphBudgetRejects', 'graphAdmissionRejects', 'graphDeadlineRejects', 'graphChecks', 'graphObjectsChecked', 'graphLookupNanos', 'graphLeaseMisses']);

// The service writes one flat object of integer counters and two optional
// fallback booleans. Parse that exact shape, rejecting duplicate names, nested
// objects, unknown fields, unsafe numbers and escaped-key tricks before upload.
export function parseXcodeRPCStats(text: string): XcodeRPCCounters {
  if (Buffer.byteLength(text, 'utf8') > maximumStatsBytes) throw new Error('Invalid Xcode service statistics');
  let remaining = text.trim();
  if (!remaining.startsWith('{')) throw new Error('Invalid Xcode service statistics');
  remaining = remaining.slice(1);
  const values = new Map<string, number | boolean>();
  while (true) {
    if (/^\s*}\s*$/.test(remaining)) {remaining = ''; break;}
    const field = /^\s*"([A-Za-z][A-Za-z0-9]*)"\s*:\s*(0|[1-9][0-9]*|true|false)\s*([,}])/.exec(remaining);
    if (!field || values.has(field[1]!)) throw new Error('Invalid Xcode service statistics');
    const name = field[1]!, fallback = name === 'uncachedFallback' || name === 'uncachedRetry';
    if (!fallback && !rpcFields.some(key => key === name) && !extraStatsFields.has(name)) throw new Error('Invalid Xcode service statistics');
    const value = field[2] === 'true' ? true : field[2] === 'false' ? false : Number(field[2]);
    if (fallback ? typeof value !== 'boolean' : typeof value !== 'number' || !Number.isSafeInteger(value)) throw new Error('Invalid Xcode service statistics');
    values.set(name, value); remaining = remaining.slice(field[0].length);
    if (field[3] === '}') {if (remaining.trim() !== '') throw new Error('Invalid Xcode service statistics'); break;}
    if (/^\s*}/.test(remaining)) throw new Error('Invalid Xcode service statistics');
  }
  const result: XcodeRPCCounters = {actionReads: null, actionHits: null, actionWrites: null, casReads: null, casHits: null, casWrites: null, cacheErrors: null};
  for (const key of rpcFields) {
    const value = values.get(key);
    if (typeof value !== 'number') throw new Error('Invalid Xcode service statistics');
    result[key] = value;
  }
  if (result.actionHits! > result.actionReads! || result.casHits! > result.casReads!) throw new Error('Invalid Xcode service statistics');
  return result;
}

const phaseFields = ['operation', 'compile', 'load', 'restore', 'close', 'snapshot'] as const;
function parseTime(text: string): bigint {
  const match = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/.exec(text);
  if (!match) throw new Error('Invalid Xcode reporting window');
  const utc = new Date(match[1] + 'Z');
  if (!Number.isFinite(utc.getTime()) || utc.toISOString().slice(0, 19) !== match[1]) throw new Error('Invalid Xcode reporting window');
  let offset = 0;
  if (match[3] !== 'Z') {
    const hours = Number(match[3]!.slice(1, 3)), minutes = Number(match[3]!.slice(4));
    if (hours > 23 || minutes > 59) throw new Error('Invalid Xcode reporting window');
    offset = (hours * 60 + minutes) * (match[3]![0] === '+' ? 1 : -1);
  }
  return BigInt(utc.getTime() - offset * 60000) * 1000000n + BigInt((match[2] ?? '').padEnd(9, '0'));
}

export async function collectXcodeReport(input: XcodeReportInput): Promise<NativeXcodeReport> {
  const start = parseTime(input.startedAt), finish = parseTime(input.finishedAt);
  if (!/^run-[A-Za-z0-9_-]{12,124}$/.test(input.invocationId) || !['success', 'failed', 'cancelled', 'unknown'].includes(input.status) || finish < start || finish - start > BigInt(maximumElapsedMs) * 1000000n) throw new Error('Invalid Xcode reporting window');
  const timingMs: XcodePhaseTiming = {operation: null, compile: null, load: null, restore: null, close: null, snapshot: null};
  for (const key of Object.keys(input.timingMs ?? {})) if (!phaseFields.some(field => field === key)) throw new Error('Invalid Xcode reporting timing');
  for (const key of phaseFields) {
    const value = input.timingMs?.[key];
    if (value !== undefined && value !== null && (!Number.isSafeInteger(value) || value < 0 || value > maximumElapsedMs)) throw new Error('Invalid Xcode reporting timing');
    timingMs[key] = value ?? null;
  }
  const compiler = await parseXcodeCompilerLog(input.log, input.format, input.limits);
  const rpc = input.serviceStats === undefined ? undefined : parseXcodeRPCStats(input.serviceStats);
  return {version: 1, invocationId: input.invocationId, startedAt: input.startedAt, finishedAt: input.finishedAt, status: input.status, tasks: [], counters: {compiler, ...(rpc ? {rpc} : {})}, timingMs};
}

async function readStatsFile(path: string): Promise<string> {
  let file;
  try {
    file = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
    const info = await file.stat();
    if (!info.isFile() || info.size > maximumStatsBytes) throw new Error();
    const bytes = Buffer.alloc(maximumStatsBytes + 1);
    let length = 0;
    while (length < bytes.length) {const read = await file.read(bytes, length, bytes.length - length); if (read.bytesRead === 0) break; length += read.bytesRead;}
    if (length > maximumStatsBytes) throw new Error();
    return new TextDecoder('utf-8', {fatal: true}).decode(bytes.subarray(0, length));
  } catch {throw new Error('Xcode service statistics unavailable');}
  finally {await file?.close();}
}
async function* readLogFile(path: string): AsyncGenerator<Uint8Array> {
  let file;
  try {
    file = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
    if (!(await file.stat()).isFile()) throw new Error();
    const stream = file.createReadStream({autoClose: false, highWaterMark: 64 << 10});
    try {for await (const chunk of stream) yield chunk;} finally {stream.destroy();}
  } catch {throw new Error('Xcode compiler log unavailable');}
  finally {await file?.close();}
}

// This export command opens only explicit local inputs. It never reads ambient
// LayerCache credentials, opens an artifact store or sends a network request.
export async function exportXcodeReport(args: string[]): Promise<void> {
  const allowed = ['--format', '--log-file', '--stats-file', '--invocation-id', '--started-at', '--finished-at', '--status', '--operation-ms', '--compile-ms', '--load-ms', '--restore-ms', '--close-ms', '--snapshot-ms', '--output'];
  const options = new Map<string, string>();
  for (let index = 0; index < args.length; index += 2) {
    const key = args[index], value = args[index + 1];
    if (!key || !allowed.includes(key) || !value || options.has(key)) throw new Error('Invalid Xcode report arguments');
    options.set(key, value);
  }
  for (const key of ['--format', '--invocation-id', '--started-at', '--finished-at', '--status', '--output']) if (!options.has(key)) throw new Error('Missing required Xcode report arguments');
  const timingMs: Partial<XcodePhaseTiming> = {};
  for (const key of phaseFields) {
    const value = options.get(`--${key}-ms`);
    if (value !== undefined) {
      if (!/^(?:0|[1-9][0-9]{0,8})$/.test(value)) throw new Error('Invalid Xcode reporting timing');
      timingMs[key] = Number(value);
    }
  }
  const log = options.get('--log-file'), stats = options.get('--stats-file');
  const status = options.get('--status');
  if (status !== 'success' && status !== 'failed' && status !== 'cancelled' && status !== 'unknown') throw new Error('Invalid Xcode report status');
  const report = await collectXcodeReport({format: options.get('--format')!, invocationId: options.get('--invocation-id')!, startedAt: options.get('--started-at')!, finishedAt: options.get('--finished-at')!, status, timingMs,
    ...(log ? {log: readLogFile(log)} : {}), ...(stats ? {serviceStats: await readStatsFile(stats)} : {})});
  let output;
  try {
    output = await open(options.get('--output')!, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
    await output.writeFile(JSON.stringify(report) + '\n'); await output.sync();
  } catch {throw new Error('Xcode report output unavailable');}
  finally {await output?.close();}
}
