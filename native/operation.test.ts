import assert from 'node:assert/strict';
import test from 'node:test';
import { createHash } from 'node:crypto';
import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { chmod, link, mkdir, mkdtemp, readFile, readdir, realpath, rm, stat, symlink, truncate, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { compareOperations, finishOperation, phaseOperation, startOperation } from './operation.ts';
import type { OperationClock, OperationContext } from './operation.ts';

const sha = (text: string) => createHash('sha256').update(text).digest('hex');
const declared: OperationContext = {version: 1, project: 'example-project', integration: 'gradle', compatibility: 'example-toolchain-v1', sourceCommit: 'a'.repeat(40),
  sessionId: 'run-example-session-001', workloadDigest: 'sha256:' + '1'.repeat(64), coverageDigest: 'sha256:' + '2'.repeat(64),
  environmentDigest: 'sha256:' + '3'.repeat(64), otherCachesDigest: 'sha256:' + '4'.repeat(64), outputState: 'fresh', cachePolicy: 'enabled'};
class Clock implements OperationClock {
  wall = Date.UTC(2026, 9, 21); mono = 1000000000000n; binding = 'a'.repeat(64);
  async sample() {return {wallMs: this.wall, monotonicNs: this.mono, binding: this.binding};}
  tick(ms: number) {this.wall += ms; this.mono += BigInt(ms) * 1000000n;}
}
async function files(t: {after: (fn: () => Promise<void>) => void}, context: Partial<OperationContext> = {}) {
  const root = await realpath(await mkdtemp(join(tmpdir(), 'layercache-operation-')));
  t.after(async () => {await rm(root, {recursive: true, force: true});});
  const input = join(root, 'context.json'), state = join(root, 'state.json'), output = join(root, 'report.json');
  await writeFile(input, JSON.stringify({...declared, ...context}), {mode: 0o600});
  return {root, input, state, output};
}
const refusal = /Operation measurement unavailable/;

test('disjoint native operation phases include cache setup and updates, aggregate repeats and keep unobserved phases null', async t => {
  const f = await files(t), clock = new Clock();
  const id = await startOperation(f.input, f.state, clock); assert.match(id, /^run-[A-Za-z0-9_-]{24}$/);
  clock.tick(10); await phaseOperation(f.state, 'restore', undefined, clock);
  clock.tick(20); await phaseOperation(f.state, 'work', 'miss', clock);
  clock.tick(100); await phaseOperation(f.state, 'restore', undefined, clock);
  clock.tick(30); await phaseOperation(f.state, 'work', 'mixed', clock);
  clock.tick(40); await phaseOperation(f.state, 'close', undefined, clock);
  clock.tick(7); await phaseOperation(f.state, 'publish', undefined, clock);
  clock.tick(13); const report = await finishOperation(f.state, 'success', f.output, undefined, clock);
  assert.equal(report.elapsedMs, 220); assert.equal(report.cachePreparation, 'mixed');
  assert.deepEqual(report.phaseTimingMs, {setup: 10, restore: 50, work: 140, close: 7, publish: 13});
  assert.equal(report.operationId, id); assert.equal(report.startedAt, '2026-10-21T00:00:00.000Z'); assert.equal(report.finishedAt, '2026-10-21T00:00:00.220Z');
  assert.equal((await stat(f.state)).mode & 0o7777, 0o600); assert.equal((await stat(f.state)).nlink, 1);
  assert.equal((await stat(f.output)).mode & 0o7777, 0o600); assert.equal((await stat(f.output)).nlink, 1);
  const bytes = await readFile(f.output, 'utf8');
  assert.doesNotMatch(bytes, /example-project|toolchain|sourceCommit|contextPath|binding|hostname|monotonic|token|command|cpu|cost/i);
  assert.deepEqual(JSON.parse(bytes), report);
  const unknown = await files(t), c2 = new Clock(); await startOperation(unknown.input, unknown.state, c2); c2.tick(5);
  const partial = await finishOperation(unknown.state, 'unknown', unknown.output, undefined, c2);
  assert.deepEqual(partial.phaseTimingMs, {setup: 5, restore: null, work: null, close: null, publish: null});
  assert.equal(partial.cachePreparation, 'unknown');
});

test('finish seals once, preserves failure status, supports exact output repair and refuses changed retries', async t => {
  const f = await files(t), clock = new Clock(); await startOperation(f.input, f.state, clock);
  clock.tick(15); await phaseOperation(f.state, 'work', undefined, clock); clock.tick(20);
  const occupied = join(f.root, 'occupied.json'); await writeFile(occupied, 'caller output', {mode: 0o600});
  await assert.rejects(finishOperation(f.state, 'failed', occupied, 'unavailable', clock), refusal);
  const sealed = await readFile(f.state, 'utf8'); clock.tick(800);
  const report = await finishOperation(f.state, 'failed', f.output, 'unavailable', clock);
  assert.equal(report.status, 'failed'); assert.equal(report.elapsedMs, 35); assert.equal(report.cachePreparation, 'unavailable');
  const before = await stat(f.output); const first = await readFile(f.output, 'utf8');
  assert.deepEqual(await finishOperation(f.state, 'failed', f.output, undefined, clock), report);
  assert.equal(await readFile(f.state, 'utf8'), sealed); assert.equal(await readFile(f.output, 'utf8'), first); assert.equal((await stat(f.output)).ino, before.ino);
  await assert.rejects(phaseOperation(f.state, 'publish', undefined, clock), refusal);
  await assert.rejects(finishOperation(f.state, 'success', f.output, undefined, clock), refusal);
  await assert.rejects(finishOperation(f.state, 'failed', f.output, 'hit', clock), refusal);
  assert.equal(await readFile(occupied, 'utf8'), 'caller output');
});

test('private files refuse permissive modes, links, symlink parents, oversized inputs and changed declared context', async t => {
  const f = await files(t), clock = new Clock();
  await chmod(f.input, 0o644); await assert.rejects(startOperation(f.input, f.state, clock), refusal); await chmod(f.input, 0o600);
  const alias = join(f.root, 'alias.json'); await link(f.input, alias); await assert.rejects(startOperation(f.input, f.state, clock), refusal); await rm(alias);
  await symlink(f.input, alias); await assert.rejects(startOperation(alias, f.state, clock), refusal); await rm(alias);
  const parent = join(f.root, 'private'), parentAlias = join(f.root, 'parent-alias'); await mkdir(parent, {mode: 0o700}); await symlink(parent, parentAlias);
  await assert.rejects(startOperation(f.input, join(parentAlias, 'state.json'), clock), refusal);
  await chmod(parent, 0o755); await assert.rejects(startOperation(f.input, join(parent, 'state.json'), clock), refusal);
  const large = join(f.root, 'large.json'); await writeFile(large, '{}', {mode: 0o600}); await truncate(large, 32769); await assert.rejects(startOperation(large, f.state, clock), refusal);
  await assert.rejects(startOperation(f.input, `${f.root}/../unsafe.json`, clock), refusal);
  await startOperation(f.input, f.state, clock); const before = await readFile(f.state, 'utf8');
  await writeFile(f.input, JSON.stringify({...declared, sourceCommit: 'b'.repeat(40)}));
  await assert.rejects(phaseOperation(f.state, 'work', undefined, clock), refusal); assert.equal(await readFile(f.state, 'utf8'), before);
});

test('exclusive lock, state checksum and mode checks prevent concurrent or replaced measurements', async t => {
  const f = await files(t), clock = new Clock(); await startOperation(f.input, f.state, clock);
  const before = await readFile(f.state, 'utf8'); await writeFile(f.state + '.lock', 'owned active measurement', {mode: 0o600});
  await assert.rejects(phaseOperation(f.state, 'work', undefined, clock), refusal); assert.equal(await readFile(f.state, 'utf8'), before); await rm(f.state + '.lock');
  const state = JSON.parse(before); state.state.preparation = 'hit'; await writeFile(f.state, JSON.stringify(state));
  await assert.rejects(phaseOperation(f.state, 'work', undefined, clock), refusal); await writeFile(f.state, before);
  await chmod(f.state, 0o640); await assert.rejects(phaseOperation(f.state, 'work', undefined, clock), refusal); await chmod(f.state, 0o600);
  const alias = join(f.root, 'state-link'); await link(f.state, alias); await assert.rejects(phaseOperation(f.state, 'work', undefined, clock), refusal); await rm(alias);
  const saved = join(f.root, 'state-original'); await writeFile(saved, before, {mode: 0o600}); await rm(f.state); await symlink(saved, f.state);
  await assert.rejects(phaseOperation(f.state, 'work', undefined, clock), refusal); assert.equal(await readFile(saved, 'utf8'), before);
});

test('two concurrent phase markers admit one atomic transition and leave a readable state', async t => {
  const f = await files(t), clock = new Clock(); await startOperation(f.input, f.state, clock); clock.tick(10);
  let release!: () => void, announced!: () => void, samples = 0;
  const gate = new Promise<void>(done => {release = done;}), held = new Promise<void>(done => {announced = done;});
  const holdingClock: OperationClock = {async sample() {if (++samples === 2) {announced(); await gate;} return await clock.sample();}};
  const first = phaseOperation(f.state, 'work', undefined, holdingClock);
  try {
    await Promise.race([held, first.then(() => {throw new Error('First marker did not hold its lock');})]);
    const before = await readFile(f.state, 'utf8'), names = await readdir(f.state + '.lock'), owner = join(f.state + '.lock', names[0]!);
    const ownerBefore = await readFile(owner, 'utf8');
    await assert.rejects(phaseOperation(f.state, 'restore', undefined, clock), refusal);
    assert.equal(await readFile(f.state, 'utf8'), before); assert.equal(await readFile(owner, 'utf8'), ownerBefore);
  } finally {release();}
  await first;
  clock.tick(20); const report = await finishOperation(f.state, 'success', f.output, undefined, clock);
  assert.equal(report.elapsedMs, 30); assert.equal(report.phaseTimingMs.setup, 10); assert.equal(report.phaseTimingMs.work, 20); assert.equal(report.phaseTimingMs.restore, null);
});

async function killedLockHolder(t: {after: (fn: () => Promise<void>) => void}, statePath: string) {
  const script = `
    import {readFile} from 'node:fs/promises';
    const {phaseOperation} = await import(process.argv[1]);
    const state = JSON.parse(await readFile(process.argv[2], 'utf8')).state;
    let samples = 0;
    const keepAlive = setInterval(() => {}, 1000);
    await phaseOperation(process.argv[2], 'work', undefined, {async sample() {
      if (++samples === 2) {process.stdout.write('locked\\n'); await new Promise(() => {});}
      return {wallMs: state.lastWallMs, monotonicNs: BigInt(state.lastNs), binding: state.binding};
    }});
    clearInterval(keepAlive);
  `;
  const child = spawn(process.execPath, ['--input-type=module', '-e', script, new URL('./operation.ts', import.meta.url).href, statePath], {stdio: ['ignore', 'pipe', 'pipe']});
  const terminal = new Promise<{code: number | null; signal: NodeJS.Signals | null}>(done => child.once('close', (code, signal) => done({code, signal})));
  t.after(async () => {if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL'); await terminal;});
  await new Promise<void>((ready, reject) => {
    const timer = setTimeout(() => {child.kill('SIGKILL'); reject(new Error('Lock holder did not reach its protected phase'));}, 10000);
    child.stdout.once('data', bytes => {clearTimeout(timer); if (bytes.toString() === 'locked\n') ready(); else reject(new Error('Unexpected lock holder output'));});
    child.once('error', error => {clearTimeout(timer); reject(error);});
    child.once('close', () => {clearTimeout(timer); reject(new Error('Lock holder exited before interruption'));});
  });
  const names = await readdir(statePath + '.lock'); assert.equal(names.length, 1); assert.match(names[0]!, /^owner-[a-f0-9]{32}\.json$/);
  const owner = JSON.parse(await readFile(join(statePath + '.lock', names[0]!), 'utf8')); assert.equal(owner.pid, child.pid);
  child.kill('SIGKILL'); const result = await terminal; assert.equal(result.signal, 'SIGKILL');
  assert.throws(() => process.kill(child.pid!, 0), {code: 'ESRCH'});
  return {owner, path: join(statePath + '.lock', names[0]!)};
}

test('SIGKILL recovery repairs a sealed report with exactly the original final bytes', async t => {
  const f = await files(t), clock = new Clock(); await startOperation(f.input, f.state, clock); clock.tick(35);
  const occupied = join(f.root, 'occupied.json'); await writeFile(occupied, 'caller output', {mode: 0o600});
  await assert.rejects(finishOperation(f.state, 'failed', occupied, 'unavailable', clock), refusal);
  const sealed = await readFile(f.state, 'utf8'), expected = JSON.parse(sealed).state.report;
  await killedLockHolder(t, f.state); const lockInfo = await stat(f.state + '.lock'); assert.equal(lockInfo.mode & 0o7777, 0o700);
  clock.tick(100); const report = await finishOperation(f.state, 'failed', f.output, 'unavailable', clock);
  assert.deepEqual(report, expected); assert.equal(report.elapsedMs, 35); assert.equal(await readFile(f.state, 'utf8'), sealed);
  assert.equal(await readFile(f.output, 'utf8'), JSON.stringify(expected) + '\n'); assert.equal(await readFile(occupied, 'utf8'), 'caller output');
  await assert.rejects(stat(f.state + '.lock'), {code: 'ENOENT'}); assert.ok((await readdir(f.root)).every(name => !name.startsWith('.measurement-lock-')));
});

test('concurrent dead-owner recovery cannot remove the new live owner', async t => {
  const f = await files(t), clock = new Clock(); await startOperation(f.input, f.state, clock); await killedLockHolder(t, f.state); clock.tick(10);
  let release!: () => void, announced!: () => void;
  const gate = new Promise<void>(done => {release = done;}), held = new Promise<void>(done => {announced = done;});
  const holdingClock = (): OperationClock => {let samples = 0; return {async sample() {if (++samples === 2) {announced(); await gate;} return await clock.sample();}};};
  const attempts = [phaseOperation(f.state, 'work', undefined, holdingClock()), phaseOperation(f.state, 'restore', undefined, holdingClock())]
    .map(attempt => attempt.then(value => ({status: 'fulfilled' as const, value}), reason => ({status: 'rejected' as const, reason})));
  try {
    await Promise.race([held, new Promise<void>((_done, reject) => {const timer = setTimeout(() => reject(new Error('Recovery did not acquire a lock')), 5000); timer.unref();})]);
    const loser = await Promise.race(attempts); assert.equal(loser.status, 'rejected');
    const names = await readdir(f.state + '.lock'); assert.equal(names.length, 1);
    const owner = JSON.parse(await readFile(join(f.state + '.lock', names[0]!), 'utf8')); assert.equal(owner.pid, process.pid);
  } finally {release();}
  const results = await Promise.all(attempts); assert.equal(results.filter(result => result.status === 'fulfilled').length, 1); assert.equal(results.filter(result => result.status === 'rejected').length, 1);
  clock.tick(20); assert.equal((await finishOperation(f.state, 'success', f.output, undefined, clock)).elapsedMs, 30);
});

test('live, foreign-clock, malformed and unsafe owners stay untouched; empty interrupted release is recoverable', async t => {
  const variants = [
    {version: 1, pid: process.pid, binding: 'a'.repeat(64)},
    {version: 1, pid: process.pid, binding: 'b'.repeat(64)},
    {version: 2, pid: process.pid, binding: 'a'.repeat(64)},
    ...[0, -1, 1.5, 2147483648].map(pid => ({version: 1, pid, binding: 'a'.repeat(64)})),
  ];
  const invalidPids: number[] = [], originalKill = process.kill;
  process.kill = ((pid, signal) => {if (!Number.isSafeInteger(pid) || pid <= 0 || pid > 0x7fffffff) invalidPids.push(pid); return originalKill(pid, signal);}) as typeof process.kill;
  try {
    for (const variant of variants) {
      const f = await files(t), clock = new Clock(); await startOperation(f.input, f.state, clock); const before = await readFile(f.state, 'utf8');
      const lock = f.state + '.lock', nonce = 'd'.repeat(32); await mkdir(lock, {mode: 0o700});
      const owner = join(lock, `owner-${nonce}.json`), bytes = JSON.stringify({...variant, nonce}); await writeFile(owner, bytes, {mode: 0o600});
      await assert.rejects(phaseOperation(f.state, 'work', undefined, clock), refusal); assert.equal(await readFile(f.state, 'utf8'), before); assert.equal(await readFile(owner, 'utf8'), bytes);
      assert.ok((await readdir(f.root)).every(name => !name.startsWith('.measurement-lock-')));
    }
  } finally {process.kill = originalKill;}
  assert.deepEqual(invalidPids, []);
  for (const kind of ['unknown', 'hardlink', 'symlink', 'directory-mode', 'lock-symlink']) {
    const f = await files(t), clock = new Clock(); await startOperation(f.input, f.state, clock); const before = await readFile(f.state, 'utf8');
    const lock = f.state + '.lock', nonce = 'd'.repeat(32), target = join(f.root, 'protected-target.json'); await mkdir(lock, {mode: 0o700});
    const owner = join(lock, `owner-${nonce}.json`); await writeFile(target, JSON.stringify({version: 1, pid: process.pid, binding: clock.binding, nonce}), {mode: 0o600});
    if (kind === 'unknown') await writeFile(join(lock, 'future-owner'), 'preserve', {mode: 0o600});
    else if (kind === 'hardlink') await link(target, owner);
    else if (kind === 'symlink') await symlink(target, owner);
    else if (kind === 'directory-mode') {await writeFile(owner, await readFile(target), {mode: 0o600}); await chmod(lock, 0o755);}
    else {await rm(lock, {recursive: true}); await symlink(f.root, lock);}
    const targetBefore = await readFile(target, 'utf8'); await assert.rejects(phaseOperation(f.state, 'work', undefined, clock), refusal);
    assert.equal(await readFile(f.state, 'utf8'), before); assert.equal(await readFile(target, 'utf8'), targetBefore);
  }
  const empty = await files(t), clock = new Clock(); await startOperation(empty.input, empty.state, clock); await mkdir(empty.state + '.lock', {mode: 0o700});
  clock.tick(10); await phaseOperation(empty.state, 'work', undefined, clock); await assert.rejects(stat(empty.state + '.lock'), {code: 'ENOENT'});
});

test('a clock failure after lock acquisition removes only its own prepared owner', async t => {
  const f = await files(t), clock = new Clock(); await startOperation(f.input, f.state, clock); const before = await readFile(f.state, 'utf8'); let samples = 0;
  const unavailable: OperationClock = {async sample() {if (++samples === 2) throw new Error('fixture clock unavailable'); return await clock.sample();}};
  await assert.rejects(phaseOperation(f.state, 'work', undefined, unavailable), refusal); assert.equal(await readFile(f.state, 'utf8'), before);
  await assert.rejects(stat(f.state + '.lock'), {code: 'ENOENT'}); assert.ok((await readdir(f.root)).every(name => !name.startsWith('.measurement-lock-')));
});

test('boot/host changes, monotonic rewind, wall jumps and intervals over one day are refused without modifying state', async t => {
  const cases = [(clock: Clock) => {clock.binding = 'b'.repeat(64);}, (clock: Clock) => {clock.mono -= 1n;}, (clock: Clock) => {clock.wall -= 1;},
    (clock: Clock) => {clock.wall += 5001;}, (clock: Clock) => {clock.tick(86400001);}, (clock: Clock) => {clock.tick(86400000); clock.wall++;}];
  for (const change of cases) {
    const f = await files(t), clock = new Clock(); await startOperation(f.input, f.state, clock); const before = await readFile(f.state, 'utf8'); change(clock);
    await assert.rejects(phaseOperation(f.state, 'work', undefined, clock), refusal); await assert.rejects(finishOperation(f.state, 'success', f.output, undefined, clock), refusal);
    assert.equal(await readFile(f.state, 'utf8'), before); await assert.rejects(stat(f.output), {code: 'ENOENT'});
  }
});

test('repeated phase markers have a bounded count and invalid context never starts an operation', async t => {
  const f = await files(t), clock = new Clock(); await startOperation(f.input, f.state, clock);
  for (let i = 0; i < 128; i++) await phaseOperation(f.state, 'setup', undefined, clock);
  await assert.rejects(phaseOperation(f.state, 'work', undefined, clock), refusal);
  assert.equal((await finishOperation(f.state, 'cancelled', f.output, undefined, clock)).status, 'cancelled');
  const invalid = [{...declared, token: 'private-ambient-value'}, {...declared, workloadDigest: 'incomplete'}, {...declared, outputState: 'cached'}, {...declared, sessionId: 'private/path'},
    {...declared, sourceCommit: 'main'}, {...declared, integration: 'turbo'}, {...declared, cachePolicy: 'off'}, {...declared, baselineReceiptId: 'not-a-receipt'},
    {...declared, environmentDigest: null}, JSON.parse('{"__proto__":{"polluted":true}}')];
  for (const value of invalid) {
    const g = await files(t); await writeFile(g.input, JSON.stringify(value)); await assert.rejects(startOperation(g.input, g.state, clock), refusal); await assert.rejects(stat(g.state), {code: 'ENOENT'});
  }
});

async function pair(t: {after: (fn: () => Promise<void>) => void}, baseline: Partial<OperationContext> = {}, candidate: Partial<OperationContext> = {}, candidateStatus: 'success' | 'failed' = 'success', candidateWork = 70) {
  const a = await files(t, {cachePreparation: 'miss', ...baseline}), b = await files(t, {cachePreparation: 'hit', ...candidate}), clock = new Clock();
  for (const [file, work, status] of [[a, 100, 'success'], [b, candidateWork, candidateStatus]] as const) {
    await startOperation(file.input, file.state, clock);
    for (const [phase, elapsed] of [['restore', 10], ['work', file === a ? 30 : 40], ['close', work], ['publish', 10]] as const) {clock.tick(elapsed); await phaseOperation(file.state, phase, undefined, clock);}
    clock.tick(20); await finishOperation(file.state, status, file.output, undefined, clock); clock.tick(5);
  }
  return {a, b, clock};
}

test('explicit fresh cold/warm and bypass/enabled pairs include overhead and retain negative observations', async t => {
  const f = await pair(t), result = await compareOperations(f.a.state, f.b.state, f.clock);
  assert.equal(result.selection, 'explicitLocalPair'); assert.equal(result.matched, true); assert.equal(result.method, 'coldToWarm'); assert.deepEqual(result.reasons, []);
  assert.equal(result.baselineElapsedMs, 170); assert.equal(result.candidateElapsedMs, 150); assert.equal(result.observedDifferenceMs, 20); assert.equal(result.observedWorkDifferenceMs, 30); assert.equal(result.observedOverheadDifferenceMs, -10);
  assert.equal(result.localBaselineReportSHA256, sha(JSON.stringify(JSON.parse(await readFile(f.a.output, 'utf8'))))); assert.equal(result.localCandidateReportSHA256, sha(JSON.stringify(JSON.parse(await readFile(f.b.output, 'utf8')))));
  assert.ok(!('baselineReceiptId' in result)); assert.ok(!('candidateReceiptId' in result));
  assert.deepEqual(result.assumptions, ['callerDeclaredContext', 'sameSessionWarmthMayDiffer', 'observedNotCausal', 'cpuAndCostUnknown']);
  const negative = await pair(t, {cachePolicy: 'bypass', cachePreparation: 'bypassed'}, {cachePreparation: 'unavailable'}, 'success', 200);
  const slower = await compareOperations(negative.a.state, negative.b.state, negative.clock);
  assert.equal(slower.method, 'bypassToEnabled'); assert.equal(slower.matched, true); assert.equal(slower.observedDifferenceMs, -110); assert.equal(slower.observedWorkDifferenceMs, -100);
});

test('failed or changed-context samples explain comparison refusal without calculating savings', async t => {
  const controls: Array<[Partial<OperationContext>, string[]]> = [[{project: 'another-project'}, ['projectMismatch']], [{integration: 'xcode'}, ['integrationMismatch']], [{compatibility: 'different-toolchain'}, ['profileMismatch']],
    [{sourceCommit: undefined}, ['sourceMissing']], [{sourceCommit: 'b'.repeat(40)}, ['sourceMismatch']], [{sessionId: undefined}, ['sessionMissing']], [{sessionId: 'run-another-session-002'}, ['sessionMismatch']],
    [{workloadDigest: 'sha256:' + '5'.repeat(64)}, ['workloadMismatch']], [{coverageDigest: 'sha256:' + '6'.repeat(64)}, ['coverageMismatch']], [{environmentDigest: undefined}, ['environmentMissing']],
    [{environmentDigest: 'sha256:' + '7'.repeat(64)}, ['environmentMismatch']], [{otherCachesDigest: undefined}, ['otherCachesMissing']], [{otherCachesDigest: 'sha256:' + '8'.repeat(64)}, ['otherCachesMismatch']],
    [{outputState: 'incremental'}, ['outputNotFresh']], [{cachePolicy: 'unknown'}, ['cachePolicyMismatch']], [{cachePreparation: 'miss'}, ['cachePolicyMismatch']]];
  for (const [change, reasons] of controls) {
    const f = await pair(t, {}, change), result = await compareOperations(f.a.state, f.b.state, f.clock);
    assert.equal(result.matched, false); assert.equal(result.method, 'unmatched'); assert.deepEqual(result.reasons, reasons);
    assert.equal(result.observedDifferenceMs, null); assert.equal(result.observedWorkDifferenceMs, null); assert.equal(result.observedOverheadDifferenceMs, null);
  }
  const failed = await pair(t, {}, {}, 'failed'); assert.deepEqual((await compareOperations(failed.a.state, failed.b.state, failed.clock)).reasons, ['unsuccessfulOperation']);
  const reversed = await pair(t); const r = await compareOperations(reversed.b.state, reversed.a.state, reversed.clock); assert.ok(r.reasons.includes('operationOrderMismatch'));
  const same = await compareOperations(reversed.a.state, reversed.a.state, reversed.clock); assert.ok(same.reasons.includes('operationOrderMismatch'));
});

test('unmeasured phase remains null in a valid whole-operation comparison', async t => {
  const a = await files(t, {cachePolicy: 'bypass', cachePreparation: 'bypassed'}), b = await files(t, {cachePreparation: 'hit'}), clock = new Clock();
  await startOperation(a.input, a.state, clock); clock.tick(10); await phaseOperation(a.state, 'work', undefined, clock); clock.tick(90); await finishOperation(a.state, 'success', a.output, undefined, clock);
  clock.tick(5); await startOperation(b.input, b.state, clock); clock.tick(5); await phaseOperation(b.state, 'work', undefined, clock); clock.tick(45); await finishOperation(b.state, 'success', b.output, undefined, clock);
  const result = await compareOperations(a.state, b.state, clock); assert.equal(result.observedDifferenceMs, 50); assert.equal(result.observedWorkDifferenceMs, 45); assert.equal(result.observedOverheadDifferenceMs, null);
});

test('explicit pair outside one day stays unmatched and an unsealed observation cannot be compared', async t => {
  const a = await files(t, {cachePolicy: 'bypass', cachePreparation: 'bypassed'}), b = await files(t, {cachePreparation: 'hit'}), clock = new Clock();
  await startOperation(a.input, a.state, clock); clock.tick(10); await finishOperation(a.state, 'success', a.output, undefined, clock); clock.tick(86400001);
  await startOperation(b.input, b.state, clock); await assert.rejects(compareOperations(a.state, b.state, clock), refusal);
  clock.tick(5); await finishOperation(b.state, 'success', b.output, undefined, clock);
  const result = await compareOperations(a.state, b.state, clock); assert.deepEqual(result.reasons, ['comparisonWindowExceeded']); assert.equal(result.observedDifferenceMs, null);
});

async function cli(args: string[], env: NodeJS.ProcessEnv = {}) {
  const child = spawn(process.execPath, [resolve(process.env.LAYERCACHE_OPERATION_TEST_ENTRY ?? 'native/cli.ts'), ...args], {stdio: ['ignore', 'pipe', 'pipe'], env: {...process.env, ...env}});
  let stdout = '', stderr = ''; child.stdout.on('data', chunk => {stdout += chunk;}); child.stderr.on('data', chunk => {stderr += chunk;});
  return await new Promise<{code: number | null; stdout: string; stderr: string}>((resolveResult, reject) => {
    const timer = setTimeout(() => {child.kill('SIGKILL'); reject(new Error('Operation command timed out'));}, 10000);
    child.on('error', error => {clearTimeout(timer); reject(error);}); child.on('close', code => {clearTimeout(timer); resolveResult({code, stdout, stderr});});
  });
}
test('split command processes use one local monotonic clock without network, credentials or automatic native execution', async t => {
  const f = await files(t, {integration: 'xcode'}); let requests = 0;
  const endpoint = createServer((_request, response) => {requests++; response.writeHead(500); response.end();});
  await new Promise<void>(ready => endpoint.listen(0, '127.0.0.1', ready)); t.after(async () => {await new Promise<void>((done, reject) => endpoint.close(error => error ? reject(error) : done()));});
  const address = endpoint.address(); assert.ok(address && typeof address !== 'string');
  const env = {LAYER_CACHE_URL: `http://127.0.0.1:${address.port}`, LAYER_CACHE_TOKEN: 'private-ambient-token', LAYERCACHE_PRIVATE_COMMAND: 'must-not-be-serialized'};
  const start = await cli(['measure-start', '--context-file', f.input, '--state-file', f.state], env); assert.equal(start.code, 0, start.stderr);
  const id = JSON.parse(start.stdout).operationId;
  for (const phase of ['restore', 'work', 'close', 'publish']) {
    const next = await cli(['measure-phase', '--state-file', f.state, '--phase', phase, '--cache-preparation', 'hit'], env); assert.equal(next.code, 0, next.stderr); assert.equal(JSON.parse(next.stdout).operationId, id);
  }
  const finished = await cli(['measure-finish', '--state-file', f.state, '--status', 'success', '--output', f.output], env); assert.equal(finished.code, 0, finished.stderr); assert.equal(JSON.parse(finished.stdout).state, 'sealed');
  const bytes = await readFile(f.output, 'utf8'), report = JSON.parse(bytes); assert.equal(report.operationId, id); assert.equal(report.status, 'success'); assert.equal(report.cachePreparation, 'hit');
  assert.ok(report.elapsedMs >= 0); assert.ok(Object.values(report.phaseTimingMs).every(value => typeof value === 'number' && value >= 0)); assert.equal(requests, 0);
  assert.doesNotMatch(bytes + start.stdout + finished.stdout, /private-ambient|must-not-be|contextPath|binding|hostname|token|example-project/);
  assert.equal((await cli(['measure-finish', '--state-file', f.state, '--status', 'success', '--output', f.output], env)).code, 0); assert.equal(await readFile(f.output, 'utf8'), bytes);
  const comparison = await cli(['measure-compare', '--baseline-state', f.state, '--candidate-state', f.state, '--json'], env); assert.equal(comparison.code, 0, comparison.stderr); assert.equal(JSON.parse(comparison.stdout).matched, false);
  for (const args of [['measure-start', '--context-file', f.input], ['measure-phase', '--state-file', f.state, '--phase', 'execute'], ['measure-finish', '--state-file', f.state, '--status', 'passed', '--output', f.output],
    ['measure-compare', '--baseline-state', f.state, '--candidate-state', f.state, '--json', '--json'], ['measure-other'], ['measure-start', '--context-file', f.input, '--state-file', f.state, '--token', 'private-ambient-token']]) {
    const rejected = await cli(args, env); assert.equal(rejected.code, 1); assert.equal(rejected.stdout, ''); assert.doesNotMatch(rejected.stderr, /private-ambient|example-project|\/tmp\//);
  }
});
