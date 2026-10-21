import assert from 'node:assert/strict';
import test from 'node:test';
import { spawn, spawnSync } from 'node:child_process';
import { createServer } from 'node:http';
import { chmod, mkdtemp, readFile, readdir, rm, stat, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { collectXcodeReport, parseXcodeCompilerLog, parseXcodeRPCStats, xcodeReportFormat } from './xcode-report.ts';

const service = {actionReads: 10, actionHits: 6, actionWrites: 4, casReads: 20, casHits: 18, casWrites: 2, cacheErrors: 0};
const metadata = {invocationId: 'run-fixture_0123456789abcdef', startedAt: '2026-10-20T10:00:00.000Z', finishedAt: '2026-10-20T10:01:00.000Z', status: 'success' as const, format: xcodeReportFormat};
const clang = 'CompileC /private/secret/output.o /private/secret/input.m normal arm64 objective-c\n';
const pch = 'ProcessPCH++ /private/secret/prefix.pch normal arm64\n';
const completed = '** BUILD SUCCEEDED **\n';
const parse = (log: string) => parseXcodeCompilerLog(log, xcodeReportFormat);

test('build-for-testing terminals complete compiler capture without creating test verdicts', async () => {
  const observed = clang + 'note: replayed cache hit: private-key\n';
  for (const outcome of ['SUCCEEDED', 'FAILED']) {
    assert.deepEqual(await parse(observed + `** TEST BUILD ${outcome} **\n`),
      {scope: 'clang-pch', coverage: 'complete', reason: 'none', hits: 1, misses: 0, skipped: null, unknown: 0});
  }
  for (const terminal of ['** TEST BUILDING SUCCEEDED **', '** TEST BUILD SUCCEEDED ** extra', '** TEST  BUILD SUCCEEDED **']) {
    assert.equal((await parse(observed + terminal + '\n')).coverage, 'partial');
  }
  const repeated = await parse(observed + '** TEST BUILD SUCCEEDED **\n' + observed + completed);
  assert.equal(repeated.coverage, 'partial');
  assert.equal(repeated.reason, 'ambiguous');
});

test('qualified task-context suffixes and SGR formatting preserve terminal verdicts and privacy', async () => {
  const suffix = " (in target 'private-target' from project 'private-project')";
  const log = '\x1b[36m' + clang.trimEnd() + '\x1b[0m\n' +
    '\x1b[1mnote: replayed cache hit: private-key' + suffix + '\x1b[0m\n' +
    pch + '\x1b[33mnote: cache miss: private-miss' + suffix + '\x1b[0m\n' + '** TEST BUILD SUCCEEDED **\n';
  const parsed = await parse(log);
  assert.deepEqual(parsed, {scope: 'clang-pch', coverage: 'complete', reason: 'none', hits: 1, misses: 1, skipped: null, unknown: 0});
  assert.doesNotMatch(JSON.stringify(parsed), /private|target|project|key|\x1b/);
  const swift = await parse('SwiftCompile normal arm64 private.swift\nnote: Replay cache miss' + suffix + '\n** TEST BUILD SUCCEEDED **');
  assert.equal(swift.misses, 1); assert.equal(swift.coverage, 'partial'); assert.equal(swift.unknown, null);
  for (const change of ['\x1b[2J', '\x1b[31m', '\x1b]0;private\x07']) {
    assert.equal((await parse(clang + change + 'note: cache miss: private-key\n' + completed)).reason, 'unsupported');
  }
  for (const badSuffix of [" (in target 'private' from project 'unterminated)", suffix + ' extra']) {
    assert.notEqual((await parse(clang + 'note: cache miss: private-key' + badSuffix + '\n' + completed)).coverage, 'complete');
  }
  assert.equal((await parseXcodeCompilerLog(log, xcodeReportFormat, {bytes: Buffer.byteLength(log) - 1})).reason, 'truncated');
});

test('module and bridging-header replay observations keep Swift-driver denominator unknown', async () => {
  const log = 'PrecompileModule /private/module.pcm\nnote: cache miss: private-clang-key\n' +
    'SwiftGeneratePch normal arm64\nnote: Replay cache miss\n' +
    'SwiftExplicitDependencyGeneratePcm normal arm64 /private/clang.pcm\nnote: replay cache hit\n' +
    'SwiftExplicitDependencyCompileModuleFromInterface normal arm64 /private/module.swiftinterface\nnote: replay cache miss\n' +
    '** TEST BUILD SUCCEEDED **\n';
  assert.deepEqual(await parse(log), {scope: 'mixed', coverage: 'partial', reason: 'ambiguous', hits: 1, misses: 3, skipped: null, unknown: null});
  assert.deepEqual(await parse('PrecompileModule /private/module.pcm\nnote: replayed cache hit: private-key\n' + completed),
    {scope: 'clang-pch', coverage: 'complete', reason: 'none', hits: 1, misses: 0, skipped: null, unknown: 0});
  for (const header of ['SwiftGeneratePchUnknown', 'PrecompileModuleUnknown', 'SwiftExplicitDependencyGeneratePcmUnknown']) {
    const unknown = await parse(header + ' /private/module\nnote: replay cache hit\n' + completed);
    assert.equal(unknown.hits, null); assert.notEqual(unknown.coverage, 'complete');
  }
});

test('Clang and PCH terminal replay counts ignore intermediate queries and duplicate result lines', async () => {
  const log = 'ClangCachingKeyQuery /private/lookup\nnote: cache hit\n' + clang +
    'note: local cache miss for key: private-intermediate-key\nnote: cache miss: private-miss-key\nCache miss\n' +
    pch + 'note: replayed cache hit: private-hit-key\nCache hit\nCache hit\n' + completed;
  assert.deepEqual(await parse(log), {scope: 'clang-pch', coverage: 'complete', reason: 'none', hits: 1, misses: 1, skipped: null, unknown: 0});
  const report = await collectXcodeReport({...metadata, log, serviceStats: JSON.stringify({...service, graphLookupNanos: 8000000000, uncachedFallback: false, uncachedRetry: false})});
  assert.deepEqual(report.tasks, []);
  assert.deepEqual(report.counters.rpc, service);
  assert.deepEqual(report.timingMs, {operation: null, compile: null, load: null, restore: null, close: null, snapshot: null});
  assert.doesNotMatch(JSON.stringify(report), /private|secret|key|graphLookup|Nanos|actionMisses|casMisses/);
});

test('Swift virtual file headers do not create jobs or misses and their cardinality stays unknown', async () => {
  const log = 'SwiftCompile normal arm64 Compiling A.swift, B.swift\nSwiftCompile normal arm64 /private/A.swift\nSwiftCompile normal arm64 /private/B.swift\n' +
    'note: local cache miss for key: private-swift-query\nnote: replay cache hit\nCache hit\n' +
    'SwiftEmitModule normal arm64\nnote: replay cache miss\nCache miss\n' + completed;
  assert.deepEqual(await parse(log), {scope: 'swift', coverage: 'partial', reason: 'ambiguous', hits: 1, misses: 1, skipped: null, unknown: null});
  const mixed = await parse(clang + 'note: replayed cache hit: key\n' + log);
  assert.equal(mixed.scope, 'mixed'); assert.equal(mixed.hits, 2); assert.equal(mixed.misses, 1);
  assert.equal(mixed.coverage, 'partial'); assert.equal(mixed.unknown, null);
});

test('missing, detached, unsupported and ambiguous diagnostics never become compiler hits or misses', async () => {
  const missing = await parse(clang + 'note: Local cache miss for key: local-query\n' + completed);
  assert.equal(missing.hits, 0); assert.equal(missing.misses, 0); assert.equal(missing.unknown, 1); assert.equal(missing.coverage, 'partial');
  const detached = await parse('ClangCachingKeyQuery job\nnote: cache hit\n' + completed);
  assert.equal(detached.coverage, 'unknown'); assert.equal(detached.hits, null);
  const unsupported = await parse(clang + "remark: compile job cache hit for 'private' => 'private-output'\n" + completed);
  assert.equal(unsupported.coverage, 'partial'); assert.equal(unsupported.reason, 'unsupported'); assert.equal(unsupported.hits, 0); assert.equal(unsupported.unknown, 1);
  for (const finals of [
    'note: replayed cache hit: same\nnote: replayed cache hit: same\n',
    'note: cache miss: same\nnote: replayed cache hit: same\n',
    'note: replayed cache hit: first\nnote: replayed cache hit: second\n',
  ]) {
    const ambiguous = await parse(clang + finals + completed);
    assert.equal(ambiguous.coverage, 'partial'); assert.equal(ambiguous.reason, 'ambiguous');
    assert.equal(ambiguous.hits, 0); assert.equal(ambiguous.misses, 0); assert.equal(ambiguous.unknown, 1);
  }
});

test('compiler format and complete capture are explicit, with bounded streaming and malformed-input refusal', async () => {
  let reads = 0;
  const unsupported = await parseXcodeCompilerLog((async function* () {reads++; yield Buffer.from(clang);})(), 'future-private-format');
  assert.equal(unsupported.coverage, 'unsupported'); assert.equal(unsupported.hits, null); assert.equal(reads, 0);
  assert.equal((await parseXcodeCompilerLog(undefined, xcodeReportFormat)).reason, 'missing');
  const noEnd = await parse(clang + 'note: replayed cache hit: key\n');
  assert.equal(noEnd.hits, 1); assert.equal(noEnd.coverage, 'partial'); assert.equal(noEnd.reason, 'missing');
  const original = clang + 'note: replayed cache hit: key\n' + completed;
  const stream = async function* () {for (const byte of Buffer.from(original.replaceAll('\n', '\r\n'))) yield Uint8Array.of(byte);};
  assert.deepEqual(await parseXcodeCompilerLog(stream(), xcodeReportFormat), await parse(original));
  const tooManyBytes = await parseXcodeCompilerLog(original, xcodeReportFormat, {bytes: Buffer.byteLength(clang + 'note: replayed cache hit: key\n')});
  assert.equal(tooManyBytes.reason, 'truncated'); assert.equal(tooManyBytes.coverage, 'partial');
  const tooLongLine = await parseXcodeCompilerLog(original, xcodeReportFormat, {lineBytes: 10});
  assert.equal(tooLongLine.reason, 'truncated'); assert.equal(tooLongLine.hits, null);
  const tooManyLines = await parseXcodeCompilerLog(original, xcodeReportFormat, {lines: 1});
  assert.equal(tooManyLines.reason, 'truncated'); assert.notEqual(tooManyLines.coverage, 'complete');
  const invalidUTF8 = await parseXcodeCompilerLog((async function* () {yield Buffer.from(clang); yield Uint8Array.of(0xc3);})(), xcodeReportFormat);
  assert.equal(invalidUTF8.reason, 'unsupported'); assert.notEqual(invalidUTF8.coverage, 'complete');
  const binary = await parse(clang + 'note: replayed cache hit: secret\x00key\n' + completed);
  assert.equal(binary.reason, 'unsupported'); assert.equal(binary.hits, 0);
});

test('service statistics accept only direct bounded counters and exact scalar fields', () => {
  assert.deepEqual(parseXcodeRPCStats(JSON.stringify({...service, actionLookupMisses: 4, graphChecks: 1, uncachedFallback: false})), service);
  const valid = JSON.stringify(service);
  for (const text of [
    '{}', '[]', valid + '{}', valid.replace('"actionReads":10', '"actionReads":10,"actionReads":20'),
    JSON.stringify({...service, privateToken: 'secret'}), JSON.stringify({...service, actionHits: 11}),
    JSON.stringify({...service, casHits: 21}), JSON.stringify({...service, actionReads: -1}),
    JSON.stringify({...service, actionReads: 1.5}), JSON.stringify({...service, actionReads: Number.MAX_SAFE_INTEGER + 1}),
    JSON.stringify({...service, actionReads: true}), JSON.stringify({...service, uncachedRetry: 0}),
    JSON.stringify({stats: service}), valid.replace(/}$/, ',}'), valid.replace('"actionReads"', '"action\\u0052eads"'),
    '{"actionReads":' + '9'.repeat(17000) + '}',
  ]) assert.throws(() => parseXcodeRPCStats(text), /^Error: Invalid Xcode service statistics$/);
});

test('caller-observed phases stay separate and missing timings are never inferred from counters', async () => {
  const report = await collectXcodeReport({...metadata, serviceStats: JSON.stringify(service), timingMs: {operation: 60000, compile: 45000, load: 0, restore: 8000, close: 500, snapshot: 2000}});
  assert.equal(report.counters.compiler.reason, 'missing');
  assert.deepEqual(report.timingMs, {operation: 60000, compile: 45000, load: 0, restore: 8000, close: 500, snapshot: 2000});
  assert.equal(report.tasks.length, 0);
  for (const value of [-1, 1.5, Number.NaN, 86400001]) await assert.rejects(collectXcodeReport({...metadata, timingMs: {compile: value}}), /Invalid Xcode reporting timing/);
  for (const change of [
    {startedAt: '2026-02-30T10:00:00Z'}, {finishedAt: '2026-10-20T09:59:59.999999999Z'},
    {finishedAt: '2026-10-21T10:00:00.000000001Z'}, {invocationId: 'private/path'},
  ]) await assert.rejects(collectXcodeReport({...metadata, ...change}), /Invalid Xcode reporting window/);
});

async function cli(args: string[], env: NodeJS.ProcessEnv = {}) {
  const child = spawn(process.execPath, [resolve(process.env.LAYERCACHE_XCODE_REPORT_TEST_ENTRY ?? 'native/cli.ts'), 'xcode-report', ...args], {stdio: ['ignore', 'pipe', 'pipe'], env: {...process.env, ...env}});
  let stdout = '', stderr = '';
  child.stdout.on('data', data => {stdout += data;}); child.stderr.on('data', data => {stderr += data;});
  return new Promise<{code: number | null; stdout: string; stderr: string}>((resolveResult, reject) => {
    const timer = setTimeout(() => {child.kill('SIGKILL'); reject(new Error('Xcode report fixture timed out'));}, 10000);
    child.on('error', error => {clearTimeout(timer); reject(error);});
    child.on('close', code => {clearTimeout(timer); resolveResult({code, stdout, stderr});});
  });
}
async function files(t: {after: (fn: () => Promise<void>) => void}) {
  const root = await mkdtemp(join(tmpdir(), 'layercache-xcode-report-'));
  t.after(async () => {await rm(root, {recursive: true, force: true});});
  const log = join(root, 'private-source-build.log'), stats = join(root, 'private-final-stats.json'), output = join(root, 'native-meter.json');
  await writeFile(log, clang + 'note: replayed cache hit: secret-key\n' + completed, {mode: 0o600});
  await writeFile(stats, JSON.stringify(service), {mode: 0o600});
  const args = ['--format', xcodeReportFormat, '--invocation-id', metadata.invocationId, '--started-at', metadata.startedAt, '--finished-at', metadata.finishedAt, '--status', metadata.status, '--log-file', log, '--stats-file', stats, '--output', output];
  return {root, log, stats, output, args};
}

test('offline CLI exports private bounded JSON without ambient authentication, HTTP or a cache store', async t => {
  const f = await files(t);
  let requests = 0;
  const endpoint = createServer((_request, response) => {requests++; response.writeHead(500); response.end();});
  await new Promise<void>(resolveReady => endpoint.listen(0, '127.0.0.1', resolveReady));
  t.after(async () => {await new Promise<void>((resolveClose, reject) => endpoint.close(error => error ? reject(error) : resolveClose()));});
  const address = endpoint.address(); assert.ok(address && typeof address !== 'string');
  const result = await cli([...f.args, '--operation-ms', '60000', '--compile-ms', '45000'], {LAYER_CACHE_URL: `http://127.0.0.1:${address.port}`, LAYER_CACHE_TOKEN: 'private-ambient-token', XDG_CACHE_HOME: join(f.root, 'cache')});
  assert.equal(result.code, 0, result.stderr); assert.equal(result.stdout, ''); assert.equal(result.stderr, ''); assert.equal(requests, 0);
  const bytes = await readFile(f.output, 'utf8'), report = JSON.parse(bytes);
  assert.equal((await stat(f.output)).mode & 0o777, 0o600);
  assert.equal(report.version, 1); assert.equal(report.invocationId, metadata.invocationId);
  assert.equal(report.counters.compiler.hits, 1); assert.equal(report.counters.rpc.actionHits, 6);
  assert.equal(report.timingMs.compile, 45000); assert.deepEqual(report.tasks, []);
  assert.doesNotMatch(bytes, /private|secret|cache-dir|token|\.log|\/|graphLookup/);
  assert.deepEqual((await readdir(f.root)).sort(), ['native-meter.json', 'private-final-stats.json', 'private-source-build.log']);
  const before = await stat(f.output), repeated = await cli(f.args);
  assert.equal(repeated.code, 1); assert.match(repeated.stderr, /^Xcode report output unavailable\n$/);
  assert.equal(await readFile(f.output, 'utf8'), bytes); assert.equal((await stat(f.output)).ino, before.ino);
});

test('CLI refuses private-path, symlink and malformed statistics without echoing their contents', async t => {
  const f = await files(t);
  await writeFile(f.stats, JSON.stringify({...service, token: 'private-secret-token'}));
  const bad = await cli(f.args);
  assert.equal(bad.code, 1); assert.match(bad.stderr, /^Invalid Xcode service statistics\n$/); assert.doesNotMatch(bad.stderr, /private|secret|\/tmp/);
  await writeFile(f.stats, JSON.stringify(service));
  const linked = join(f.root, 'symlink-stats'); await symlink(f.stats, linked);
  const linkedArgs = [...f.args]; linkedArgs[linkedArgs.indexOf('--stats-file') + 1] = linked;
  const refused = await cli(linkedArgs);
  assert.equal(refused.code, 1); assert.match(refused.stderr, /^Xcode service statistics unavailable\n$/);
  const linkedLog = join(f.root, 'symlink-log'); await symlink(f.log, linkedLog);
  const logArgs = [...f.args]; logArgs[logArgs.indexOf('--log-file') + 1] = linkedLog;
  const noLog = await cli(logArgs);
  assert.equal(noLog.code, 0, noLog.stderr);
  assert.equal(JSON.parse(await readFile(f.output, 'utf8')).counters.compiler.coverage, 'unsupported');
});

test('nonregular compiler input cannot block the CLI or become compilation evidence', async t => {
  const f = await files(t), fifo = join(f.root, 'input.fifo');
  assert.equal(spawnSync('mkfifo', [fifo]).status, 0); await chmod(fifo, 0o600);
  const args = [...f.args]; args[args.indexOf('--log-file') + 1] = fifo;
  const result = await cli(args);
  assert.equal(result.code, 0, result.stderr);
  assert.equal(JSON.parse(await readFile(f.output, 'utf8')).counters.compiler.hits, null);
});
