import assert from 'node:assert/strict';
import test from 'node:test';
import { spawn } from 'node:child_process';
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { collectExecutionSample, collectExecutionSampleIsolated, reportExecutionObservation } from './execution.ts';

const GiB = 1024 ** 3;
const project = 'github.com/acme/widget';
const base = {
  platform: 'linux', architecture: 'x64',
  visibleCPU: () => 8, affinityCPU: () => 6, visibleMemory: () => 16 * GiB,
  salt: 'fixed-private-test-salt',
  filesystem: async () => ({ device: 'private-device-canary', capacityBytes: 100 * GiB, freeBytes: 0, availableBytes: 0 }),
};
const env = { GITHUB_WORKSPACE: '/private/workspace-canary', RUNNER_TEMP: '/private/staging-canary', SECRET: 'private-environment-canary' };
const exchange = { teamToken: 'native-token-canary', executionObservations: { version: 1, path: '/v1/executions/observations' } };
const rootReader = async (path: string) => path === '/proc/self/cgroup' ? '0::/\n' : path.endsWith('cpu.max') ? '200000 100000\n' : '4294967296\n';

test('visible resources and namespace limits do not prove effective budgets', async () => {
  const sample = await collectExecutionSample(env, { ...base, read: rootReader });
  assert.deepEqual(sample.cpu, { visibleLogical: 8, affinityLogical: 6, quotaMilliCPU: 2000, effectiveMilliCPU: null, limitKnowledge: 'partial' });
  assert.deepEqual(sample.memory, { visibleTotalBytes: 16 * GiB, cgroupLimitBytes: 4 * GiB, effectiveLimitBytes: null, limitKnowledge: 'partial' });
  assert.equal(sample.filesystems.length, 1);
  assert.deepEqual(sample.filesystems[0]?.roles, ['workspace', 'staging']);
  assert.equal(sample.filesystems[0]?.freeBytes, 0);
  assert.equal(sample.filesystems[0]?.availableBytes, 0);
  assert.match(sample.filesystems[0]?.id || '', /^[0-9a-f]{64}$/);
  const serialized = JSON.stringify(sample);
  for (const privateValue of [...Object.values(env), 'private-device-canary', base.salt]) assert.ok(!serialized.includes(privateValue));
});

test('parent cgroup limits are retained without inventing hidden outer limits', async () => {
  const paths: string[] = [];
  const sample = await collectExecutionSample({}, { ...base, read: async (path, limit) => {
    paths.push(path);
    assert.ok(limit <= 16 * 1024);
    if (path === '/proc/self/cgroup') return '0::/parent/child\n';
    if (path === '/sys/fs/cgroup/parent/cpu.max') return '150000 100000';
    if (path === '/sys/fs/cgroup/parent/memory.max') return String(8 * GiB);
    return path.endsWith('cpu.max') ? 'max 100000' : 'max';
  } });
  assert.equal(sample.cpu.quotaMilliCPU, 1500);
  assert.equal(sample.memory.cgroupLimitBytes, 8 * GiB);
  assert.equal(sample.cpu.effectiveMilliCPU, null);
  assert.equal(sample.memory.effectiveLimitBytes, null);
  assert.equal(paths.length, 7);
  const unlimited = await collectExecutionSample({}, { ...base, read: async path => path === '/proc/self/cgroup' ? '0::/' : path.endsWith('cpu.max') ? 'max 100000' : 'max' });
  assert.equal(unlimited.cpu.effectiveMilliCPU, null);
  assert.equal(unlimited.cpu.limitKnowledge, 'partial');
  assert.equal(unlimited.memory.effectiveLimitBytes, null);
  assert.equal(unlimited.memory.limitKnowledge, 'partial');
  assert.equal(unlimited.memory.cgroupLimitBytes, null);
});

test('missing, partial and unsupported limits remain visibly unknown', async () => {
  const sample = await collectExecutionSample(env, { ...base, read: async () => null, filesystem: async () => { throw new Error('private-error'); } });
  assert.equal(sample.cpu.visibleLogical, 8);
  assert.equal(sample.cpu.effectiveMilliCPU, null);
  assert.equal(sample.cpu.limitKnowledge, 'partial');
  assert.equal(sample.memory.effectiveLimitBytes, null);
  assert.equal(sample.memory.limitKnowledge, 'partial');
  assert.deepEqual(sample.filesystems, []);
  const unknown = await collectExecutionSample({}, {
    ...base, platform: 'darwin', architecture: 'unsupported',
    visibleCPU: () => NaN, affinityCPU: () => { throw new Error('unavailable'); }, visibleMemory: () => -1,
    read: async () => assert.fail('non-Linux limit probe'),
  });
  assert.equal(unknown.arch, 'other');
  assert.deepEqual(unknown.cpu, { visibleLogical: null, affinityLogical: null, quotaMilliCPU: null, effectiveMilliCPU: null, limitKnowledge: 'unknown' });
  assert.equal(unknown.memory.visibleTotalBytes, null);
  assert.equal(unknown.memory.limitKnowledge, 'unknown');
});

test('cgroup ancestry and file reads are bounded without accepting traversal', async () => {
  for (const path of ['/../../private', `/${Array.from({ length: 30 }, () => 'child').join('/')}`]) {
    let reads = 0;
    const sample = await collectExecutionSample({}, { ...base, read: async source => {
      reads++;
      if (source === '/proc/self/cgroup') return `0::${path}`;
      assert.ok(source.startsWith('/sys/fs/cgroup/'));
      assert.ok(!source.includes('..'));
      return source.endsWith('cpu.max') ? 'max 100000' : 'max';
    } });
    assert.ok(reads <= 33);
    assert.equal(sample.cpu.effectiveMilliCPU, null);
    assert.equal(sample.memory.effectiveLimitBytes, null);
  }
});

test('old servers and malicious discovery paths cause no probe or report', async () => {
  for (const discovery of [undefined, { version: 2, path: '/v1/executions/observations' }, ...[
    'https://attacker.invalid/token', '//attacker.invalid/token', '/v1/executions/observations?token=leak',
    '/v1/../executions/observations', '/v1/executions/observations#fragment',
  ].map(path => ({ version: 1, path }))]) {
    await reportExecutionObservation({ project, endpoint: 'https://cache.example', exchange: { teamToken: exchange.teamToken, executionObservations: discovery },
      collect: async () => assert.fail('probe'), fetcher: async () => assert.fail('request') });
  }
  for (const project of ['', 'project\r\nAuthorization: foreign', 'x'.repeat(1025)]) {
    await reportExecutionObservation({ project, endpoint: 'https://cache.example', exchange,
      collect: async () => assert.fail('invalid project probe'), fetcher: async () => assert.fail('invalid project request') });
  }
});

test('the optional snapshot uses only native authority and declared provider', async () => {
  for (const [provider, expected] of [[undefined, 'unknown'], ['ubicloud', 'ubicloud'], ['github', 'github'], ['selfHosted', 'selfHosted'], ['invalid', 'unknown']] as const) {
    const requests: Array<[string, RequestInit | undefined]> = [];
    await reportExecutionObservation({ project, endpoint: 'https://cache.example/v1', exchange, phase: 'finish',
      env: { ...env, RUNNER_NAME: 'ubicloud-secret-host', RUNNER_ENVIRONMENT: 'github-hosted' },
      ...(provider === undefined ? {} : { provider }),
      collect: selected => collectExecutionSample(selected, { ...base, read: rootReader }),
      fetcher: async (url, input) => { requests.push([String(url), input]); return Response.json({ version: 1, recorded: true }, { status: 201 }); },
    });
    assert.equal(requests.length, 1);
    const [url, input] = requests[0]!;
    assert.equal(url, 'https://cache.example/v1/executions/observations');
    assert.equal(input?.redirect, 'error');
    assert.equal(new Headers(input?.headers).get('X-LayerCache-Project'), project);
    assert.equal(new Headers(input?.headers).get('Authorization'), `Bearer ${exchange.teamToken}`);
    const payload = JSON.parse(String(input?.body));
    assert.deepEqual(Object.keys(payload).sort(), ['phase', 'provider', 'sample', 'version']);
    assert.equal(payload.provider, expected);
    assert.equal(payload.phase, 'finish');
    assert.ok(Buffer.byteLength(String(input?.body)) <= 16 * 1024);
    for (const value of [exchange.teamToken, ...Object.values(env), 'ubicloud-secret-host', 'private-device-canary']) assert.ok(!String(input?.body).includes(value));
  }
});

test('observation errors, unavailable metrics and oversized snapshots cannot fail cache setup', async () => {
  await assert.doesNotReject(reportExecutionObservation({ project, endpoint: 'https://cache.example', exchange, collect: async () => { throw new Error('private-collector-error'); }, fetcher: async () => assert.fail('request') }));
  const sample = await collectExecutionSample({}, { ...base, read: async () => null });
  for (const fetcher of [async () => { throw new Error('private-network-error'); }, async () => new Response('private-response-body', { status: 500 })]) {
    await assert.doesNotReject(reportExecutionObservation({ project, endpoint: 'https://cache.example', exchange, collect: async () => sample, fetcher }));
  }
  await reportExecutionObservation({ project, endpoint: 'https://cache.example', exchange, collect: async () => ({ ...sample,
    filesystems: [{ id: 'x'.repeat(17 * 1024), roles: ['workspace'], capacityBytes: null, freeBytes: null, availableBytes: null }],
  }), fetcher: async () => assert.fail('oversized request') });
});

test('an unfinished injected collector respects the reporter deadline', { timeout: 5000 }, async () => {
  await reportExecutionObservation({ project, endpoint: 'https://cache.example', exchange,
    collect: async () => new Promise<never>(() => {}),
    fetcher: async () => assert.fail('unfinished sample must not be submitted'),
  });
});

test('direct TypeScript reporting loads the shipped isolated worker', async () => {
  const sample = await collectExecutionSampleIsolated({ ...env });
  assert.ok(['linux', 'darwin', 'windows', 'other'].includes(sample.os));
  assert.ok(sample.cpu.visibleLogical === null || sample.cpu.visibleLogical > 0);
  assert.equal(sample.cpu.effectiveMilliCPU, null);
  assert.equal(sample.memory.effectiveLimitBytes, null);
  for (const value of Object.values(env)) assert.ok(!JSON.stringify(sample).includes(value));
});

test('the shipped native CJS action finds its sibling worker', { timeout: 5000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), 'layercache-native-observation-'));
  try {
    const marker = join(directory, 'observed.json');
    const preload = join(directory, 'fixture.mjs');
    await writeFile(preload, `import {writeFileSync} from 'node:fs';globalThis.fetch=async(input,request)=>{const path=new URL(input).pathname;if(path==='/oidc')return Response.json({value:'fixture-id-token'});if(path.endsWith('/auth/github-oidc/exchange'))return Response.json({teamToken:'fixture-native-token',expiresAt:new Date(Date.now()+300000).toISOString(),executionObservations:{version:1,path:'/v1/executions/observations'}});if(path==='/v1/executions/observations'){if(new Headers(request.headers).get('X-LayerCache-Project')!=='github.com/acme/widget')process.exit(3);writeFileSync(${JSON.stringify(marker)},request.body);process.exit(0);}throw Error('Unexpected network');};`);
    const child = spawn(process.execPath, ['--import', preload, new URL('../native/dist/main.cjs', import.meta.url).pathname], {
      env: {
        'INPUT_TEAM-URL': 'https://cache.example', INPUT_PROJECT: 'github.com/acme/widget', INPUT_KEY: 'fixture-key', INPUT_COMPATIBILITY: 'fixture-abi',
        ACTIONS_ID_TOKEN_REQUEST_URL: 'https://identity.example/oidc', ACTIONS_ID_TOKEN_REQUEST_TOKEN: 'fixture-oidc-request',
        GITHUB_OUTPUT: join(directory, 'output'), GITHUB_WORKSPACE: directory, RUNNER_TEMP: directory,
      }, stdio: ['ignore', 'pipe', 'pipe'],
    });
    let diagnostics = '';
    child.stdout.on('data', chunk => { diagnostics += chunk; });
    child.stderr.on('data', chunk => { diagnostics += chunk; });
    const result = await new Promise<number | null>((resolve, reject) => {
      const watchdog = setTimeout(() => { child.kill('SIGKILL'); reject(new Error('Native fixture deadline')); }, 4000);
      child.on('error', error => { clearTimeout(watchdog); reject(error); });
      child.on('close', code => { clearTimeout(watchdog); resolve(code); });
    });
    assert.equal(result, 0, diagnostics);
    const payload = JSON.parse(await readFile(marker, 'utf8'));
    assert.equal(payload.phase, 'start');
    assert.equal(payload.sample.cpu.effectiveMilliCPU, null);
    assert.equal(payload.sample.memory.effectiveLimitBytes, null);
    assert.ok(!JSON.stringify(payload).includes(directory));
  } finally { await rm(directory, { recursive: true, force: true }); }
});

// macOS can add this key even when spawn receives an empty environment.
// https://github.com/nodejs/node/blob/3e5c8afba6dcff91d46b8204276d62e3843b23b6/test/embedding/test-embedding-permission-env.js#L28-L30
function workerEnvironmentIsIsolated(keys: string[], platform: string): boolean {
  return keys.every(key => platform === 'darwin' && key === '__CF_USER_TEXT_ENCODING');
}

test('the worker environment fixture permits only the Darwin runtime key', () => {
  for (const platform of ['linux', 'darwin', 'win32']) {
    assert.equal(workerEnvironmentIsIsolated([], platform), true);
    assert.equal(workerEnvironmentIsIsolated(['__CF_USER_TEXT_ENCODING'], platform), platform === 'darwin');
    for (const key of ['SECRET', 'GITHUB_TOKEN', 'NODE_OPTIONS', '__CF_USER_TEXT_ENCODING_SUFFIX', '__CF_UNEXPECTED']) {
      assert.equal(workerEnvironmentIsIsolated([key], platform), false);
      assert.equal(workerEnvironmentIsIsolated(['__CF_USER_TEXT_ENCODING', key], platform), false);
    }
  }
});

test('the isolated worker inherits no token, environment or Node flags', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'layercache-observation-worker-'));
  try {
    const worker = join(directory, 'fixture.cjs');
    const sample = await collectExecutionSample({}, { ...base, read: async () => null });
    await writeFile(worker, `let source='';process.stdin.on('data',chunk=>source+=chunk);process.stdin.on('end',()=>{const input=JSON.parse(source);if(!(${workerEnvironmentIsIsolated.toString()})(Object.keys(process.env),process.platform)||Object.keys(input).some(key=>!['GITHUB_WORKSPACE','RUNNER_TEMP'].includes(key))||process.execArgv.length)process.exit(2);process.stdout.write(${JSON.stringify(JSON.stringify(sample))});});`);
    assert.deepEqual(await collectExecutionSampleIsolated(env, worker), sample);
  } finally { await rm(directory, { recursive: true, force: true }); }
});

test('an actual worker spawn failure remains an optional observation failure', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'layercache-observation-no-executable-'));
  try {
    await assert.rejects(collectExecutionSampleIsolated(env, 'unused-worker.cjs', join(directory, 'missing-node')), /Observation worker unavailable/);
    await assert.doesNotReject(reportExecutionObservation({ project, endpoint: 'https://cache.example', exchange,
      collect: selected => collectExecutionSampleIsolated(selected, 'unused-worker.cjs', join(directory, 'missing-node')),
      fetcher: async () => assert.fail('failed spawn must not report'),
    }));
  } finally { await rm(directory, { recursive: true, force: true }); }
});

test('a blocked worker with an active handle cannot keep the action parent alive', { timeout: 7000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), 'layercache-observation-deadline-'));
  let workerPID: number | undefined;
  try {
    const worker = join(directory, 'blocked.cjs');
    const marker = join(directory, 'worker-pid');
    await writeFile(worker, `require('node:fs').writeFileSync(${JSON.stringify(marker)},String(process.pid));setInterval(()=>{},1000);`);
    const parentSource = `import {collectExecutionSampleIsolated} from ${JSON.stringify(new URL('./execution.ts', import.meta.url).href)};try{await collectExecutionSampleIsolated({},${JSON.stringify(worker)});process.exitCode=2;}catch{console.log('bounded-return');}`;
    const parent = spawn(process.execPath, ['--input-type=module', '-e', parentSource], { stdio: ['ignore', 'pipe', 'pipe'] });
    let output = '';
    parent.stdout.on('data', chunk => { output += chunk; });
    parent.stderr.on('data', chunk => { output += chunk; });
    const started = Date.now();
    const result = await new Promise<number | null>((resolve, reject) => {
      const watchdog = setTimeout(() => { parent.kill('SIGKILL'); reject(new Error('Action parent remained alive after worker deadline')); }, 5000);
      parent.on('error', error => { clearTimeout(watchdog); reject(error); });
      parent.on('close', code => { clearTimeout(watchdog); resolve(code); });
    });
    workerPID = Number(await readFile(marker, 'utf8'));
    assert.equal(result, 0, output);
    assert.match(output, /bounded-return/);
    assert.ok(Date.now() - started < 4500, output);
    assert.throws(() => process.kill(workerPID!, 0), /ESRCH/);
  } finally {
    if (!workerPID) { try { workerPID = Number(await readFile(join(directory, 'worker-pid'), 'utf8')); } catch {} }
    if (workerPID) { try { process.kill(workerPID, 'SIGKILL'); } catch {} }
    await rm(directory, { recursive: true, force: true });
  }
});
