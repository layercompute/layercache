// The real pinned Gradle client owns keys, task execution and opaque archives.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { createServer as createHttpServer, request as requestHttp } from 'node:http';
import { chmodSync, closeSync, cpSync, existsSync, mkdirSync, mkdtempSync, openSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { basename, dirname, isAbsolute, join, resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import { cleanupOnSignals, stopChild, stopChildren, trackChild } from '../nx-native/processes.mjs';

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const fixture = join(repo, 'qa/fixtures/gradle-native');
const init = join(repo, 'native/gradle/init.gradle');
const root = mkdtempSync(join(tmpdir(), 'layercache-gradle-native-'));
chmodSync(root, 0o700);
const started = performance.now();
const gradle = process.env.GRADLE_BIN ?? 'gradle';
let profile;
const project = 'github.com/layercache/gradle-native-qa';
const processes = [];
const cancellation = new AbortController();
const finishSignals = cleanupOnSignals(processes, cancellation);
const stages = [];
const proxies = [];
let completed;
let commandIndex = 0;
let binary = process.env.LAYERCACHE_BIN;
const tokenBinary = join(root, 'mint-token');
const environment = { ...process.env, CI: 'true', TERM: 'dumb', NO_COLOR: '1',
  XDG_CONFIG_HOME: join(root, 'config'), XDG_CACHE_HOME: join(root, 'cache') };
for (const key of Object.keys(environment)) {
  if (key.startsWith('LAYER_CACHE_') || key.startsWith('ORG_GRADLE_PROJECT_') || key.startsWith('GRADLE_')
      || ['JAVA_OPTS', 'JAVA_TOOL_OPTIONS', 'JDK_JAVA_OPTIONS', '_JAVA_OPTIONS'].includes(key)) delete environment[key];
}

function digest(bytes) { return `sha256:${createHash('sha256').update(bytes).digest('hex')}`; }

async function execute(program, args, options = {}) {
  cancellation.signal.throwIfAborted();
  const begin = performance.now();
  const log = join(root, `command-${++commandIndex}.log`);
  const child = trackChild(spawn(program, args, { cwd: options.cwd ?? repo, env: options.env ?? environment,
    detached: process.platform !== 'win32', stdio: ['ignore', 'pipe', 'pipe'] }));
  processes.push(child);
  return new Promise((done, fail) => {
    const chunks = [];
    let bytes = 0;
    let failure;
    const abort = error => { failure ??= error; void stopChild(child).catch(() => {}); };
    const timeout = setTimeout(() => abort(new Error(`${basename(program)} exceeded 120 seconds; inspect ${root}`)), 120_000);
    for (const stream of [child.stdout, child.stderr]) stream.on('data', chunk => {
      bytes += chunk.length;
      if (bytes > 8 * 1024 * 1024) abort(new Error(`${basename(program)} exceeded the output budget; inspect ${root}`));
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
      else if (!options.allowFailure && status !== 0) fail(new Error(`${basename(program)} exited ${status}; inspect ${log}`));
      else done(result);
    });
  });
}

async function freeAddress() {
  const socket = createServer();
  await new Promise((done, fail) => { socket.once('error', fail); socket.listen(0, '127.0.0.1', done); });
  const address = socket.address();
  await new Promise((done, fail) => socket.close(error => error ? fail(error) : done()));
  return `127.0.0.1:${address.port}`;
}

async function start(runtime) {
  const log = openSync(join(root, `${runtime.name}-daemon.log`), 'a', 0o600);
  const child = trackChild(spawn(binary, ['serve', '--config', runtime.path], {
    cwd: repo, env: environment, detached: process.platform !== 'win32', stdio: ['ignore', log, log],
  }));
  closeSync(log);
  processes.push(child);
  runtime.child = child;
  let error;
  child.once('error', value => { error = value; });
  for (let attempt = 0; attempt < 100; attempt++) {
    cancellation.signal.throwIfAborted();
    if (error) throw error;
    if (child.exitCode !== null) throw new Error(`${runtime.name} exited; inspect ${root}`);
    try {
      const response = await fetch(`${runtime.url}/healthz`, { signal: AbortSignal.any([cancellation.signal, AbortSignal.timeout(250)]) });
      if (response.ok) return runtime;
    } catch {}
    await delay(50);
  }
  throw new Error(`${runtime.name} did not become healthy; inspect ${root}`);
}

async function setup(name, role, maxBytes = 64 * 1024 * 1024) {
  const path = join(root, `${name}.json`);
  const address = await freeAddress();
  await execute(binary, ['setup', '--config', path, '--data-dir', join(root, `${name}-cache`), '--listen', address,
    '--role', role, '--project', project, '--compatibility-id', profile, '--max-size', String(maxBytes), '--non-interactive', '--json']);
  const config = JSON.parse(readFileSync(path, 'utf8'));
  config.teamMembers = { qa: 'writer' };
  writeFileSync(path, JSON.stringify(config), { mode: 0o600 });
  const runtime = await start({ name, path, config, url: `http://${address}`, exchanges: [] });
  // Record only protocol facts. Headers, credentials, bodies and paths never
  // enter exported evidence. Forced misses exercise genuine duplicate PUTs.
  const proxy = createHttpServer((request, response) => {
    const row = { method: request.method, hash: request.url.split('/').at(-1), uploadBytes: 0, downloadBytes: 0 };
    const uploadHash = createHash('sha256');
    assert.match(row.hash, /^[a-f0-9]{32}$/);
    runtime.exchanges.push(row);
    if (runtime.forceMiss && request.method === 'GET') {
      row.status = 404;
      row.forcedMiss = true;
      request.resume();
      response.writeHead(404).end();
      return;
    }
    const upstream = requestHttp(new URL(request.url, runtime.url), { method: request.method, headers: request.headers }, reply => {
      row.status = reply.statusCode;
      response.writeHead(reply.statusCode, reply.headers);
      reply.on('data', chunk => { row.downloadBytes += chunk.length; });
      reply.on('error', () => response.destroy());
      reply.pipe(response);
    });
    request.on('data', chunk => { row.uploadBytes += chunk.length; uploadHash.update(chunk); });
    request.on('end', () => { if (row.method === 'PUT') row.uploadDigest = `sha256:${uploadHash.digest('hex')}`; });
    request.on('aborted', () => upstream.destroy());
    upstream.on('error', () => {
      row.status = 503;
      if (!response.headersSent) response.writeHead(503).end();
      else response.destroy();
    });
    request.pipe(upstream);
  });
  proxies.push(proxy);
  await new Promise((done, fail) => { proxy.once('error', fail); proxy.listen(0, '127.0.0.1', done); });
  runtime.proxyUrl = `http://127.0.0.1:${proxy.address().port}`;
  return runtime;
}

async function mint(runtime, capability = 'read', compatibility = profile) {
  return (await execute(tokenBinary, ['--config', runtime.path, '--capability', capability, '--compatibility', compatibility])).text.trim();
}

function workspace(name) {
  const path = join(root, name);
  cpSync(fixture, path, { recursive: true, filter: source => !source.slice(fixture.length).split('/').some(part => ['build', '.gradle'].includes(part)) });
  const home = join(root, `${name}-gradle-home`);
  mkdirSync(home, { mode: 0o700 });
  assert.ok(!existsSync(join(path, '.gradle')) && !existsSync(join(path, 'build')));
  assert.deepEqual([...readFileSync(join(path, 'settings.gradle'))], [...readFileSync(join(fixture, 'settings.gradle'))]);
  return { path, home, counter: join(root, `${name}-executions.txt`) };
}

function counts(work) {
  const lines = existsSync(work.counter) ? readFileSync(work.counter, 'utf8').trim().split('\n') : [];
  return Object.fromEntries(['compileJava', 'verifyFixture'].map(task => [task, lines.filter(line => line === task).length]));
}

function clear(work) {
  rmSync(join(work.path, 'build'), { recursive: true, force: true });
  rmSync(join(work.path, '.gradle'), { recursive: true, force: true });
}

function outputDigests(work) {
  return Object.fromEntries(['classes/java/main/example/Hello.class', 'verification/result.txt'].map(path => [path, digest(readFileSync(join(work.path, 'build', path)))]));
}

async function run(name, work, runtime, token, options = {}) {
  const env = { ...environment, GRADLE_USER_HOME: work.home, GRADLE_FIXTURE_COUNTER: work.counter,
    GRADLE_FIXTURE_EXPECTED: options.expected ?? 'hello baseline', GRADLE_FIXTURE_FAIL: options.fail ? '1' : '0',
    LAYER_CACHE_GRADLE_LOCAL_ENABLED: '0', ...options.env };
  if (runtime) {
    env.LAYER_CACHE_GRADLE_URL = `${runtime.proxyUrl}/v1/gradle-cache/`;
    env.LAYER_CACHE_GRADLE_TOKEN = token;
    if (options.push) env.LAYER_CACHE_GRADLE_PUSH = '1';
  }
  const before = counts(work);
  const exchangeStart = runtime?.exchanges.length ?? 0;
  const result = await execute(gradle, ['--no-daemon', '--no-configuration-cache', '--console=plain', '--info',
    '--max-workers=2', options.bypass ? '--no-build-cache' : '--build-cache',
    '-Dorg.gradle.java.installations.auto-download=false', '-Dorg.gradle.internal.http.connectionTimeout=1000',
    '-Dorg.gradle.internal.http.socketTimeout=1500', '-I', init, ...(options.args ?? []), 'verifyFixture'],
  { cwd: work.path, env, allowFailure: options.expectFailure });
  if (env.LAYER_CACHE_GRADLE_TOKEN) assert.ok(!result.text.includes(env.LAYER_CACHE_GRADLE_TOKEN), `${name} never prints the credential`);
  assert.equal(result.status !== 0, !!options.expectFailure, `${name} exit status`);
  const tasks = {};
  for (const task of ['compileJava', 'verifyFixture']) {
    const hash = result.text.match(new RegExp(`Build cache key for task ':${task}' is ([a-f0-9]+)`))?.[1];
    const outcome = result.text.match(new RegExp(`> Task :${task}(?: ([A-Z-]+))?(?:\\r?\\n|$)`))?.[1] ?? 'EXECUTED';
    tasks[task] = { ...(hash ? { hash } : {}), outcome, executions: counts(work)[task] - before[task] };
  }
  const stage = { name, status: result.status, commandWallMs: result.commandWallMs, tasks,
    requests: runtime?.exchanges.slice(exchangeStart) ?? [],
    remoteDisabled: result.text.includes('remote build cache was disabled'),
    ...(options.expectFailure ? {} : { outputDigests: outputDigests(work) }) };
  stages.push(stage);
  console.log(`${name}: ${Object.entries(tasks).map(([task, row]) => `${task} ${row.outcome}`).join(', ')}`);
  return { stage, text: result.text };
}

function assertHit(result, hashes, expectedDigests) {
  for (const task of ['compileJava', 'verifyFixture']) {
    assert.equal(result.stage.tasks[task].hash, hashes[task]);
    assert.equal(result.stage.tasks[task].outcome, 'FROM-CACHE');
    assert.equal(result.stage.tasks[task].executions, 0);
  }
  assert.deepEqual(result.stage.outputDigests, expectedDigests);
}

async function archive(runtime, token, hash, status = 200) {
  const response = await fetch(`${runtime.url}/v1/gradle-cache/${hash}`, { headers: { Authorization: `Basic ${Buffer.from(`layercache:${token}`).toString('base64')}` }, signal: AbortSignal.timeout(3000) });
  assert.equal(response.status, status);
  const bytes = Buffer.from(await response.arrayBuffer());
  return { size: bytes.length, digest: digest(bytes) };
}

try {
  const version = await execute(gradle, ['--version'], { env: { ...environment, GRADLE_USER_HOME: join(root, 'version-home') } });
  assert.match(version.text, /Gradle 9\.3\.1(?:\s|$)/, 'GRADLE_BIN must run pinned Gradle 9.3.1');
  const java = await execute('java', ['-version']);
  assert.match(java.text, /version "17\./, 'the fixture requires the pinned Java 17 language toolchain');
  profile = `${process.platform}-${process.arch}-jdk17-${digest(java.text).slice(7, 19)}-gradle931-schema1`;
  if (binary) assert.ok(isAbsolute(binary), 'LAYERCACHE_BIN must be absolute');
  else {
    binary = join(root, 'layercache');
    await execute('go', ['build', '-trimpath', '-o', binary, './cmd/layercache']);
  }
  await execute('go', ['build', '-trimpath', '-o', tokenBinary, './qa/gradle-native/token']);
  const sourceRevision = (await execute('git', ['rev-parse', 'HEAD'])).text.trim();
  const binaryDigest = digest(readFileSync(binary));
  const local = await setup('local', 'local');
  const team = await setup('team', 'team');
  const localWriter = await mint(local, 'write');
  const teamWriter = await mint(team, 'write');
  const teamReader = await mint(team);
  const setupWallMs = Math.round(performance.now() - started);

  const producer = workspace('local-producer');
  const cold = await run('local-cold', producer, local, localWriter, { push: true });
  const hashes = Object.fromEntries(Object.entries(cold.stage.tasks).map(([task, row]) => [task, row.hash]));
  for (const row of Object.values(cold.stage.tasks)) { assert.equal(row.executions, 1); assert.match(row.hash, /^[a-f0-9]{32}$/); }
  const expectedDigests = cold.stage.outputDigests;
  const localArchives = {};
  for (const [task, hash] of Object.entries(hashes)) localArchives[task] = await archive(local, localWriter, hash);
  assertHit(await run('local-empty-consumer', workspace('local-consumer'), local, localWriter), hashes, expectedDigests);
  await stopChild(local.child);
  await start(local);
  assertHit(await run('local-restart-consumer', workspace('local-restart-consumer'), local, localWriter), hashes, expectedDigests);

  const direct = workspace('team-producer');
  const publication = await run('team-cold', direct, team, teamWriter, { push: true });
  for (const task of Object.keys(hashes)) { assert.equal(publication.stage.tasks[task].hash, hashes[task]); assert.equal(publication.stage.tasks[task].executions, 1); }
  const teamArchives = {};
  for (const [task, hash] of Object.entries(hashes)) teamArchives[task] = await archive(team, teamReader, hash);
  assertHit(await run('team-empty-reader', workspace('team-reader'), team, teamReader), hashes, expectedDigests);

  team.forceMiss = true;
  const duplicate = await run('duplicate-first-winner', workspace('duplicate-producer'), team, teamWriter, { push: true });
  team.forceMiss = false;
  for (const [task, hash] of Object.entries(hashes)) {
    assert.equal(duplicate.stage.tasks[task].executions, 1);
    assert.equal(duplicate.stage.tasks[task].hash, hash);
    assert.deepEqual(await archive(team, teamReader, hash), teamArchives[task]);
    const upload = duplicate.stage.requests.find(row => row.method === 'PUT' && row.hash === hash);
    assert.match(upload.uploadDigest, /^sha256:[a-f0-9]{64}$/);
    assert.notEqual(upload.uploadDigest, teamArchives[task].digest, 'new native origin metadata changes archive bytes');
  }
  assert.equal(duplicate.stage.requests.filter(row => row.method === 'PUT' && row.status === 200).length, 2);

  const inputMiss = await run('declared-input-miss', workspace('input-miss'), team, teamReader,
    { env: { GRADLE_FIXTURE_LABEL: 'changed' } });
  assert.equal(inputMiss.stage.tasks.compileJava.outcome, 'FROM-CACHE');
  assert.notEqual(inputMiss.stage.tasks.verifyFixture.hash, hashes.verifyFixture);
  assert.equal(inputMiss.stage.tasks.verifyFixture.executions, 1);

  const changed = workspace('source-miss');
  const javaSource = join(changed.path, 'src/main/java/example/Hello.java');
  writeFileSync(javaSource, readFileSync(javaSource, 'utf8').replace('hello baseline', 'hello changed'));
  const sourceMiss = await run('changed-source-miss', changed, team, teamReader, { expected: 'hello changed' });
  for (const task of Object.keys(hashes)) { assert.notEqual(sourceMiss.stage.tasks[task].hash, hashes[task]); assert.equal(sourceMiss.stage.tasks[task].executions, 1); }

  const releaseMiss = await run('compiler-option-miss', workspace('compiler-option-miss'), team, teamReader, { args: ['-PfixtureRelease=11'] });
  assert.notEqual(releaseMiss.stage.tasks.compileJava.hash, hashes.compileJava);
  assert.equal(releaseMiss.stage.tasks.compileJava.executions, 1);
  const profileToken = await mint(team, 'read', `${profile}-incompatible`);
  const profileMiss = await run('profile-isolation-miss', workspace('profile-miss'), team, profileToken);
  for (const task of Object.keys(hashes)) { assert.equal(profileMiss.stage.tasks[task].hash, hashes[task]); assert.equal(profileMiss.stage.tasks[task].executions, 1); }

  const failureWork = workspace('failed-task');
  let failedHash;
  for (let attempt = 1; attempt <= 2; attempt++) {
    clear(failureWork);
    const failed = await run(`failed-task-rerun-${attempt}`, failureWork, team, teamWriter, { fail: true, push: true, expectFailure: true });
    assert.equal(failed.stage.tasks.compileJava.outcome, 'FROM-CACHE');
    assert.equal(failed.stage.tasks.verifyFixture.outcome, 'FAILED');
    assert.equal(failed.stage.tasks.verifyFixture.executions, 1);
    failedHash ??= failed.stage.tasks.verifyFixture.hash;
    assert.equal(failed.stage.tasks.verifyFixture.hash, failedHash, 'the same failed native identity executes again');
    await archive(team, teamReader, failed.stage.tasks.verifyFixture.hash, 404);
  }

  const readonly = workspace('readonly-publication');
  const denied = await run('readonly-publication-rejected', readonly, team, profileToken, { push: true });
  assert.equal(denied.stage.remoteDisabled, true);
  assert.ok(denied.stage.requests.some(row => row.method === 'PUT' && row.status === 403));
  for (const hash of Object.values(hashes)) await archive(team, profileToken, hash, 404);

  const bypass = await run('explicit-fresh-bypass', workspace('fresh-bypass'), team, teamWriter, { push: true, bypass: true });
  for (const row of Object.values(bypass.stage.tasks)) { assert.equal(row.executions, 1); assert.equal(row.hash, undefined); }
  assert.deepEqual(bypass.stage.requests, [], '--no-build-cache sends zero cache HTTP requests');

  const existing = workspace('existing-remote');
  writeFileSync(join(existing.path, 'settings.gradle'), readFileSync(join(existing.path, 'settings.gradle'), 'utf8') +
    "\nbuildCache { remote(org.gradle.caching.http.HttpBuildCache) { url = 'http://127.0.0.1:1/cache/'; allowInsecureProtocol = true } }\n");
  const conflict = await run('existing-remote-rejected', existing, team, teamReader, { expectFailure: true });
  assert.match(conflict.text, /cannot replace an existing remote Gradle build cache/);
  assert.deepEqual(conflict.stage.requests, []);
  const untouched = await run('unconfigured-script-preserves-remote', existing, undefined, undefined, { bypass: true });
  for (const row of Object.values(untouched.stage.tasks)) assert.equal(row.executions, 1);

  const partial = await run('partial-environment-rejected', workspace('partial-environment'), undefined, undefined,
    { env: { LAYER_CACHE_GRADLE_TOKEN: teamReader }, expectFailure: true });
  assert.match(partial.text, /requires both LAYER_CACHE_GRADLE_URL and LAYER_CACHE_GRADLE_TOKEN/);
  const unsafe = await run('unsafe-http-rejected', workspace('unsafe-http'), undefined, undefined,
    { env: { LAYER_CACHE_GRADLE_URL: 'http://cache.example.invalid/', LAYER_CACHE_GRADLE_TOKEN: teamReader }, expectFailure: true });
  assert.match(unsafe.text, /must be an HTTPS or loopback HTTP URL/);

  const quota = await setup('quota', 'team', 128);
  const quotaToken = await mint(quota, 'write');
  const pressured = await run('quota-pressure', workspace('quota-pressure'), quota, quotaToken, { push: true });
  assert.match(pressured.text, /response status 413/);
  assert.equal(pressured.stage.remoteDisabled, true);
  for (const hash of Object.values(hashes)) await archive(quota, quotaToken, hash, 404);

  await stopChild(team.child);
  const outage = await run('private-endpoint-offline', workspace('outage'), team, teamReader);
  assert.equal(outage.stage.remoteDisabled, true);
  for (const row of Object.values(outage.stage.tasks)) assert.equal(row.executions, 1);
  await start(team);
  assertHit(await run('private-restart-reader', workspace('restart-reader'), team, teamReader), hashes, expectedDigests);

  await stopChild(team.child);
  team.config.teamMembers = {};
  writeFileSync(team.path, JSON.stringify(team.config), { mode: 0o600 });
  await start(team);
  const revoked = await run('revoked-member', workspace('revoked-member'), team, teamReader);
  assert.equal(revoked.stage.remoteDisabled, true);
  assert.match(revoked.text, /response status 401/);
  for (const row of Object.values(revoked.stage.tasks)) assert.equal(row.executions, 1);

  completed = { schemaVersion: 'gradle-native-v1', passed: true, sourceRevision, binaryDigest, initScriptDigest: digest(readFileSync(init)),
    gradleVersion: '9.3.1', javaVersion: java.text.match(/version "([^"]+)"/)[1], profile,
    setupWallMs, hashes, localArchives, teamArchives, outputDigests: expectedDigests, stages,
    actualDifferentJdkQualified: false, phoneAndroidBuildQualified: false,
    gradleFilesystemCacheBoundedByLayerCache: false, localToPrivateForwardingImplemented: false };
  completed.nativeTransferBytes = stages.flatMap(stage => stage.requests).reduce((sum, row) => ({
    upload: sum.upload + row.uploadBytes, download: sum.download + row.downloadBytes,
  }), { upload: 0, download: 0 });
} finally {
  try { await stopChildren(processes); } finally {
    for (const proxy of proxies) {
      proxy.closeAllConnections();
      await new Promise((done, fail) => proxy.close(error => error ? fail(error) : done()));
    }
    await finishSignals();
  }
}

completed.qualificationWallMs = Math.round(performance.now() - started);
const receipt = JSON.stringify(completed, null, 2) + '\n';
writeFileSync(join(root, 'evidence.json'), receipt, { mode: 0o600 });
if (process.env.GRADLE_QA_OUTPUT) {
  assert.ok(isAbsolute(process.env.GRADLE_QA_OUTPUT), 'GRADLE_QA_OUTPUT must be an absolute new path');
  const file = openSync(process.env.GRADLE_QA_OUTPUT, 'wx', 0o600);
  try { writeFileSync(file, receipt); } finally { closeSync(file); }
}
console.log(`Gradle native acceptance passed. Private runtime directory: ${root}`);
