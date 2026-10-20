import assert from 'node:assert/strict';
import test from 'node:test';
import { createHash } from 'node:crypto';
import { spawn, spawnSync } from 'node:child_process';
import { createServer } from 'node:http';
import { chmod, link, mkdir, mkdtemp, readFile, readdir, rm, stat, symlink, truncate, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

const project = 'synthetic-project', compatibility = 'synthetic-gradle931';
const nonce = '11111111-1111-4111-8111-111111111111';
const nativeKeys = ['a'.repeat(32), 'b'.repeat(32), 'c'.repeat(32)];
async function cli(args: string[], env: NodeJS.ProcessEnv = {}) {
  const child = spawn(process.execPath, [resolve(process.env.LAYERCACHE_GRADLE_REPORT_TEST_ENTRY ?? 'native/cli.ts'), 'gradle-report', ...args], {stdio: ['ignore', 'pipe', 'pipe'], env: {...process.env, ...env}});
  let stdout = '', stderr = '';
  child.stdout.on('data', data => {stdout += data;}); child.stderr.on('data', data => {stderr += data;});
  return new Promise<{code: number | null; stdout: string; stderr: string}>((resolveResult, reject) => {
    const timer = setTimeout(() => {child.kill('SIGKILL'); reject(new Error('Gradle report fixture timed out'));}, 10000);
    child.on('error', error => {clearTimeout(timer); reject(error);});
    child.on('close', code => {clearTimeout(timer); resolveResult({code, stdout, stderr});});
  });
}
async function files(t: {after: (fn: () => Promise<void>) => void}) {
  const root = await mkdtemp(join(tmpdir(), 'layercache-gradle-report-cli-'));
  t.after(async () => {await rm(root, {recursive: true, force: true});});
  const reports = join(root, 'private-reports');
  await mkdir(reports, {mode: 0o700});
  const now = Date.now(), startedAtMs = now - 1000, finishedAtMs = now - 500;
  const states = ['executed', 'fromCache', 'executed', 'executed', 'upToDate', 'noSource', 'skipped'];
  const tasks = states.map((outcome, index) => {
    const taskPath = `:private-source-${index}:privateTask`;
    return {taskPath, workRef: 'sha256:' + createHash('sha256').update(`layercache-gradle-task-v1\0${project}\0${compatibility}\0${taskPath}`).digest('hex'),
      outcome, startedAtMs, finishedAtMs, ...(outcome === 'executed' ? {executionMs: 500} : {}), privateEnvironment: 'private-credential'};
  });
  const document = {version: 1, invocationId: 'gradle-' + nonce, gradleVersion: '9.3.1', startedAtMs, finishedAtMs, status: 'unknown', truncated: false,
    tasks, privateCommand: 'private-credential /private/working/tree'};
  const report = join(reports, document.invocationId + '.json'), info = join(root, 'private-info.log'), output = join(root, 'native-meter.json');
  const infoText = [
    `Build cache key for task '${tasks[0]!.taskPath}' is ${nativeKeys[0]}`,
    `Build cache key for task '${tasks[1]!.taskPath}' is ${nativeKeys[1]}`,
    `Caching disabled for task '${tasks[2]!.taskPath}' because:`, '  Private reason stays local',
    `Build cache key for task '${tasks[4]!.taskPath}' is ${nativeKeys[2]}`,
    'private-credential /private/working/tree', 'BUILD SUCCESSFUL in 1s', '',
  ].join('\n');
  await writeFile(report, JSON.stringify(document), {mode: 0o600}); await writeFile(info, infoText, {mode: 0o600});
  const args = ['--project', project, '--compatibility', compatibility, '--reports-dir', reports, '--info-log-file', info, '--output', output];
  return {root, reports, document, report, info, infoText, output, args};
}
function withValue(args: string[], flag: string, value: string): string[] {
  const next = [...args]; next[next.indexOf(flag) + 1] = value; return next;
}
async function noOutput(path: string) {await assert.rejects(stat(path), {code: 'ENOENT'});}

test('offline Gradle CLI preserves actual native outcomes and exact key eligibility without auth or importer telemetry', async t => {
  const f = await files(t);
  let requests = 0;
  const endpoint = createServer((_request, response) => {requests++; response.writeHead(500); response.end();});
  await new Promise<void>(resolveReady => endpoint.listen(0, '127.0.0.1', resolveReady));
  t.after(async () => {await new Promise<void>((resolveClose, reject) => endpoint.close(error => error ? reject(error) : resolveClose()));});
  const address = endpoint.address(); assert.ok(address && typeof address !== 'string');
  const result = await cli(f.args, {LAYER_CACHE_URL: `http://127.0.0.1:${address.port}`, LAYER_CACHE_TOKEN: 'private-ambient-token', XDG_CACHE_HOME: join(f.root, 'cache')});
  assert.equal(result.code, 0, result.stderr); assert.equal(result.stdout, ''); assert.equal(result.stderr, ''); assert.equal(requests, 0);
  const bytes = await readFile(f.output, 'utf8'), report = JSON.parse(bytes);
  assert.equal((await stat(f.output)).mode & 0o777, 0o600);
  assert.equal(report.version, 1); assert.equal(report.invocationId, 'run-' + nonce); assert.equal(report.status, 'success');
  assert.equal(report.startedAt, new Date(f.document.startedAtMs).toISOString());
  assert.equal(report.finishedAt, new Date(f.document.finishedAtMs).toISOString());
  assert.deepEqual(report.tasks.map((row: {outcome: string}) => row.outcome), f.document.tasks.map(row => row.outcome));
  assert.deepEqual(report.tasks.slice(0, 3).map((row: {cacheEligibility: string}) => row.cacheEligibility), ['eligible', 'eligible', 'ineligible']);
  assert.equal(report.tasks[0].nativeKey, nativeKeys[0]); assert.equal(report.tasks[1].nativeKey, nativeKeys[1]);
  assert.equal(report.tasks[0].executionMs, 500); assert.equal(report.tasks[1].executionMs, undefined);
  assert.equal(report.tasks[2].nativeKey, undefined); assert.equal(report.tasks[3].cacheEligibility, undefined);
  assert.ok(report.tasks.slice(4).every((row: {nativeKey?: string; cacheEligibility?: string; executionMs?: number}) => row.nativeKey === undefined && row.cacheEligibility === undefined && row.executionMs === undefined));
  assert.equal(report.tasks[0].workRef, f.document.tasks[0]!.workRef);
  assert.doesNotMatch(bytes, /private|taskPath|project|compatibility|command|token|resource|cpu|sourceCommit|repository|directory|\.log|\//i);
  assert.deepEqual((await readdir(f.root)).sort(), ['native-meter.json', 'private-info.log', 'private-reports']);
  assert.equal(await readFile(f.report, 'utf8'), JSON.stringify(f.document)); assert.equal(await readFile(f.info, 'utf8'), f.infoText);
});

test('one invocation exports stable retry bytes and never overwrites an existing file or symlink', async t => {
  const f = await files(t);
  assert.equal((await cli(f.args)).code, 0);
  const before = await readFile(f.output, 'utf8'), inode = (await stat(f.output)).ino;
  const copy = join(f.root, 'second-export.json'); assert.equal((await cli(withValue(f.args, '--output', copy))).code, 0);
  assert.equal(await readFile(copy, 'utf8'), before);
  const repeated = await cli(f.args); assert.equal(repeated.code, 1); assert.equal(repeated.stderr, 'Gradle report output unavailable\n');
  assert.equal(await readFile(f.output, 'utf8'), before); assert.equal((await stat(f.output)).ino, inode);
  const linked = join(f.root, 'linked-output'); await symlink(f.output, linked);
  assert.equal((await cli(withValue(f.args, '--output', linked))).code, 1); assert.equal(await readFile(f.output, 'utf8'), before);
});

test('offline Gradle 9.4.1 export retains qualified keys and original task-operation duration', async t => {
  const f = await files(t);
  f.document.gradleVersion = '9.4.1'; await writeFile(f.report, JSON.stringify(f.document));
  const result = await cli(f.args);assert.equal(result.code, 0, result.stderr);
  const report = JSON.parse(await readFile(f.output, 'utf8'));
  assert.equal(report.status, 'success');
  assert.equal(report.tasks[0].nativeKey, nativeKeys[0]);assert.equal(report.tasks[0].executionMs, 500);
  assert.equal(report.tasks[1].nativeKey, nativeKeys[1]);assert.equal(report.tasks[1].outcome, 'fromCache');
  assert.equal(report.tasks[2].cacheEligibility, 'ineligible');assert.equal(report.tasks[2].nativeKey, undefined);
  assert.doesNotMatch(JSON.stringify(report), /private|taskPath|command|token/i);
});

test('offline no-actions classification keeps lifecycle execution separate from cache misses', async t => {
  for (const gradleVersion of ['9.3.1', '9.4.1']) {
    const f = await files(t);f.document.gradleVersion = gradleVersion;
    await writeFile(f.report, JSON.stringify(f.document));
    const task = f.document.tasks[3]!;
    const infoText = f.infoText.replace('BUILD SUCCESSFUL', `Skipping task '${task.taskPath}' as it has no actions.\nBUILD SUCCESSFUL`);
    await writeFile(f.info, infoText);
    const result = await cli(f.args);assert.equal(result.code, 0, result.stderr);
    const report = JSON.parse(await readFile(f.output, 'utf8'));
    assert.deepEqual(report.tasks.map((row: {outcome: string}) => row.outcome), f.document.tasks.map(row => row.outcome));
    assert.equal(report.tasks[3].cacheEligibility, 'ineligible');assert.equal(report.tasks[3].outcome, 'executed');
    assert.equal(report.tasks[3].executionMs, 500);assert.equal(report.tasks[3].nativeKey, undefined);
    assert.equal(report.tasks[3].workRef, task.workRef);
    assert.equal(report.tasks[0].nativeKey, nativeKeys[0]);assert.equal(report.tasks[1].nativeKey, nativeKeys[1]);
    assert.doesNotMatch(JSON.stringify(report), /private|taskPath|no actions|Skipping/i);
    assert.equal(await readFile(f.report, 'utf8'), JSON.stringify(f.document));assert.equal(await readFile(f.info, 'utf8'), infoText);
  }
});

test('missing unsupported or unsafe optional info cannot invent keys or discard native FROM-CACHE outcomes', async t => {
  const f = await files(t);
  const symlinkInfo = join(f.root, 'symlink-info'); await symlink(f.info, symlinkInfo);
  const large = join(f.root, 'oversized-info'); await writeFile(large, '', {mode: 0o600}); await truncate(large, 64 * 1024 ** 2 + 1);
  const cases = [f.args.filter((_, index) => index !== f.args.indexOf('--info-log-file') && index !== f.args.indexOf('--info-log-file') + 1),
    withValue(f.args, '--info-log-file', join(f.root, 'missing-private-info')), withValue(f.args, '--info-log-file', symlinkInfo), withValue(f.args, '--info-log-file', large)];
  if (process.platform !== 'win32') {
    const fifo = join(f.root, 'info.fifo'); assert.equal(spawnSync('mkfifo', [fifo]).status, 0);
    cases.push(withValue(f.args, '--info-log-file', fifo));
  }
  for (let index = 0; index < cases.length; index++) {
    const output = join(f.root, `unknown-${index}.json`), result = await cli(withValue(cases[index]!, '--output', output));
    assert.equal(result.code, 0, result.stderr);
    const report = JSON.parse(await readFile(output, 'utf8'));
    assert.equal(report.status, 'unknown'); assert.equal(report.tasks[1].outcome, 'fromCache'); assert.equal(report.tasks[1].cacheEligibility, 'eligible');
    assert.ok(report.tasks.every((row: {nativeKey?: string}) => row.nativeKey === undefined));
    assert.equal(report.tasks[0].cacheEligibility, undefined); assert.equal(report.tasks[2].cacheEligibility, undefined);
  }
  f.document.gradleVersion = '9.4.2'; await writeFile(f.report, JSON.stringify(f.document));
  const result = await cli(f.args); assert.equal(result.code, 0, result.stderr);
  const report = JSON.parse(await readFile(f.output, 'utf8'));
  assert.equal(report.status, 'unknown'); assert.ok(report.tasks.every((row: {nativeKey?: string}) => row.nativeKey === undefined));
});

test('empty or multi-invocation directories cannot combine runs or export an arbitrary invocation', async t => {
  const f = await files(t);
  await rm(f.report);
  let result = await cli(f.args); assert.equal(result.code, 1); assert.equal(result.stderr, 'Gradle report export requires exactly one completed invocation\n'); await noOutput(f.output);
  await writeFile(f.report, JSON.stringify(f.document), {mode: 0o600});
  const other = {...f.document, invocationId: 'gradle-22222222-2222-4222-8222-222222222222'};
  await writeFile(join(f.reports, other.invocationId + '.json'), JSON.stringify(other), {mode: 0o600});
  result = await cli(f.args); assert.equal(result.code, 1); assert.equal(result.stderr, 'Gradle report export requires exactly one completed invocation\n'); await noOutput(f.output);
});

test('unsafe report directories and files fail closed without leaking private paths or source bytes', async t => {
  const f = await files(t), original = JSON.stringify(f.document);
  const linkedDirectory = join(f.root, 'linked-directory'); await symlink(f.reports, linkedDirectory);
  const fail = async (args = f.args) => {
    const result = await cli(args); assert.equal(result.code, 1); assert.equal(result.stderr, 'Gradle invocation reports unavailable\n');
    assert.doesNotMatch(result.stderr, /private|credential|\/tmp|taskPath/); await noOutput(f.output);
  };
  await fail(withValue(f.args, '--reports-dir', linkedDirectory));
  await chmod(f.reports, 0o755); await fail(); await chmod(f.reports, 0o700);
  await chmod(f.report, 0o644); await fail(); await chmod(f.report, 0o600);
  const hardlink = join(f.root, 'private-hardlink'); await link(f.report, hardlink); await fail(); await rm(hardlink);
  await rm(f.report); await symlink(f.info, f.report); await fail(); await rm(f.report);
  await writeFile(f.report, '', {mode: 0o600}); await truncate(f.report, 4 * 1024 ** 2 + 1); await fail();
  await writeFile(f.report, '{"private-credential":'); await fail();
  await writeFile(f.report, JSON.stringify({...f.document, truncated: true})); await fail();
  await writeFile(f.report, JSON.stringify({...f.document, invocationId: 'gradle-22222222-2222-4222-8222-222222222222'})); await fail();
  if (process.platform !== 'win32') {await rm(f.report); assert.equal(spawnSync('mkfifo', [f.report]).status, 0); await fail(); await rm(f.report);}
  await writeFile(f.report, original, {mode: 0o600}); assert.equal(await readFile(f.report, 'utf8'), original);
});

test('scope and command controls require explicit matching identities without accepting credentials or arbitrary flags', async t => {
  const f = await files(t);
  for (const args of [[], f.args.slice(2), [...f.args, '--project', project], [...f.args, '--token', 'private-credential'],
    [...f.args, '--info-log-file'], withValue(f.args, '--project', 'project with spaces'), withValue(f.args, '--compatibility', 'unsafe/compatibility')]) {
    const result = await cli(args); assert.equal(result.code, 1); assert.equal(result.stderr, 'Invalid Gradle report arguments\n'); await noOutput(f.output);
  }
  const foreign = await cli(withValue(f.args, '--project', 'different-project'));
  assert.equal(foreign.code, 1); assert.equal(foreign.stderr, 'Gradle invocation reports unavailable\n'); await noOutput(f.output);
});
