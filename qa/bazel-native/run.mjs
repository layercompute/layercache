// Real pinned Bazel client against an isolated LayerCache gateway and bazel-remote.
// No language rule downloads, application builds, or operator services are used.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { createServer, request as requestHTTP } from 'node:http';
import { chmodSync, closeSync, cpSync, existsSync, lstatSync, mkdtempSync,
  openSync, readFileSync, readdirSync, rmSync, statfsSync, writeFileSync } from 'node:fs';
import { createConnection } from 'node:net';
import { tmpdir } from 'node:os';
import { basename, dirname, isAbsolute, join, resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import { cleanupOnSignals, stopChild, stopChildren, trackChild } from '../nx-native/processes.mjs';

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const root = mkdtempSync(join(tmpdir(), 'layercache-bazel-native-'));
chmodSync(root, 0o700);
const pins = {
  bazel: { version: '9.3.0', sha256: 'd302d22ed77ee5658b87857249baa579db84300ecebe0a1e9a357b5b20f80eb8',
    url: 'https://github.com/bazelbuild/bazel/releases/download/9.3.0/bazel-9.3.0-linux-x86_64' },
  engine: { version: '2.6.3', source: '985cc081b1d5299e9d651ab01a605362f6bb170c',
    sha256: '65427624c288e29b17c7b0e189cf1d03ae1d8745354d9effc432f75c4361159a',
    url: 'https://github.com/buchgr/bazel-remote/releases/download/v2.6.3/bazel-remote-2.6.3-linux-amd64' },
};
const children = [];
const abort = new AbortController();
const finishSignals = cleanupOnSignals(children, abort);
const stages = [];
const exchanges = [];
const secrets = [];
const environment = { ...process.env, CI: 'true',
  XDG_CONFIG_HOME: join(root, 'config'), XDG_CACHE_HOME: join(root, 'cache') };
for (const key of Object.keys(environment)) {
  if (key.startsWith('LAYER_CACHE_') || key.startsWith('BAZEL_')
      || ['JAVA_TOOL_OPTIONS', 'JDK_JAVA_OPTIONS', '_JAVA_OPTIONS'].includes(key)) delete environment[key];
}
const project = 'github.com/layercache/bazel-native-qa';
const profile = 'linux-amd64-bazel9-fixture-v1';
const pressure = process.env.BAZEL_QA_PRESSURE === '1';
const scratchBudget = pressure ? 1.5 * 1024 ** 3 : 1024 ** 3;
const configPath = join(root, 'gateway.json');
let binary = process.env.LAYERCACHE_BIN;
let engine;
let gateway;
let observer;
let observedURL;
let config;
let evidence;
let sequence = 0;
const clean = text => secrets.reduce((value, secret) => value.replaceAll(secret, '[redacted]'), text);
const sha = bytes => createHash('sha256').update(bytes).digest('hex');

async function execute(program, args, options = {}) {
  abort.signal.throwIfAborted();
  const child = trackChild(spawn(program, args, { cwd: options.cwd ?? repo, env: environment,
    detached: true, stdio: ['ignore', 'pipe', 'pipe'] }));
  children.push(child);
  return new Promise((done, fail) => {
    const parts = [];
    let size = 0;
    let error;
    const stop = reason => { error ??= reason; void stopChild(child).catch(() => {}); };
    const timer = setTimeout(() => stop(new Error(`${basename(program)} exceeded 120 seconds`)), 120_000);
    for (const stream of [child.stdout, child.stderr]) stream.on('data', chunk => {
      size += chunk.length;
      if (size > 4 * 1024 * 1024) stop(new Error('QA command exceeded output budget'));
      else parts.push(chunk);
    });
    child.once('error', stop);
    child.once('close', status => {
      clearTimeout(timer);
      const text = clean(Buffer.concat(parts).toString());
      if (!options.sensitive) writeFileSync(join(root, `command-${sequence++}.log`), text, { mode: 0o600 });
      if (abort.signal.aborted) fail(abort.signal.reason);
      else if (error) fail(error);
      else if (!options.allowFailure && status !== 0) fail(new Error(`${basename(program)} failed (${status})\n${text.slice(-8000)}`));
      else done({ status, text });
    });
  });
}

async function tool(name, override) {
  const pin = pins[name];
  const path = override ?? join(root, name);
  assert.ok(isAbsolute(path), `${name} path must be absolute`);
  if (!override) {
    const response = await fetch(pin.url, { signal: AbortSignal.timeout(60_000) });
    assert.equal(response.status, 200);
    const bytes = Buffer.from(await response.arrayBuffer());
    assert.ok(bytes.length < 100 * 1024 * 1024, 'release download bounded');
    assert.equal(sha(bytes), pin.sha256, `${name} official pinned release digest`);
    writeFileSync(path, bytes, { mode: 0o700 });
  }
  assert.equal(sha(readFileSync(path)), pin.sha256, `${name} override must match the pinned release digest`);
  return path;
}

async function address() {
  const server = createServer();
  await new Promise(done => server.listen(0, '127.0.0.1', done));
  const value = `127.0.0.1:${server.address().port}`;
  await new Promise((done, fail) => server.close(error => error ? fail(error) : done()));
  return value;
}

async function launch(program, args, name, health) {
  const log = openSync(join(root, `${name}.log`), 'a', 0o600);
  const child = trackChild(spawn(program, args, { cwd: repo, env: environment, detached: true,
    stdio: ['ignore', log, log] }));
  closeSync(log);
  children.push(child);
  let error;
  child.once('error', value => { error = value; });
  for (let i = 0; i < 100; i++) {
    if (error) throw error;
    if (child.exitCode !== null) throw new Error(`${name} exited: ${clean(readFileSync(join(root, `${name}.log`), 'utf8')).slice(-4000)}`);
    try {
      const response = await fetch(health, { signal: AbortSignal.timeout(250) });
      if (response.ok) return child;
    } catch {}
    await delay(50);
  }
  throw new Error(`${name} startup failed`);
}

async function observe(upstream) {
  observer = createServer((request, response) => {
    const row = { method: request.method, path: request.url, status: null, uploadBytes: 0, downloadBytes: 0 };
    exchanges.push(row);
    const started = performance.now();
    const target = requestHTTP(new URL(request.url, upstream), { method: request.method, headers: request.headers }, reply => {
      row.status = reply.statusCode;
      row.responseHeadersMs = performance.now() - started;
      response.writeHead(reply.statusCode, reply.headers);
      reply.on('data', chunk => { row.downloadBytes += chunk.length; });
      reply.on('end', () => { row.completeMs = performance.now() - started; });
      reply.on('error', () => response.destroy());
      reply.pipe(response);
    });
    request.on('data', chunk => { row.uploadBytes += chunk.length; });
    request.on('aborted', () => target.destroy());
    target.on('error', () => {
      row.status = 502;
      if (!response.headersSent) response.writeHead(502).end();
      else response.destroy();
    });
    request.pipe(target);
  });
  await new Promise(done => observer.listen(0, '127.0.0.1', done));
  return `http://127.0.0.1:${observer.address().port}`;
}

async function token(options = {}) {
  const args = ['--config', configPath];
  for (const [name, value] of Object.entries(options)) args.push(`--${name}`, value);
  const value = (await execute(join(root, 'mint-token'), args, { sensitive: true })).text.trim();
  secrets.push(value);
  return value;
}

function allocated(path) {
  if (!existsSync(path)) return 0;
  const seen = new Set();
  function walk(file) {
    const stat = lstatSync(file);
    if (stat.isSymbolicLink()) return stat.blocks * 512;
    const key = `${stat.dev}:${stat.ino}`;
    if (seen.has(key)) return 0;
    seen.add(key);
    return stat.blocks * 512 + (stat.isDirectory() ? readdirSync(file).reduce((sum, name) => sum + walk(join(file, name)), 0) : 0);
  }
  return walk(path);
}

function summary(rows) {
  return { requests: rows.length, puts: rows.filter(row => row.method === 'PUT').length,
    acPuts: rows.filter(row => row.method === 'PUT' && row.path.includes('/ac/')).length,
    casPuts: rows.filter(row => row.method === 'PUT' && row.path.includes('/cas/')).length,
    acHits: rows.filter(row => row.method === 'GET' && row.path.includes('/ac/') && row.status === 200).length,
    casHits: rows.filter(row => row.method === 'GET' && row.path.includes('/cas/') && row.status === 200).length,
    uploadBytes: rows.reduce((sum, row) => sum + row.uploadBytes, 0),
    downloadBytes: rows.reduce((sum, row) => sum + row.downloadBytes, 0),
    statuses: Object.fromEntries([...new Set(rows.map(row => row.status))].map(status => [status, rows.filter(row => row.status === status).length])),
    responseHeadersMs: rows.reduce((sum, row) => sum + (row.responseHeadersMs ?? 0), 0),
    requestCompleteMs: rows.reduce((sum, row) => sum + (row.completeMs ?? 0), 0) };
}

function workspace(name) {
  const path = join(root, name);
  cpSync(join(repo, 'qa/fixtures/bazel-native'), path, { recursive: true });
  return path;
}

async function bazelRun(name, auth, options = {}) {
  const work = options.work ?? workspace(name);
  const output = join(root, `${name}-output`);
  assert.ok(!existsSync(output), 'independent native output root starts empty');
  const bep = join(root, `${name}-events.jsonl`);
  const begin = exchanges.length;
  const started = performance.now();
  const result = await execute(bazel, ['--batch', `--install_base=${join(root, 'install')}`,
    `--output_user_root=${output}`, '--host_jvm_args=-Xmx384m', '--ignore_all_rc_files',
    options.test ? 'test' : 'build', options.test ? '//:failure' : '//:message',
    '--jobs=1', '--local_resources=memory=512', '--noshow_progress', '--color=no', '--curses=no',
    `--repository_cache=${join(root, 'repository-cache')}`, '--disk_cache=',
    `--remote_cache=${observedURL}/v1/bazel-cache`, `--remote_header=Authorization=Bearer ${auth}`,
    `--remote_upload_local_results=${options.write ? 'true' : 'false'}`, '--remote_download_outputs=all',
    '--remote_timeout=10s', '--remote_retries=0', '--spawn_strategy=local', '--test_strategy=standalone',
    `--action_env=FIXTURE_ENV=${options.env ?? 'baseline'}`, `--build_event_json_file=${bep}`,
    ...(options.test ? [`--test_env=FIXTURE_COUNTER=${join(root, 'failed-test-count')}`, '--test_output=errors'] : []),
  ], { cwd: work, allowFailure: options.test });
  const events = readFileSync(bep, 'utf8').trim().split('\n').map(line => JSON.parse(line));
  const runners = events.find(event => event.buildMetrics)?.buildMetrics?.actionSummary?.runnerCount ?? [];
  const rows = exchanges.slice(begin);
  const stage = { name, status: result.status, commandMs: performance.now() - started,
    nativeRunners: runners, http: summary(rows), emptyOutputRoot: true };
  if (options.test) stage.nativeTestResults = events.filter(event => event.testResult).map(event => ({
    status: event.testResult.status, cachedLocally: event.testResult.cachedLocally ?? false,
    cachedRemotely: event.testResult.executionInfo?.cachedRemotely ?? false,
  }));
  if (!options.test) {
    const bytes = readFileSync(join(work, 'bazel-bin/message.txt'));
    stage.outputSha256 = sha(bytes);
    stage.outputBytes = bytes.length;
  }
  stage.scratchAllocatedBytes = allocated(root);
  assert.ok(stage.scratchAllocatedBytes < scratchBudget, 'owned QA scratch stays below its explicit budget');
  stages.push(stage);
  console.log(`${name}: ${Math.round(stage.commandMs)}ms; ${stage.http.acHits} AC hits; ${stage.http.puts} PUTs`);
  return { stage, rows, work };
}

let bazel;
let engineBinary;
try {
  assert.equal(process.platform, 'linux', 'this pinned qualification is Linux only');
  assert.equal(process.arch, 'x64', 'this pinned qualification is amd64 only');
  bazel = await tool('bazel', process.env.BAZEL_BIN);
  engineBinary = await tool('engine', process.env.BAZEL_REMOTE_BIN);
  if (binary) assert.ok(isAbsolute(binary), 'LAYERCACHE_BIN must be absolute');
  else {
    binary = join(root, 'layercache');
    await execute('go', ['build', '-o', binary, './cmd/layercache']);
  }
  await execute('go', ['build', '-o', join(root, 'mint-token'), './qa/bazel-native/token']);
  const backendAddress = await address();
  const gatewayAddress = await address();
  const backendURL = `http://${backendAddress}`;
  const gatewayURL = `http://${gatewayAddress}`;
  const engineArgs = ['--dir', join(root, 'engine-cache'), '--max_size', '1', '--max_size_hard_limit', '1',
    '--max_blob_size', '1048576', '--storage_mode', 'uncompressed', '--http_address', backendAddress,
    '--grpc_address', 'none', '--access_log_level', 'none'];
  engine = await launch(engineBinary, engineArgs, 'engine', `${backendURL}/status`);
  await execute(binary, ['setup', '--config', configPath, '--data-dir', join(root, 'gateway-cache'),
    '--listen', gatewayAddress, '--role', 'team', '--project', project, '--compatibility-id', profile,
    '--max-size', '67108864', '--non-interactive', '--json']);
  config = JSON.parse(readFileSync(configPath, 'utf8'));
  config.teamMembers = { qa: 'writer' };
  config.bazelRemote = { projectId: project, compatibilityId: profile, url: backendURL,
    maxBlobBytes: 1048576, requestTimeout: 30_000_000_000 };
  writeFileSync(configPath, JSON.stringify(config), { mode: 0o600 });
  gateway = await launch(binary, ['serve', '--config', configPath], 'gateway', `${gatewayURL}/healthz`);
  observedURL = await observe(gatewayURL);
  let writer = await token({ capability: 'write' });
  let reader = await token();
  const cold = await bazelRun('cold-writer', writer, { write: true });
  assert.equal(cold.stage.http.acHits, 0);
  assert.ok(cold.stage.http.puts > 0, 'cold producer publishes native CAS and AC');
  assert.ok(cold.rows.filter(row => row.method === 'PUT').every(row => row.status === 200), 'every cold publication is acknowledged');
  const consumer = await bazelRun('empty-reader', reader);
  assert.equal(consumer.stage.outputSha256, cold.stage.outputSha256);
  assert.equal(consumer.stage.http.puts, 0, 'read-only consumer sends zero PUT');
  assert.ok(consumer.stage.http.acHits > 0 && consumer.stage.http.casHits > 0);
  assert.ok(consumer.stage.nativeRunners.some(row => row.name === 'remote cache hit' && row.count > 0));
  assert.ok(!consumer.stage.nativeRunners.some(row => row.execKind === 'Local' && row.count > 0), 'consumer executes no local fixture action');
  await stopChild(gateway);
  await stopChild(engine);
  engine = await launch(engineBinary, engineArgs, 'engine', `${backendURL}/status`);
  gateway = await launch(binary, ['serve', '--config', configPath], 'gateway', `${gatewayURL}/healthz`);
  const restart = await bazelRun('restart-reader', reader);
  assert.equal(restart.stage.outputSha256, cold.stage.outputSha256);
  assert.ok(restart.stage.http.acHits > 0);
  assert.equal(restart.stage.http.puts, 0);
  await stopChild(engine);
  const missing = [];
  function findBlob(path) {
    for (const name of readdirSync(path)) {
      const file = join(path, name);
      if (lstatSync(file).isDirectory()) findBlob(file);
      else if (name.includes(cold.stage.outputSha256)) missing.push(file);
    }
  }
  findBlob(join(root, 'engine-cache'));
  assert.equal(missing.length, 1, 'native output CAS blob identified inside the disposable sidecar');
  rmSync(missing[0]);
  engine = await launch(engineBinary, engineArgs, 'engine', `${backendURL}/status`);
  const incomplete = await bazelRun('missing-cas-reader', reader);
  assert.equal(incomplete.stage.outputSha256, cold.stage.outputSha256);
  assert.equal(incomplete.stage.http.acHits, 0, 'upstream AC validation rejects incomplete dependencies');
  assert.equal(incomplete.stage.http.puts, 0);
  for (const changed of ['source', 'environment', 'tool']) {
    const work = workspace(`changed-${changed}`);
    if (changed === 'source') writeFileSync(join(work, 'source.txt'), 'changed-source\n');
    if (changed === 'tool') writeFileSync(join(work, 'tool.sh'), readFileSync(join(work, 'tool.sh'), 'utf8').replace('tool-v1', 'tool-v2'));
    const result = await bazelRun(`changed-${changed}`, reader, { work, env: changed === 'environment' ? 'changed' : 'baseline' });
    assert.notEqual(result.stage.outputSha256, cold.stage.outputSha256);
    assert.equal(result.stage.http.acHits, 0);
    assert.equal(result.stage.http.puts, 0);
  }
  for (const name of ['failed-test-first', 'failed-test-second']) {
    const result = await bazelRun(name, writer, { test: true, write: true });
    assert.equal(result.stage.status, 3, 'real failed test is not accepted');
    assert.ok(result.stage.nativeTestResults.length > 0);
    assert.ok(result.stage.nativeTestResults.every(test => test.status === 'FAILED' && !test.cachedLocally && !test.cachedRemotely),
      'the failed native test runs fresh even when successful test preparation is reused');
  }
  assert.equal(readFileSync(join(root, 'failed-test-count'), 'utf8'), 'ran\nran\n', 'failed native tests execute twice');

  const nativeCAS = '/v1/bazel-cache/cas/' + cold.stage.outputSha256;
  assert.ok(cold.rows.some(row => row.method === 'PUT' && row.path === nativeCAS && row.uploadBytes > 0), 'producer uploaded the native output CAS object');
  const baselineBytes = readFileSync(join(cold.work, 'bazel-bin/message.txt'));
  const repair = await fetch(observedURL + nativeCAS, { method: 'PUT',
    headers: { authorization: `Bearer ${writer}` }, body: baselineBytes });
  assert.equal(repair.status, 200, 'restore only the deliberately removed owned native output blob');
  await repair.arrayBuffer();
  const controlStart = exchanges.length;
  async function control(name, method, path, auth, body, headers = {}) {
    const response = await fetch(observedURL + path, { method,
      headers: { authorization: `Bearer ${auth}`, ...headers }, body,
      signal: AbortSignal.timeout(5000) });
    await response.arrayBuffer();
    const result = { name, status: response.status };
    const expected = { 'read-only-put': 403, 'foreign-project': 401, 'foreign-profile': 401,
      'foreign-integration': 401, 'wrong-digest-put': 500, 'wrong-digest-not-published': 404,
      'oversize-put': 413, 'partial-put-not-published': 404, 'revoked-membership': 401, 'revoked-token': 401 };
    assert.equal(response.status, expected[name], `${name} has its qualified rejection status`);
    return result;
  }
  const controls = [];
  controls.push(await control('read-only-put', 'PUT', nativeCAS, reader, Buffer.from(baselineBytes)));
  controls.push(await control('foreign-project', 'GET', nativeCAS, await token({ project: 'github.com/layercache/foreign' })));
  controls.push(await control('foreign-profile', 'GET', nativeCAS, await token({ profile: 'changed-profile' })));
  controls.push(await control('foreign-integration', 'GET', nativeCAS, await token({ integration: 'gradle' })));
  controls.push(await control('wrong-digest-put', 'PUT', '/v1/bazel-cache/cas/' + 'a'.repeat(64), writer, Buffer.from('mismatch')));
  controls.push(await control('wrong-digest-not-published', 'GET', '/v1/bazel-cache/cas/' + 'a'.repeat(64), reader));
  controls.push(await control('oversize-put', 'PUT', '/v1/bazel-cache/cas/' + 'b'.repeat(64), writer, Buffer.alloc(1048577)));
  const partialBytes = Buffer.from('complete-object-required');
  const partialPath = '/v1/bazel-cache/cas/' + sha(partialBytes);
  await new Promise((done, fail) => {
    const target = new URL(observedURL);
    const socket = createConnection({ host: target.hostname, port: Number(target.port) });
    const timeout = setTimeout(() => { socket.destroy(); fail(new Error('partial PUT exceeded timeout')); }, 5000);
    socket.on('connect', () => socket.end(`PUT ${partialPath} HTTP/1.1\r\nHost: ${target.host}\r\nAuthorization: Bearer ${writer}\r\nContent-Length: ${partialBytes.length}\r\nConnection: close\r\n\r\n${partialBytes.subarray(0, 3).toString()}`));
    socket.on('data', () => {});
    socket.once('error', fail);
    socket.once('close', () => { clearTimeout(timeout); done(); });
  });
  await delay(100);
  controls.push(await control('partial-put-not-published', 'GET', partialPath, reader));
  await stopChild(gateway);
  config.teamMembers = {};
  writeFileSync(configPath, JSON.stringify(config), { mode: 0o600 });
  gateway = await launch(binary, ['serve', '--config', configPath], 'gateway', `${gatewayURL}/healthz`);
  controls.push(await control('revoked-membership', 'GET', nativeCAS, reader));
  await stopChild(gateway);
  config.teamMembers = { qa: 'writer' };
  config.localToken = randomBytes(32).toString('hex');
  writeFileSync(configPath, JSON.stringify(config), { mode: 0o600 });
  gateway = await launch(binary, ['serve', '--config', configPath], 'gateway', `${gatewayURL}/healthz`);
  controls.push(await control('revoked-token', 'GET', nativeCAS, reader));
  reader = await token();
  writer = await token({ capability: 'write' });
  const retained = await fetch(observedURL + nativeCAS, { headers: { authorization: `Bearer ${reader}` } });
  assert.equal(retained.status, 200);
  assert.equal(sha(Buffer.from(await retained.arrayBuffer())), sha(Buffer.from(baselineBytes)));
  let pressureEvidence = { exercised: false };
  if (pressure) {
    const nativeAC = cold.rows.find(row => row.method === 'GET' && row.path.includes('/ac/')).path;
    const beforePressure = await fetch(observedURL + nativeAC, { headers: { authorization: `Bearer ${reader}` } });
    assert.equal(beforePressure.status, 200, 'complete native result exists before optional pressure');
    await beforePressure.arrayBuffer();
    const started = performance.now();
    const bytes = Buffer.alloc(1048576, 0x5a);
    const statuses = {};
    let uploaded = 0;
    for (let i = 0; i < 1050; i++) {
      assert.ok(performance.now() - started < 120_000, 'bounded sidecar pressure deadline');
      const fs = statfsSync(root);
      assert.ok(fs.bavail * fs.bsize > 5 * 1024 ** 3, 'preserve filesystem reserve during optional pressure');
      bytes.writeUInt32LE(i);
      const response = await fetch(`${observedURL}/v1/bazel-cache/cas/${sha(bytes)}`, {
        method: 'PUT', headers: { authorization: `Bearer ${writer}` }, body: bytes,
        signal: AbortSignal.timeout(5000) });
      await response.arrayBuffer();
      statuses[response.status] = (statuses[response.status] ?? 0) + 1;
      if (response.ok) uploaded += bytes.length;
      else assert.ok([500, 503, 507].includes(response.status), 'pressure failure remains a cache write decline');
      if (i % 100 === 0) assert.ok(allocated(root) < scratchBudget, 'optional pressure scratch bounded');
    }
    pressureEvidence = { exercised: true, attemptedObjects: 1050, successfulBytes: uploaded,
      statuses, commandMs: performance.now() - started, allocatedBytes: allocated(join(root, 'engine-cache')) };
    assert.ok(uploaded > 1024 ** 3, 'accepted unique CAS data exceeds the logical cache capacity');
    assert.ok(allocated(root) < scratchBudget);
    const fallback = await bazelRun('post-pressure-reader', reader);
    assert.equal(fallback.stage.outputSha256, cold.stage.outputSha256);
    assert.equal(fallback.stage.http.puts, 0);
    pressureEvidence.nativeFallback = fallback.stage.http.acHits === 0;
    assert.equal(pressureEvidence.nativeFallback, true, 'capacity pressure evicts old native results and the native client rebuilds');
  }
  evidence = { schemaVersion: 1, checkedAt: new Date().toISOString(), qualification: 'isolated-source-only',
    scope: 'Linux amd64 real Bazel HTTP AC/CAS, dedicated loopback bazel-remote, private read/write capabilities',
    clients: pins, candidateBinarySha256: sha(readFileSync(binary)),
    sourceRevision: (await execute('git', ['rev-parse', 'HEAD'])).text.trim(),
    sourceKind: 'candidate-working-tree',
    sourceDigests: Object.fromEntries(['internal/server/bazel.go', 'internal/config/config.go', 'qa/bazel-native/run.mjs',
      'qa/fixtures/bazel-native/fixture.bzl', 'qa/fixtures/bazel-native/tool.sh'].map(path => [path, sha(readFileSync(join(repo, path)))])),
    stages, controls, pressure: pressureEvidence, controlHTTP: summary(exchanges.slice(controlStart)),
    storage: { sidecarLogicalLimitBytes: 1024 ** 3, sidecarHardLimitBytes: 1024 ** 3,
      maximumBlobBytes: 1048576, sidecarAllocatedBytes: allocated(join(root, 'engine-cache')),
      gatewayAllocatedBytes: allocated(join(root, 'gateway-cache')), scratchAllocatedBytes: allocated(root) },
    limits: ['No application, Apple, remote execution, installed gateway, hosted CI or savings qualification.',
      'HTTP request timings include gateway and sidecar work; summed overlapping requests are not wall time.',
      pressure ? 'Capacity pressure is optional and bounded; sidecar limits do not cap other stores or native output roots.'
        : 'Stock sidecar minimum logical limit is 1 GiB; eviction under a full sidecar is not exercised.'],
  };
} finally {
  let cleanupError;
  try { await stopChildren(children); } catch (error) { cleanupError = error; }
  if (observer) await new Promise(done => { observer.close(done); observer.closeAllConnections(); });
  await finishSignals();
  if (!evidence || cleanupError) console.log(`Owned QA diagnostics: ${root}`);
  if (cleanupError) throw cleanupError;
}

// Export success only after every owned process and observer has stopped.
const destination = process.env.BAZEL_QA_EVIDENCE ?? join(tmpdir(), `layercache-bazel-native-receipt-${randomUUID()}.json`);
assert.ok(isAbsolute(destination), 'evidence destination must be absolute');
writeFileSync(destination, JSON.stringify(evidence, null, 2) + '\n', { mode: 0o600, flag: 'wx' });
// Only this mkdtemp root is owned; the durable receipt survives its removal.
rmSync(root, { recursive: true, force: true });
console.log(`Bazel native qualification passed; evidence: ${destination}`);
