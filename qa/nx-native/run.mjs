// Real Nx CLI acceptance against disposable LayerCache Local and Team runtimes.
// Nx owns task hashes, archive creation, restore, and terminal replay.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  chmodSync, closeSync, cpSync, existsSync, mkdirSync, mkdtempSync, openSync,
  readFileSync, realpathSync, rmSync, symlinkSync, writeFileSync,
} from 'node:fs';
import { createRequire } from 'node:module';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { basename, dirname, isAbsolute, join, resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import { cleanupOnSignals, stopChild as stop, stopChildren, trackChild } from './processes.mjs';
import { writeEvidence } from './evidence.mjs';

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const fixture = join(repo, 'qa/fixtures/nx-native');
const root = mkdtempSync(join(tmpdir(), 'layercache-nx-native-'));
chmodSync(root, 0o700);
const profile = `${process.platform}-${process.arch}-node${process.versions.node.split('.')[0]}-nx23-schema1`;
const project = 'github.com/layercache/nx-native-qa';
const processes = [];
const cancellation = new AbortController();
const finishSignalCleanup = cleanupOnSignals(processes, cancellation);
const stages = [];
let completedEvidence;
const environment = { ...process.env, CI: 'true', NX_DAEMON: 'false', NX_TUI: 'false',
  NX_SKIP_NATIVE_FILE_CACHE: 'true', NX_FIXTURE_ENV: 'baseline', FORCE_COLOR: '0',
  NODE_NO_WARNINGS: '1', GIT_TERMINAL_PROMPT: '0',
  XDG_CONFIG_HOME: join(root, 'config'), XDG_CACHE_HOME: join(root, 'cache'),
};
for (const key of Object.keys(environment)) {
  if (key.startsWith('NX_') && !['NX_DAEMON', 'NX_TUI', 'NX_SKIP_NATIVE_FILE_CACHE', 'NX_FIXTURE_ENV'].includes(key)
      || key.startsWith('TURBO_') || key.startsWith('LAYER_CACHE_') || key === 'GITHUB_REF'
      || key === 'ACTIONS_CACHE_URL' || key === 'ACTIONS_RUNTIME_TOKEN' || key === 'NO_COLOR') {
    delete environment[key];
  }
}

async function execute(program, args, options = {}) {
  cancellation.signal.throwIfAborted();
  const child = trackChild(spawn(program, args, { cwd: options.cwd ?? repo, env: options.env ?? environment,
    detached: process.platform !== 'win32', stdio: ['ignore', 'pipe', 'pipe'] }));
  processes.push(child);
  return new Promise((resolve, reject) => {
    const output = { stdout: [], stderr: [] };
    let bytes = 0;
    let failure;
    const fail = error => {
      failure ??= error;
      void stop(child).catch(() => {});
    };
    const timeout = setTimeout(() => fail(new Error(`${basename(program)} exceeded the 120-second command budget`)), 120_000);
    for (const stream of ['stdout', 'stderr']) child[stream].on('data', chunk => {
      bytes += chunk.length;
      if (bytes > 8 * 1024 * 1024) fail(new Error(`${basename(program)} exceeded the QA output budget`));
      else output[stream].push(chunk);
    });
    child.once('error', fail);
    child.once('close', (status, signal) => {
      clearTimeout(timeout);
      const result = { status, signal, stdout: Buffer.concat(output.stdout).toString(), stderr: Buffer.concat(output.stderr).toString() };
      if (cancellation.signal.aborted) reject(cancellation.signal.reason);
      else if (failure) reject(failure);
      else if (!options.allowFailure && status !== 0) reject(new Error(`${basename(program)} failed with status ${status}\n${result.stdout}\n${result.stderr}`));
      else resolve(result);
    });
  });
}

async function freeAddress() {
  const server = createServer();
  await new Promise((done, fail) => { server.once('error', fail); server.listen(0, '127.0.0.1', done); });
  const address = server.address();
  assert.ok(address && typeof address !== 'string');
  await new Promise((done, fail) => server.close(error => error ? fail(error) : done()));
  return `127.0.0.1:${address.port}`;
}

let binary = process.env.LAYERCACHE_BIN;
if (binary) assert.ok(isAbsolute(binary), 'LAYERCACHE_BIN must be an absolute path');
const tokenBinary = join(root, 'mint-token');
let nxCLI;

async function start(runtime) {
  cancellation.signal.throwIfAborted();
  const log = openSync(join(root, `${runtime.name}-daemon.log`), 'a', 0o600);
  const child = trackChild(spawn(binary, ['serve', '--config', runtime.path], {
    cwd: repo, env: environment, detached: process.platform !== 'win32', stdio: ['ignore', log, log],
  }));
  closeSync(log);
  let startupError;
  child.on('error', error => { startupError = error; });
  processes.push(child);
  runtime.child = child;
  for (let attempt = 0; attempt < 100; attempt++) {
    cancellation.signal.throwIfAborted();
    if (startupError) throw startupError;
    if (child.exitCode !== null) throw new Error(`${runtime.name} daemon exited; inspect ${root}`);
    try {
      const response = await fetch(runtime.url + '/healthz', { signal: AbortSignal.any([cancellation.signal, AbortSignal.timeout(250)]) });
      if (response.ok) return runtime;
    } catch {}
    await delay(50);
  }
  throw new Error(`${runtime.name} daemon did not become healthy; inspect ${root}`);
}

async function setup(name, role) {
  const configPath = join(root, `${name}.json`);
  const address = await freeAddress();
  const args = ['setup', '--config', configPath, '--data-dir', join(root, `${name}-cache`),
    '--listen', address, '--role', role, '--project', project, '--compatibility-id', profile,
    '--max-size', '67108864', '--non-interactive', '--json'];
  await execute(binary, args);
  const config = JSON.parse(readFileSync(configPath, 'utf8'));
  return start({ name, path: configPath, config, url: `http://${address}` });
}

async function mint(runtime, capability = 'read', compatibility = profile) {
  return (await execute(tokenBinary, ['--config', runtime.path, '--capability', capability,
    '--compatibility', compatibility])).stdout.trim();
}

function workspace(name) {
  const path = join(root, name);
  cpSync(fixture, path, { recursive: true, filter: source => {
    const relative = source.slice(fixture.length);
    return !relative.split('/').some(part => ['node_modules', '.nx', 'dist', 'test-results'].includes(part));
  } });
  symlinkSync(join(fixture, 'node_modules'), join(path, 'node_modules'), 'dir');
  const counter = join(root, `${name}-counter`);
  mkdirSync(counter, { mode: 0o700 });
  assert.ok(!existsSync(join(path, '.nx')), 'fresh workspace has no native Nx cache');
  assert.ok(!existsSync(join(path, '.layercache')), 'direct consumer has no LayerCache cache');
  return { path, counter };
}

function clearNative(work) {
  rmSync(join(work.path, '.nx'), { recursive: true, force: true });
  rmSync(join(work.path, 'apps/fixture/dist'), { recursive: true, force: true });
  rmSync(join(work.path, 'apps/fixture/test-results'), { recursive: true, force: true });
}

function counts(work) {
  const file = join(work.counter, 'executions.jsonl');
  const rows = existsSync(file) ? readFileSync(file, 'utf8').trim().split('\n').map(row => JSON.parse(row)) : [];
  return Object.fromEntries(['build', 'test'].map(task => [task, rows.filter(row => row.task === task).length]));
}

function outputs(work) {
  return Object.fromEntries(['dist/result.bin', 'test-results/verdict.json'].map(file => [file,
    readFileSync(join(work.path, 'apps/fixture', file))]));
}

function digest(bytes) { return 'sha256:' + createHash('sha256').update(bytes).digest('hex'); }

async function run(name, work, local, direct, env = {}, expectFailure = false) {
  const childEnvironment = { ...environment, NX_FIXTURE_COUNTER_DIR: work.counter, ...env };
  if (direct) {
    childEnvironment.NX_SELF_HOSTED_REMOTE_CACHE_SERVER = direct.url;
    childEnvironment.NX_SELF_HOSTED_REMOTE_CACHE_ACCESS_TOKEN = direct.token;
  }
  const args = [nxCLI, 'run', 'fixture:test', '--outputStyle=static'];
  const result = local
    ? await execute(binary, ['run', '--config', local.path, '--', process.execPath, ...args],
      { cwd: work.path, env: childEnvironment, allowFailure: expectFailure })
    : await execute(process.execPath, args, { cwd: work.path, env: childEnvironment, allowFailure: expectFailure });
  writeFileSync(join(root, `${name}.log`), result.stdout + result.stderr, { mode: 0o600 });
  assert.equal(result.status !== 0, expectFailure, `${name} exit status`);
  const report = JSON.parse(readFileSync(join(work.path, '.nx/cache/run.json'), 'utf8'));
  assert.equal(report.tasks.length, 2, `${name} includes real build and test tasks`);
  const tasks = Object.fromEntries(report.tasks.map(task => [task.target, {
    hash: task.hash, cacheStatus: task.cacheStatus, status: task.status,
  }]));
  for (const task of ['build', 'test']) {
    assert.ok((result.stdout + result.stderr).includes(`NATIVE_NX_STDOUT task=${task}`), `${name} replays ${task} stdout`);
    assert.ok((result.stdout + result.stderr).includes(`NATIVE_NX_STDERR task=${task}`), `${name} replays ${task} stderr`);
  }
  const stage = { name, status: result.status, counts: counts(work), tasks };
  stages.push(stage);
  console.log(`${name}: ${Object.entries(tasks).map(([target, task]) => `${target} ${task.cacheStatus}`).join(', ')}`);
  return { stage, text: result.stdout + result.stderr };
}

function assertHit(result, expectedHashes) {
  for (const task of ['build', 'test']) {
    assert.equal(result.stage.tasks[task].hash, expectedHashes[task]);
    assert.equal(result.stage.tasks[task].cacheStatus, 'remote-cache-hit');
    assert.equal(result.stage.tasks[task].status, 0);
  }
}

function logs(work, hashes) {
  return Object.fromEntries(Object.entries(hashes).map(([target, hash]) => [target,
    readFileSync(join(work.path, '.nx/cache/terminalOutputs', hash))]));
}

async function nativeBytes(runtime, token, hash, expectedStatus = 200) {
  const response = await fetch(`${runtime.url}/v1/cache/${encodeURIComponent(hash)}`, {
    headers: { Authorization: `Bearer ${token}` }, signal: AbortSignal.timeout(2_000),
  });
  assert.equal(response.status, expectedStatus, 'direct native HTTP status');
  return Buffer.from(await response.arrayBuffer());
}

try {
  await execute('pnpm', ['install', '--frozen-lockfile'], { cwd: fixture });
  const requireNx = createRequire(realpathSync(join(fixture, 'node_modules/nx/package.json')));
  const nxPackage = requireNx('./package.json');
  assert.equal(nxPackage.version, '23.3.0');
  nxCLI = join(fixture, 'node_modules/nx', nxPackage.bin.nx);
  const native = requireNx('./dist/src/native');
  assert.equal(native.IS_WASM, false, 'this acceptance requires the compiled native Nx client');
  const nativePackageNames = Object.keys(nxPackage.optionalDependencies).filter(name => name.startsWith('@nx/nx-'));
  const installedNativePackages = nativePackageNames.flatMap(name => {
    try { const version = requireNx(`${name}/package.json`).version; assert.equal(version, '23.3.0'); return [{ name, version }]; }
    catch (error) { if (error.code === 'MODULE_NOT_FOUND') return []; throw error; }
  });
  assert.ok(installedNativePackages.length > 0, 'matching native package is installed');
  if (!binary) {
    binary = join(root, 'layercache');
    await execute('go', ['build', '-trimpath', '-o', binary, './cmd/layercache']);
  }
  await execute('go', ['build', '-trimpath', '-o', tokenBinary, './qa/nx-native/token']);
  const team = await setup('team', 'team');
  const local = await setup('local', 'local');
  const producer = workspace('producer');
  await stop(team.child);
  const cold = await run('cold-producer', producer, local);
  assert.deepEqual(cold.stage.counts, { build: 1, test: 1 });
  for (const task of Object.values(cold.stage.tasks)) assert.equal(task.cacheStatus, 'cache-miss');
  const hashes = Object.fromEntries(Object.entries(cold.stage.tasks).map(([name, task]) => [name, task.hash]));
  const expectedOutputs = outputs(producer);
  const expectedLogs = logs(producer, hashes);
  const localReader = await mint(local);
  const localArchives = {};
  for (const [task, hash] of Object.entries(hashes)) {
    localArchives[task] = digest(await nativeBytes(local, localReader, hash));
  }
  clearNative(producer);
  const localReplay = await run('same-local-daemon-replay-team-offline', producer, local);
  assertHit(localReplay, hashes);
  assert.deepEqual(localReplay.stage.counts, { build: 1, test: 1 });
  assert.deepEqual(outputs(producer), expectedOutputs);
  assert.deepEqual(logs(producer, hashes), expectedLogs);
  await stop(local.child);
  await start(local);
  clearNative(producer);
  const restartedReplay = await run('restarted-local-daemon-replay-team-offline', producer, local);
  assertHit(restartedReplay, hashes);
  assert.deepEqual(restartedReplay.stage.counts, { build: 1, test: 1 });
  assert.deepEqual(outputs(producer), expectedOutputs);
  assert.deepEqual(logs(producer, hashes), expectedLogs);
  for (const [task, hash] of Object.entries(hashes)) assert.equal(digest(await nativeBytes(local, localReader, hash)), localArchives[task]);
  await start(team);
  const teamProducer = workspace('direct-team-producer');
  const produced = await run('cold-direct-team-producer', teamProducer, null, { url: team.url, token: await mint(team, 'write') });
  assert.deepEqual(produced.stage.counts, { build: 1, test: 1 });
  for (const target of ['build', 'test']) {
    assert.equal(produced.stage.tasks[target].hash, hashes[target]);
    assert.equal(produced.stage.tasks[target].cacheStatus, 'cache-miss');
  }
  assert.deepEqual(outputs(teamProducer), expectedOutputs);
  const teamLogs = logs(teamProducer, hashes);
  const reader = await mint(team);
  const archives = {};
  for (const [task, hash] of Object.entries(hashes)) archives[task] = digest(await nativeBytes(team, reader, hash));
  await stop(team.child);
  await start(team);
  for (const [task, hash] of Object.entries(hashes)) assert.equal(digest(await nativeBytes(team, reader, hash)), archives[task]);
  const consumer = workspace('fresh-direct-readonly-consumer');
  const direct = { url: team.url, token: reader };
  const consumed = await run('fresh-direct-readonly-consumer', consumer, null, direct);
  assertHit(consumed, hashes);
  assert.deepEqual(consumed.stage.counts, { build: 0, test: 0 });
  assert.deepEqual(outputs(consumer), expectedOutputs);
  assert.deepEqual(logs(consumer, hashes), teamLogs);
  const forbidden = await fetch(team.url + '/v1/cache/qa-readonly-denied', {
    method: 'PUT', headers: { Authorization: `Bearer ${reader}`, 'Content-Type': 'application/octet-stream' },
    body: Buffer.from('cannot publish'), signal: AbortSignal.timeout(2_000),
  });
  assert.equal(forbidden.status, 403, 'read-only consumer cannot populate private Nx cache');
  await forbidden.arrayBuffer();
  for (const changed of ['source', 'dependency', 'environment']) {
    const work = workspace(`changed-${changed}`);
    const env = {};
    if (changed === 'source') writeFileSync(join(work.path, 'apps/fixture/src/message.txt'), 'source-v2\n');
    if (changed === 'dependency') writeFileSync(join(work.path, 'dependency.json'), '{"value":"dependency-v2"}\n');
    if (changed === 'environment') env.NX_FIXTURE_ENV = 'environment-v2';
    const miss = await run(`changed-${changed}`, work, null, direct, env);
    assert.deepEqual(miss.stage.counts, { build: 1, test: 1 });
    for (const target of ['build', 'test']) {
      assert.notEqual(miss.stage.tasks[target].hash, hashes[target], `${changed} changes Nx ${target} hash`);
      assert.equal(miss.stage.tasks[target].cacheStatus, 'cache-miss');
      await nativeBytes(team, reader, miss.stage.tasks[target].hash, 404);
    }
    assert.notDeepEqual(outputs(work), expectedOutputs, `${changed} changes native output bytes`);
  }
  const alternateProfile = profile + '-alternate';
  const alternate = workspace('changed-profile');
  const profileMiss = await run('changed-profile', alternate, null,
    { url: team.url, token: await mint(team, 'read', alternateProfile) });
  assert.deepEqual(profileMiss.stage.counts, { build: 1, test: 1 });
  for (const task of ['build', 'test']) {
    assert.equal(profileMiss.stage.tasks[task].hash, hashes[task], 'profile is scoped by credential, separate from Nx hash');
    assert.equal(profileMiss.stage.tasks[task].cacheStatus, 'cache-miss');
  }
  const failing = workspace('failed-test');
  const firstFailure = await run('failed-test-first', failing, local, null, { NX_FIXTURE_FAIL: '1' }, true);
  assert.deepEqual(firstFailure.stage.counts, { build: 1, test: 1 });
  assert.ok(firstFailure.text.includes('NATIVE_NX_TEST_FAILURE'));
  assert.notEqual(firstFailure.stage.tasks.test.status, 0);
  const secondFailure = await run('failed-test-repeat', failing, local, null, { NX_FIXTURE_FAIL: '1' }, true);
  assert.deepEqual(secondFailure.stage.counts, { build: 1, test: 2 });
  assert.equal(secondFailure.stage.tasks.test.cacheStatus, 'cache-miss');
  assert.notEqual(secondFailure.stage.tasks.test.status, 0);
  clearNative(failing);
  const thirdFailure = await run('failed-test-empty-native-cache', failing, local, null, { NX_FIXTURE_FAIL: '1' }, true);
  assert.deepEqual(thirdFailure.stage.counts, { build: 1, test: 3 });
  assert.equal(thirdFailure.stage.tasks.build.cacheStatus, 'remote-cache-hit');
  assert.equal(thirdFailure.stage.tasks.test.cacheStatus, 'cache-miss');
  assert.notEqual(thirdFailure.stage.tasks.test.status, 0);
  await nativeBytes(local, await mint(local), thirdFailure.stage.tasks.test.hash, 404);
  const evidence = { passed: true, nxVersion: nxPackage.version, installedNativePackages,
    nodeVersion: process.version, profile, root, hashes, localArchives, teamArchives: archives,
    outputDigests: Object.fromEntries(Object.entries(expectedOutputs).map(([name, bytes]) => [name, digest(bytes)])),
    localTerminalOutputDigests: Object.fromEntries(Object.entries(expectedLogs).map(([name, bytes]) => [name, digest(bytes)])),
    teamTerminalOutputDigests: Object.fromEntries(Object.entries(teamLogs).map(([name, bytes]) => [name, digest(bytes)])),
    stages, nativeFilesystemCacheBoundedByLayerCache: false, localNxTeamReadthroughAndPublicationImplemented: false,
  };
  writeFileSync(join(root, 'evidence.json'), JSON.stringify(evidence, null, 2) + '\n', { mode: 0o600 });
  completedEvidence = evidence;
  console.log(`Nx native HTTP acceptance passed. Evidence: ${join(root, 'evidence.json')}`);
} catch (error) {
  console.error(`Nx native HTTP acceptance failed. Disposable evidence: ${root}`);
  throw error;
} finally {
  const cleanup = await Promise.allSettled([finishSignalCleanup(), stopChildren(processes)]);
  const errors = [...new Set(cleanup.filter(result => result.status === 'rejected')
    .flatMap(result => result.reason instanceof AggregateError ? result.reason.errors : [result.reason]))];
  if (errors.length) throw new AggregateError(errors, 'Nx QA cleanup failed after attempting every owned child');
}
if (process.env.NX_QA_OUTPUT) writeEvidence(process.env.NX_QA_OUTPUT, completedEvidence);
