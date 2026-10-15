// Actions transports only Go's private compiler cache; Go still executes tests.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash, randomUUID } from 'node:crypto';
import { chmodSync, closeSync, copyFileSync, existsSync, mkdirSync, mkdtempSync, openSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, isAbsolute, join, resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import { cleanupOnSignals, stopChild, stopChildren, trackChild } from '../nx-native/processes.mjs';

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const root = mkdtempSync(join(tmpdir(), 'layercache-go-dogfood-'));
chmodSync(root, 0o700);
const started = performance.now();
const binary = process.env.LAYERCACHE_BIN;
const receiptPath = process.env.GO_DOGFOOD_QA_RECEIPT ? resolve(process.env.GO_DOGFOOD_QA_RECEIPT) : undefined;
const client = process.env.ACTIONS_CACHE_MODULE ?? join(repo, 'action/cache/node_modules/@actions/cache/lib/cache.js');
const processes = [];
const cancellation = new AbortController();
const finishSignals = cleanupOnSignals(processes, cancellation);
const stages = [];
let commandIndex = 0;
let receipt;
const env = { ...process.env, GOTOOLCHAIN: 'local', GOPROXY: 'off', GOSUMDB: 'off', GOCACHEPROG: '',
  XDG_CONFIG_HOME: join(root, 'config'), XDG_CACHE_HOME: join(root, 'cache') };
for (const key of Object.keys(env)) {
  if (key.startsWith('ACTIONS_') || key.startsWith('LAYER_CACHE_') || key.startsWith('TURBO_')) delete env[key];
}
function digest(bytes) { return `sha256:${createHash('sha256').update(bytes).digest('hex')}`; }

async function execute(label, program, args, options = {}) {
  cancellation.signal.throwIfAborted();
  const begin = performance.now();
  const log = join(root, `command-${++commandIndex}-${label}.log`);
  const child = trackChild(spawn(program, args, { cwd: options.cwd ?? repo, env: options.env ?? env,
    detached: true, stdio: ['ignore', 'pipe', 'pipe'] }));
  processes.push(child);
  return new Promise((done, fail) => {
    const chunks = [];
    let bytes = 0;
    let failure;
    const abort = error => { failure ??= error; void stopChild(child).catch(() => {}); };
    const timeout = setTimeout(() => abort(new Error(`${label} exceeded 120 seconds; inspect ${root}`)), 120_000);
    for (const stream of [child.stdout, child.stderr]) stream.on('data', chunk => {
      bytes += chunk.length;
      if (bytes > 8 * 1024 * 1024) abort(new Error(`${label} exceeded the output budget; inspect ${root}`));
      else chunks.push(chunk);
    });
    child.once('error', abort);
    child.once('close', (status, signal) => {
      clearTimeout(timeout);
      const text = Buffer.concat(chunks).toString();
      writeFileSync(log, text, { mode: 0o600 });
      const result = { status, signal, text, commandWallMs: Math.round(performance.now() - begin) };
      if (cancellation.signal.aborted) fail(cancellation.signal.reason);
      else if (failure) fail(failure);
      else if (!options.allowFailure && status !== 0) fail(new Error(`${label} exited ${status}; inspect ${log}`));
      else done(result);
    });
  });
}

function workspace(name, files) {
  const path = join(root, name);
  mkdirSync(path, { mode: 0o700 });
  // Never copy ignored files, host caches, local credentials or Git metadata.
  for (const file of files) {
    const destination = join(path, file);
    mkdirSync(dirname(destination), { recursive: true });
    copyFileSync(join(repo, file), destination);
  }
  for (const cache of ['gocache', 'gomodcache']) mkdirSync(join(path, '.fixture-cache', cache), { recursive: true });
  return path;
}

function goEnv(path) {
  return { ...env, GOCACHE: join(path, '.fixture-cache/gocache'), GOMODCACHE: join(path, '.fixture-cache/gomodcache') };
}
async function go(label, path, args, options = {}) {
  const result = await execute(label, 'go', args, { cwd: path, env: { ...goEnv(path), ...options.env }, allowFailure: options.allowFailure });
  const row = { label, status: result.status, command: ['go', ...args], commandWallMs: result.commandWallMs,
    compiledPackages: result.text.split('\n').filter(line => /\/compile -o /.test(line)).map(line => line.match(/ -p (\S+)/)?.[1] ?? 'unknown'),
    linkerInvocations: result.text.split('\n').filter(line => /\/link -o /.test(line)).length,
    testRuns: [...result.text.matchAll(/^=== RUN\s+(\S+)/gm)].map(match => match[1]),
    testPasses: [...result.text.matchAll(/^--- PASS: (\S+)/gm)].map(match => match[1]),
    ...(options.env ? { overrides: options.env } : {}) };
  row.compilerInvocations = row.compiledPackages.length;
  stages.push(row);
  console.log(`${label}: ${row.compilerInvocations} compile(s), ${row.testRuns.length} fresh test(s), ${row.commandWallMs} ms`);
  return row;
}

async function stock(label, path, operation, key, config) {
  const result = await execute(label, binary, ['run', '--config', config, '--', process.execPath,
    join(repo, 'qa/stock-actions-cache.mjs'), operation, path, key, client],
  { cwd: path, env: { ...env, RUNNER_TEMP: join(path, '.runner-temp'), RUNNER_OS: 'Linux', GITHUB_REF: 'refs/heads/main' } });
  assert.match(result.text, /Layer Cache run: run-/);
  const archiveBytes = Number(result.text.match(/(?:File Size: |Cache Size: [^\n]*\()(\d+)/)?.[1]);
  assert.ok(archiveBytes > 0, `${label} archive size`);
  const row = { label, status: result.status, commandWallMs: result.commandWallMs, archiveBytes, matched: operation === 'restore' };
  stages.push(row);
  console.log(`${label}: ${archiveBytes} archive byte(s), ${row.commandWallMs} ms`);
  return row;
}

async function start(config, address) {
  const log = openSync(join(root, 'daemon.log'), 'a', 0o600);
  const child = trackChild(spawn(binary, ['serve', '--config', config], { cwd: root, env, detached: true, stdio: ['ignore', log, log] }));
  closeSync(log);
  processes.push(child);
  let error;
  child.once('error', value => { error = value; });
  const begin = performance.now();
  for (let attempt = 0; attempt < 100; attempt++) {
    cancellation.signal.throwIfAborted();
    if (error) throw error;
    if (child.exitCode !== null) throw new Error(`daemon exited; inspect ${root}`);
    try {
      const response = await fetch(`http://${address}/healthz`, { signal: AbortSignal.any([cancellation.signal, AbortSignal.timeout(250)]) });
      if (response.ok) return Math.round(performance.now() - begin);
    } catch {}
    await delay(50);
  }
  throw new Error(`daemon did not become healthy; inspect ${root}`);
}

try {
  // A failed attempt must not leave a previous run's successful result.
  if (receiptPath) rmSync(receiptPath, { force: true });
  assert.equal(process.platform, 'linux', 'this bounded qualification uses Linux');
  assert.equal(process.arch, 'x64', 'this qualification varies GOAMD64 on amd64');
  assert.ok(binary && isAbsolute(binary) && existsSync(binary), 'LAYERCACHE_BIN must name an existing absolute candidate binary');
  assert.ok(isAbsolute(client) && existsSync(client), 'ACTIONS_CACHE_MODULE must name an existing absolute @actions/cache module');
  const clientVersion = JSON.parse(readFileSync(resolve(dirname(client), '../package.json'))).version;
  assert.equal(clientVersion, '6.2.0', 'use the repository locked stock Actions client');
  const source = (await execute('source', 'git', ['rev-parse', 'HEAD'])).text.trim();
  const dirty = (await execute('dirty', 'git', ['diff', '--name-only', 'HEAD'])).text.trim();
  assert.equal(dirty, '', 'tracked source must match the source receipt');
  const files = (await execute('files', 'git', ['ls-files', '-z'])).text.split('\0').filter(Boolean);
  const producer = workspace('producer', files);
  const consumer = workspace('consumer', files);
  const changed = workspace('changed', files);
  const failed = workspace('failed', files);
  const goContext = JSON.parse((await execute('go-context', 'go', ['env', '-json', 'GOVERSION', 'GOOS', 'GOARCH', 'GOAMD64',
    'CGO_ENABLED', 'GOFLAGS', 'GOEXPERIMENT', 'GOTOOLCHAIN', 'GOCACHEPROG'], { cwd: producer, env: goEnv(producer) })).text);
  assert.equal(goContext.GOVERSION, 'go1.25.13', 'use the repository pinned Go');
  assert.equal(goContext.GOAMD64, 'v1', 'baseline native architecture profile');
  assert.equal(goContext.GOFLAGS, '', 'baseline has no extra compiler flags or trimpath');
  assert.equal(goContext.GOEXPERIMENT, '', 'baseline has no compiler experiments');
  const profile = `linux-amd64-go1.25.13-v1-cgo${goContext.CGO_ENABLED}-actions-v1-schema1`;
  const coldBuild = await go('cold-build', producer, ['build', '-x', './internal/compatibility']);
  const coldTest = await go('cold-test', producer, ['test', '-x', '-v', '-count=1', './internal/compatibility']);
  assert.ok(coldBuild.compilerInvocations > 1);
  assert.equal(coldTest.testRuns.length, 3);
  assert.deepEqual([...coldTest.testRuns].sort(), [...coldTest.testPasses].sort());
  const socket = createServer();
  await new Promise((done, fail) => { socket.once('error', fail); socket.listen(0, '127.0.0.1', done); });
  const address = `127.0.0.1:${socket.address().port}`;
  await new Promise(done => socket.close(done));
  const config = join(root, 'local.json');
  const setup = await execute('setup', binary, ['setup', '--config', config, '--data-dir', join(root, 'local-cache'), '--listen', address,
    '--project', 'github.com/layercache/go-dogfood-qa', '--compatibility-id', profile, '--max-size', '268435456', '--non-interactive', '--json'], { cwd: root });
  const daemonStartupMs = await start(config, address);
  const key = `go-compatibility-${profile}-${source}`;
  const saved = await stock('save', producer, 'save', key, config);
  assert.ok(!existsSync(join(consumer, '.fixture-cache/value.txt')));
  const restored = await stock('restore', consumer, 'restore', key, config);
  assert.equal(saved.archiveBytes, restored.archiveBytes);
  const warmBuild = await go('warm-build', consumer, ['build', '-x', './internal/compatibility']);
  const warmTest = await go('warm-test', consumer, ['test', '-x', '-v', '-count=1', './internal/compatibility']);
  assert.ok(warmBuild.compilerInvocations < coldBuild.compilerInvocations);
  assert.ok(warmTest.compilerInvocations < coldTest.compilerInvocations);
  assert.deepEqual([...warmTest.testRuns].sort(), [...coldTest.testRuns].sort());
  assert.deepEqual([...warmTest.testPasses].sort(), [...coldTest.testPasses].sort());
  await stock('changed-restore', changed, 'restore', key, config);
  await go('changed-relocated-build', changed, ['build', '-x', './internal/compatibility']);
  const unchanged = await go('changed-unchanged-build', changed, ['build', '-x', './internal/compatibility']);
  assert.equal(unchanged.compilerInvocations, 0);
  const sourcePath = join(changed, 'internal/compatibility/identity.go');
  const before = readFileSync(sourcePath, 'utf8');
  const after = before.replace('MaxLength = 256', 'MaxLength = 255');
  assert.notEqual(after, before);
  writeFileSync(sourcePath, after);
  const changedBuild = await go('changed-source-build', changed, ['build', '-x', './internal/compatibility']);
  assert.ok(changedBuild.compilerInvocations > 0);
  await stock('failure-restore', failed, 'restore', key, config);
  writeFileSync(join(failed, 'internal/compatibility/dogfood_failure_test.go'),
    'package compatibility\nimport "testing"\nfunc TestDogfoodFreshFailure(t *testing.T) { t.Fatal("intentional fresh failure") }\n');
  for (let index = 1; index <= 2; index++) {
    const failure = await go(`failed-test-${index}`, failed, ['test', '-x', '-v', '-count=1', './internal/compatibility'], { allowFailure: true });
    assert.equal(failure.status, 1);
    assert.ok(failure.testRuns.includes('TestDogfoodFreshFailure'));
    assert.ok(!failure.testPasses.includes('TestDogfoodFreshFailure'));
  }
  const compilerProfile = await go('compiler-profile-build', consumer, ['build', '-x', './internal/compatibility'], { env: { GOAMD64: 'v2' } });
  assert.ok(compilerProfile.compilerInvocations > warmBuild.compilerInvocations);
  const incompatible = join(root, 'incompatible.json');
  writeFileSync(incompatible, JSON.stringify({ ...JSON.parse(readFileSync(config)), compatibilityId: `${profile}-different` }), { mode: 0o600 });
  const profileProbe = join(root, 'profile-probe.mjs');
  writeFileSync(profileProbe, `import {pathToFileURL} from 'node:url';
import assert from 'node:assert/strict';
const {restoreCache} = await import(pathToFileURL(process.argv[2]));
process.env.GITHUB_WORKSPACE = process.cwd();
const value = await restoreCache(['.fixture-cache'], process.argv[3], []);
assert.equal(value, undefined); console.log('profile-miss');\n`, { mode: 0o600 });
  const profileMiss = await execute('profile-miss', binary, ['run', '--config', incompatible, '--', process.execPath, profileProbe, client, key],
    { cwd: consumer, env: { ...env, RUNNER_TEMP: join(consumer, '.runner-temp'), RUNNER_OS: 'Linux', GITHUB_REF: 'refs/heads/main' } });
  assert.ok(profileMiss.text.includes('profile-miss'));
  stages.push({ label: 'profile-miss', status: profileMiss.status, commandWallMs: profileMiss.commandWallMs, miss: true });
  receipt = { passed: true, source, binaryDigest: digest(readFileSync(binary)), runnerDigest: digest(readFileSync(fileURLToPath(import.meta.url))),
    stockDriverDigest: digest(readFileSync(join(repo, 'qa/stock-actions-cache.mjs'))), stockActionsCacheVersion: clientVersion,
    goContext, profile, nodeVersion: process.version, setupMs: setup.commandWallMs, daemonStartupMs, commandCount: commandIndex,
    coldBuildAndFreshTestMs: coldBuild.commandWallMs + coldTest.commandWallMs,
    restoreBuildAndFreshTestMs: restored.commandWallMs + warmBuild.commandWallMs + warmTest.commandWallMs,
    firstProducerWithPublicationMs: coldBuild.commandWallMs + coldTest.commandWallMs + saved.commandWallMs,
    stages, testsAlwaysFresh: true, hostCacheDirectoriesArchived: false, bootstrapIncluded: false,
    qualification: 'loopback Actions v1 archive of private Go compiler cache; stdlib-only package; no hosted or Phone claim' };
} finally {
  try {
    await stopChildren(processes);
  } finally {
    try {
      await finishSignals();
    } finally {
      // Default runs retain no credentials, compiler state or subprocess logs,
      // including when a child requires forced shutdown.
      if (process.env.KEEP_GO_DOGFOOD_QA !== '1') rmSync(root, { recursive: true, force: true });
    }
  }
}
receipt.totalWallMs = Math.round(performance.now() - started);
receipt.cleanup = { processesStopped: true, privateStateRemoved: process.env.KEEP_GO_DOGFOOD_QA !== '1' };
if (receiptPath) {
  const temporary = join(dirname(receiptPath), `.layercache-go-receipt-${randomUUID()}.tmp`);
  try {
    writeFileSync(temporary, JSON.stringify(receipt, null, 2) + '\n', { mode: 0o600, flag: 'wx' });
    renameSync(temporary, receiptPath);
  } finally {
    rmSync(temporary, { force: true });
  }
}
if (process.env.KEEP_GO_DOGFOOD_QA === '1') console.log(`Private QA state: ${root}`);
console.log(JSON.stringify(receipt, null, 2));
