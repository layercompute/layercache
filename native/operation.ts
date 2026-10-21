import { createHash, randomBytes } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { constants } from 'node:fs';
import type { Stats } from 'node:fs';
import { lstat, open, readFile, realpath, rename, unlink, link } from 'node:fs/promises';
import type { FileHandle } from 'node:fs/promises';
import { hostname } from 'node:os';
import { dirname, isAbsolute, join, resolve } from 'node:path';
import { parse } from 'secure-json-parse';

const phases = ['setup', 'restore', 'work', 'close', 'publish'] as const;
type Phase = typeof phases[number];
const preparations = ['hit', 'miss', 'mixed', 'unavailable', 'bypassed', 'unknown'] as const;
type Preparation = typeof preparations[number];
type Status = 'success' | 'failed' | 'cancelled' | 'unknown';
const limitMs = 86400000, fileLimit = 32 * 1024, transitionLimit = 128;
const digestPattern = /^sha256:[a-f0-9]{64}$/, receiptPattern = /^[a-f0-9]{64}$/;
const idPattern = /^run-[A-Za-z0-9_-]{12,124}$/;
const errorMessage = 'Operation measurement unavailable: invalid, changed or unsafe private measurement state';
const hash = (value: string) => createHash('sha256').update(value).digest('hex');
const own = (value: object, key: string) => Object.prototype.hasOwnProperty.call(value, key);
const plain = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value);
function keys(value: Record<string, unknown>, allowed: readonly string[]): void {
  if (Object.keys(value).some(key => !allowed.includes(key))) throw new Error();
}
function optionalDigest(value: unknown): value is string | undefined {return value === undefined || typeof value === 'string' && digestPattern.test(value);}
function integer(value: unknown, max = limitMs): value is number {return Number.isSafeInteger(value) && Number(value) >= 0 && Number(value) <= max;}
function ns(value: unknown): bigint {if (typeof value !== 'string' || !/^(0|[1-9][0-9]{0,24})$/.test(value)) throw new Error(); return BigInt(value);}

export interface OperationContext {
  version?: 1; project: string; integration: 'gradle' | 'xcode'; compatibility: string; sourceCommit?: string;
  sessionId?: string; workloadDigest: string; coverageDigest: string; environmentDigest?: string; otherCachesDigest?: string;
  outputState: 'fresh' | 'incremental' | 'unknown'; cachePolicy: 'enabled' | 'bypass' | 'unknown';
  cachePreparation?: Preparation; baselineReceiptId?: string;
}
export interface OperationReport {
  version: 1; operationId: string; sessionId?: string; workloadDigest: string; coverageDigest: string;
  environmentDigest?: string; otherCachesDigest?: string; outputState: OperationContext['outputState'];
  cachePolicy: OperationContext['cachePolicy']; cachePreparation: Preparation; baselineReceiptId?: string;
  startedAt: string; finishedAt: string; elapsedMs: number; status: Status;
  phaseTimingMs: Record<Phase, number | null>;
}
export interface OperationClock {sample(): Promise<{wallMs: number; monotonicNs: bigint; binding: string}>;}
const systemClock: OperationClock = {
  async sample() {
    let boot: string;
    if (process.platform === 'linux') boot = await readFile('/proc/sys/kernel/random/boot_id', 'utf8');
    else if (process.platform === 'darwin') boot = execFileSync('/usr/sbin/sysctl', ['-n', 'kern.bootsessionuuid'], {encoding: 'utf8', timeout: 2000, maxBuffer: 1024, stdio: ['ignore', 'pipe', 'ignore']});
    else throw new Error();
    if (!/^[a-f0-9-]{36}\s*$/i.test(boot)) throw new Error();
    // Private only. A report never contains the boot token, hostname or binding.
    return {wallMs: Date.now(), monotonicNs: process.hrtime.bigint(), binding: hash(`${process.platform}\0${hostname()}\0${boot.trim()}`)};
  },
};
interface State {
  version: 1; operationId: string; contextPath: string; contextSha256: string; context: OperationContext;
  binding: string; startedWallMs: number; lastWallMs: number; startedNs: string; lastNs: string;
  activePhase: Phase; phaseNs: Record<Phase, string | null>; transitions: number; preparation: Preparation;
  report?: OperationReport; reportSha256?: string;
}
function context(value: unknown): OperationContext {
  if (!plain(value)) throw new Error();
  keys(value, ['version', 'project', 'integration', 'compatibility', 'sourceCommit', 'sessionId', 'workloadDigest', 'coverageDigest', 'environmentDigest', 'otherCachesDigest', 'outputState', 'cachePolicy', 'cachePreparation', 'baselineReceiptId']);
  if (value.version !== undefined && value.version !== 1 || typeof value.project !== 'string' || value.project.length < 1 || value.project.length > 256 || /[\p{White_Space}\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/u.test(value.project) ||
      !['gradle', 'xcode'].includes(String(value.integration)) || typeof value.compatibility !== 'string' || !/^[a-z0-9][a-z0-9_.:+@-]{0,255}$/.test(value.compatibility) ||
      value.sourceCommit !== undefined && (typeof value.sourceCommit !== 'string' || !/^(?:[a-f0-9]{40}|[a-f0-9]{64})$/.test(value.sourceCommit)) ||
      value.sessionId !== undefined && (typeof value.sessionId !== 'string' || !idPattern.test(value.sessionId)) ||
      typeof value.workloadDigest !== 'string' || !digestPattern.test(value.workloadDigest) || typeof value.coverageDigest !== 'string' || !digestPattern.test(value.coverageDigest) ||
      !optionalDigest(value.environmentDigest) || !optionalDigest(value.otherCachesDigest) || !['fresh', 'incremental', 'unknown'].includes(String(value.outputState)) ||
      !['enabled', 'bypass', 'unknown'].includes(String(value.cachePolicy)) || value.cachePreparation !== undefined && !preparations.includes(value.cachePreparation as Preparation) ||
      value.baselineReceiptId !== undefined && (typeof value.baselineReceiptId !== 'string' || !receiptPattern.test(value.baselineReceiptId))) throw new Error();
  return value as unknown as OperationContext;
}
function sameInfo(a: Stats, b: Stats): boolean {return a.dev === b.dev && a.ino === b.ino && a.size === b.size && a.mtimeMs === b.mtimeMs && a.ctimeMs === b.ctimeMs;}
function privateFile(info: Stats): boolean {return process.geteuid !== undefined && info.isFile() && info.uid === process.geteuid() && (info.mode & 0o7777) === 0o600 && info.nlink === 1 && info.size > 0 && info.size <= fileLimit;}
async function trustedPath(path: string): Promise<string> {
  if (!isAbsolute(path) || path !== resolve(path)) throw new Error();
  const parent = dirname(path), owner = process.geteuid?.();
  if (owner === undefined || await realpath(parent) !== parent) throw new Error();
  let current = parent;
  while (true) {
    const info = await lstat(current);
    if (!info.isDirectory() || ![0, owner].includes(info.uid)) throw new Error();
    if (current === parent && (info.uid !== owner || (info.mode & 0o7777) !== 0o700)) throw new Error();
    if ((info.mode & 0o022) !== 0 && !(info.uid === 0 && (info.mode & 0o1000) !== 0)) throw new Error();
    if (dirname(current) === current) break;
    current = dirname(current);
  }
  return path;
}
async function readPrivate(path: string): Promise<{bytes: string; info: Stats}> {
  await trustedPath(path);
  let file: FileHandle | undefined;
  try {
    file = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
    const before = await file.stat(); if (!privateFile(before)) throw new Error();
    const buffer = Buffer.alloc(fileLimit + 1); let length = 0;
    while (length < buffer.length) {const read = await file.read(buffer, length, buffer.length - length, null); if (!read.bytesRead) break; length += read.bytesRead;}
    const after = await file.stat();
    if (length !== before.size || length > fileLimit || !privateFile(after) || !sameInfo(before, after) || !sameInfo(after, await lstat(path))) throw new Error();
    return {bytes: buffer.toString('utf8', 0, length), info: after};
  } finally {await file?.close();}
}
async function writePrivate(path: string, bytes: string, previous?: {bytes: string; info: Stats}): Promise<void> {
  await trustedPath(path);
  if (Buffer.byteLength(bytes) > fileLimit) throw new Error();
  const temporary = join(dirname(path), `.measurement-${randomBytes(16).toString('hex')}`);
  let file: FileHandle | undefined;
  try {
    file = await open(temporary, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
    await file.writeFile(bytes); await file.sync(); await file.close(); file = undefined;
    if (previous) {
      const current = await readPrivate(path);
      if (!sameInfo(previous.info, current.info) || previous.bytes !== current.bytes) throw new Error();
      await rename(temporary, path);
    } else {
      // link() supplies exclusive creation without replacing a caller's file.
      await link(temporary, path); await unlink(temporary);
    }
    const directory = await open(dirname(path), constants.O_RDONLY);
    try {await directory.sync();} finally {await directory.close();}
  } finally {await file?.close(); await unlink(temporary).catch(error => {if (error.code !== 'ENOENT') throw error;});}
}
async function locked<T>(path: string, action: () => Promise<T>): Promise<T> {
  await trustedPath(path);
  const lockPath = path + '.lock'; let lock: FileHandle | undefined; let identity: Stats | undefined;
  try {
    lock = await open(lockPath, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
    await lock.writeFile(randomBytes(16).toString('hex')); identity = await lock.stat();
    return await action();
  } finally {
    await lock?.close();
    if (identity) {const current = await lstat(lockPath); if (current.dev !== identity.dev || current.ino !== identity.ino) throw new Error(); await unlink(lockPath);}
  }
}
function stateBytes(value: State): string {return JSON.stringify({version: 1, state: value, sha256: hash(JSON.stringify(value))}) + '\n';}
function readState(bytes: string): State {
  const envelope: unknown = parse(bytes, undefined, {protoAction: 'error', constructorAction: 'error'});
  if (!plain(envelope)) throw new Error(); keys(envelope, ['version', 'state', 'sha256']);
  if (envelope.version !== 1 || !plain(envelope.state) || envelope.sha256 !== hash(JSON.stringify(envelope.state))) throw new Error();
  const s = envelope.state; keys(s, ['version', 'operationId', 'contextPath', 'contextSha256', 'context', 'binding', 'startedWallMs', 'lastWallMs', 'startedNs', 'lastNs', 'activePhase', 'phaseNs', 'transitions', 'preparation', 'report', 'reportSha256']);
  context(s.context);
  if (s.version !== 1 || typeof s.operationId !== 'string' || !idPattern.test(s.operationId) || typeof s.contextPath !== 'string' || typeof s.contextSha256 !== 'string' || !receiptPattern.test(s.contextSha256) ||
      typeof s.binding !== 'string' || !receiptPattern.test(s.binding) || !integer(s.startedWallMs, Number.MAX_SAFE_INTEGER) || !integer(s.lastWallMs, Number.MAX_SAFE_INTEGER) ||
      !integer(s.transitions, transitionLimit) || !phases.includes(s.activePhase as Phase) || !preparations.includes(s.preparation as Preparation) || !plain(s.phaseNs)) throw new Error();
  keys(s.phaseNs, phases);
  const elapsed = ns(s.lastNs) - ns(s.startedNs);
  if (elapsed < 0n || elapsed > BigInt(limitMs) * 1000000n || s.lastWallMs < s.startedWallMs || s.lastWallMs - s.startedWallMs > limitMs || Math.abs(s.lastWallMs - s.startedWallMs - Number(elapsed / 1000000n)) > 5000) throw new Error();
  let total = 0n;
  for (const phase of phases) {if (!own(s.phaseNs, phase)) throw new Error(); if (s.phaseNs[phase] !== null) total += ns(s.phaseNs[phase]);}
  if (total !== elapsed || s.phaseNs[s.activePhase as Phase] === null) throw new Error();
  if (s.report !== undefined && (!plain(s.report) || !['success', 'failed', 'cancelled', 'unknown'].includes(String(s.report.status)) || s.reportSha256 !== hash(JSON.stringify(s.report)))) throw new Error();
  if (s.report === undefined && s.reportSha256 !== undefined) throw new Error();
  return s as unknown as State;
}
async function loaded(path: string, clock: OperationClock): Promise<{state: State; previous: {bytes: string; info: Stats}; now: Awaited<ReturnType<OperationClock['sample']>>}> {
  const previous = await readPrivate(path), state = readState(previous.bytes), source = await readPrivate(state.contextPath);
  if (hash(source.bytes) !== state.contextSha256 || JSON.stringify(context(parse(source.bytes, undefined, {protoAction: 'error', constructorAction: 'error'}))) !== JSON.stringify(state.context)) throw new Error();
  const now = await clock.sample();
  if (now.binding !== state.binding || !receiptPattern.test(now.binding) || !integer(now.wallMs, Number.MAX_SAFE_INTEGER) || now.wallMs < state.lastWallMs || now.monotonicNs < ns(state.lastNs)) throw new Error();
  return {state, previous, now};
}
function advance(state: State, now: Awaited<ReturnType<OperationClock['sample']>>): void {
  const elapsedNs = now.monotonicNs - ns(state.startedNs), stepNs = now.monotonicNs - ns(state.lastNs);
  if (elapsedNs < 0n || elapsedNs > BigInt(limitMs) * 1000000n || now.wallMs < state.lastWallMs || now.wallMs - state.startedWallMs > limitMs ||
      Math.abs(now.wallMs - state.startedWallMs - Number(elapsedNs / 1000000n)) > 5000 || Math.abs(now.wallMs - state.lastWallMs - Number(stepNs / 1000000n)) > 5000) throw new Error();
  state.phaseNs[state.activePhase] = String(ns(state.phaseNs[state.activePhase]) + stepNs);
  state.lastNs = String(now.monotonicNs); state.lastWallMs = now.wallMs;
}
function preparation(value: unknown): Preparation {if (!preparations.includes(value as Preparation)) throw new Error(); return value as Preparation;}
function reportFor(state: State, status: Status): OperationReport {
  const c = state.context;
  return {version: 1, operationId: state.operationId, ...(c.sessionId ? {sessionId: c.sessionId} : {}), workloadDigest: c.workloadDigest, coverageDigest: c.coverageDigest,
    ...(c.environmentDigest ? {environmentDigest: c.environmentDigest} : {}), ...(c.otherCachesDigest ? {otherCachesDigest: c.otherCachesDigest} : {}),
    outputState: c.outputState, cachePolicy: c.cachePolicy, cachePreparation: state.preparation, ...(c.baselineReceiptId ? {baselineReceiptId: c.baselineReceiptId} : {}),
    startedAt: new Date(state.startedWallMs).toISOString(), finishedAt: new Date(state.lastWallMs).toISOString(), elapsedMs: Number((ns(state.lastNs) - ns(state.startedNs)) / 1000000n), status,
    phaseTimingMs: Object.fromEntries(phases.map(phase => [phase, state.phaseNs[phase] === null ? null : Number(ns(state.phaseNs[phase]) / 1000000n)])) as OperationReport['phaseTimingMs']};
}
export async function startOperation(contextPath: string, statePath: string, clock: OperationClock = systemClock): Promise<string> {
  try {return await locked(statePath, async () => {
    if (contextPath === statePath || contextPath === statePath + '.lock') throw new Error();
    const source = await readPrivate(contextPath), declared = context(parse(source.bytes, undefined, {protoAction: 'error', constructorAction: 'error'})), now = await clock.sample();
    if (!receiptPattern.test(now.binding) || !integer(now.wallMs, Number.MAX_SAFE_INTEGER) || now.monotonicNs < 0n) throw new Error();
    const operationId = 'run-' + randomBytes(18).toString('base64url');
    await writePrivate(statePath, stateBytes({version: 1, operationId, contextPath, contextSha256: hash(source.bytes), context: declared, binding: now.binding, startedWallMs: now.wallMs,
      lastWallMs: now.wallMs, startedNs: String(now.monotonicNs), lastNs: String(now.monotonicNs), activePhase: 'setup', phaseNs: {setup: '0', restore: null, work: null, close: null, publish: null}, transitions: 0, preparation: declared.cachePreparation ?? 'unknown'}));
    return operationId;
  });} catch {throw new Error(errorMessage);}
}
export async function phaseOperation(statePath: string, phase: Phase, cachePreparation?: Preparation, clock: OperationClock = systemClock): Promise<string> {
  try {return await locked(statePath, async () => {
    if (!phases.includes(phase)) throw new Error();
    const {state, previous, now} = await loaded(statePath, clock);
    if (state.report || state.transitions >= transitionLimit) throw new Error();
    advance(state, now); state.activePhase = phase; state.phaseNs[phase] ??= '0'; state.transitions++;
    if (cachePreparation !== undefined) state.preparation = preparation(cachePreparation);
    await writePrivate(statePath, stateBytes(state), previous); return state.operationId;
  });} catch {throw new Error(errorMessage);}
}
export async function finishOperation(statePath: string, status: Status, outputPath: string, cachePreparation?: Preparation, clock: OperationClock = systemClock): Promise<OperationReport> {
  try {return await locked(statePath, async () => {
    if (!['success', 'failed', 'cancelled', 'unknown'].includes(status) || outputPath === statePath || outputPath === statePath + '.lock') throw new Error();
    const {state, previous, now} = await loaded(statePath, clock);
    if (outputPath === state.contextPath) throw new Error();
    if (state.report) {
      if (state.report.status !== status || cachePreparation !== undefined && state.preparation !== preparation(cachePreparation) || JSON.stringify(reportFor(state, status)) !== JSON.stringify(state.report)) throw new Error();
    } else {
      advance(state, now); if (cachePreparation !== undefined) state.preparation = preparation(cachePreparation);
      state.report = reportFor(state, status); state.reportSha256 = hash(JSON.stringify(state.report));
      await writePrivate(statePath, stateBytes(state), previous);
    }
    const bytes = JSON.stringify(state.report) + '\n';
    try {await writePrivate(outputPath, bytes);} catch (error) {
      // A failed output write can be retried with the already sealed bytes.
      const output = await readPrivate(outputPath); if (output.bytes !== bytes) throw error;
    }
    return state.report;
  });} catch {throw new Error(errorMessage);}
}

export interface LocalOperationComparison {
  selection: 'explicitLocalPair'; localBaselineReportSHA256: string; localCandidateReportSHA256: string;
  method: 'bypassToEnabled' | 'coldToWarm' | 'unmatched'; matched: boolean; reasons: string[];
  baselineElapsedMs: number; candidateElapsedMs: number; observedDifferenceMs: number | null;
  observedWorkDifferenceMs: number | null; observedOverheadDifferenceMs: number | null; assumptions: string[];
}
export async function compareOperations(baselinePath: string, candidatePath: string, clock: OperationClock = systemClock): Promise<LocalOperationComparison> {
  try {
    const baseline = (await loaded(baselinePath, clock)).state, candidate = (await loaded(candidatePath, clock)).state;
    if (!baseline.report || !candidate.report || JSON.stringify(reportFor(baseline, baseline.report.status)) !== JSON.stringify(baseline.report) || JSON.stringify(reportFor(candidate, candidate.report.status)) !== JSON.stringify(candidate.report)) throw new Error();
    const a = baseline.context, b = candidate.context, x = baseline.report, y = candidate.report, reasons: string[] = [];
    if (a.project !== b.project) reasons.push('projectMismatch');
    if (a.integration !== b.integration) reasons.push('integrationMismatch');
    if (a.compatibility !== b.compatibility) reasons.push('profileMismatch');
    if (!a.sourceCommit || !b.sourceCommit) reasons.push('sourceMissing'); else if (a.sourceCommit !== b.sourceCommit) reasons.push('sourceMismatch');
    if (x.status !== 'success' || y.status !== 'success') reasons.push('unsuccessfulOperation');
    if (!x.sessionId || !y.sessionId) reasons.push('sessionMissing'); else if (x.sessionId !== y.sessionId) reasons.push('sessionMismatch');
    if (x.workloadDigest !== y.workloadDigest) reasons.push('workloadMismatch');
    if (x.coverageDigest !== y.coverageDigest) reasons.push('coverageMismatch');
    if (!x.environmentDigest || !y.environmentDigest) reasons.push('environmentMissing'); else if (x.environmentDigest !== y.environmentDigest) reasons.push('environmentMismatch');
    if (!x.otherCachesDigest || !y.otherCachesDigest) reasons.push('otherCachesMissing'); else if (x.otherCachesDigest !== y.otherCachesDigest) reasons.push('otherCachesMismatch');
    if (x.outputState !== 'fresh' || y.outputState !== 'fresh') reasons.push('outputNotFresh');
    if (Date.parse(x.finishedAt) > Date.parse(y.startedAt) || x.operationId === y.operationId) reasons.push('operationOrderMismatch');
    if (Date.parse(y.startedAt) - Date.parse(x.finishedAt) > limitMs) reasons.push('comparisonWindowExceeded');
    let method: LocalOperationComparison['method'] = x.cachePolicy === 'bypass' && x.cachePreparation === 'bypassed' && y.cachePolicy === 'enabled' ? 'bypassToEnabled' :
      x.cachePolicy === 'enabled' && x.cachePreparation === 'miss' && y.cachePolicy === 'enabled' && ['hit', 'mixed'].includes(y.cachePreparation) ? 'coldToWarm' : 'unmatched';
    if (method === 'unmatched') reasons.push('cachePolicyMismatch');
    if (reasons.length) method = 'unmatched';
    const fullPhases = (report: OperationReport) => phases.every(phase => report.phaseTimingMs[phase] !== null) && report.elapsedMs - phases.reduce((sum, phase) => sum + report.phaseTimingMs[phase]!, 0) <= 5;
    const complete = fullPhases(x) && fullPhases(y);
    const overhead = (report: OperationReport) => phases.filter(phase => phase !== 'work').reduce((sum, phase) => sum + report.phaseTimingMs[phase]!, 0);
    return {selection: 'explicitLocalPair', localBaselineReportSHA256: baseline.reportSha256!, localCandidateReportSHA256: candidate.reportSha256!, method, matched: reasons.length === 0, reasons,
      baselineElapsedMs: x.elapsedMs, candidateElapsedMs: y.elapsedMs, observedDifferenceMs: reasons.length ? null : x.elapsedMs - y.elapsedMs,
      observedWorkDifferenceMs: reasons.length || x.phaseTimingMs.work === null || y.phaseTimingMs.work === null ? null : x.phaseTimingMs.work - y.phaseTimingMs.work,
      observedOverheadDifferenceMs: reasons.length || !complete ? null : overhead(x) - overhead(y),
      assumptions: ['callerDeclaredContext', 'sameSessionWarmthMayDiffer', 'observedNotCausal', 'cpuAndCostUnknown']};
  } catch {throw new Error(errorMessage);}
}

export async function measureOperationCLI(operation: string, args: string[]): Promise<void> {
  const allowed: Record<string, string[]> = {'measure-start': ['--context-file', '--state-file'], 'measure-phase': ['--state-file', '--phase', '--cache-preparation'],
    'measure-finish': ['--state-file', '--status', '--output', '--cache-preparation'], 'measure-compare': ['--baseline-state', '--candidate-state', '--json']};
  const flags = allowed[operation]; if (!flags) throw new Error('Invalid operation measurement arguments');
  const values = new Map<string, string>();
  for (let index = 0; index < args.length; index++) {
    const flag = args[index]; if (!flag || !flags.includes(flag) || values.has(flag)) throw new Error('Invalid operation measurement arguments');
    if (flag === '--json') {values.set(flag, 'true'); continue;}
    const value = args[++index]; if (!value || value.startsWith('--')) throw new Error('Invalid operation measurement arguments'); values.set(flag, value);
  }
  const required = (flag: string) => {const value = values.get(flag); if (!value) throw new Error('Invalid operation measurement arguments'); return value;};
  if (operation === 'measure-start') console.log(JSON.stringify({version: 1, operationId: await startOperation(required('--context-file'), required('--state-file')), state: 'open'}));
  else if (operation === 'measure-phase') console.log(JSON.stringify({version: 1, operationId: await phaseOperation(required('--state-file'), required('--phase') as Phase, values.get('--cache-preparation') as Preparation | undefined), state: 'open'}));
  else if (operation === 'measure-finish') {const report = await finishOperation(required('--state-file'), required('--status') as Status, required('--output'), values.get('--cache-preparation') as Preparation | undefined); console.log(JSON.stringify({version: 1, operationId: report.operationId, state: 'sealed'}));}
  else {const result = await compareOperations(required('--baseline-state'), required('--candidate-state')); console.log(values.has('--json') ? JSON.stringify(result) : result.matched ? `Observed operation difference: ${result.observedDifferenceMs} ms (${result.method}); CPU and cost unknown` : `Operation comparison unavailable: ${result.reasons.join(', ')}`);}
}
