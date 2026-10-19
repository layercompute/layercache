import { createHash } from 'node:crypto';
import { execFile, execFileSync } from 'node:child_process';
import { createServer } from 'node:http';
import { request as requestHTTPS } from 'node:https';
import { copyFileSync, existsSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { setImmediate as nextTurn } from 'node:timers/promises';
import { exchangeTurbo } from '../../action/setup/auth.ts';

export const compatibility = 'linux-x64-node24.13.0-turbo2.10.12-pnpm10.34.5-r2-native-v1';
const fixtureRevision = '170582ba6caccb5f5dcdf60171d8d65196075993';
const fixtureFiles = ['.gitignore', 'dependency.json', 'package.json', 'packages/app/fixture.json',
  'packages/app/package.json', 'packages/app/task.mjs', 'pnpm-lock.yaml', 'pnpm-workspace.yaml', 'turbo.json'];
const fixtureDigest = '5ad4043e643204cef74cf9254189e5576c7cb0f90ee0b3cbc0ef6c8c90485ca8';
const sha256 = value => createHash('sha256').update(value).digest('hex');
const taskIDs = ['@layercache/local-ci-reuse-app#build', '@layercache/local-ci-reuse-app#test'];
const check = (value, code) => { if (!value) throw new Error(code); };

export function configuration(env) {
  check(env.R2_QA_ENABLED === 'true' && ['producer', 'reader'].includes(env.R2_QA_MODE), 'operator-opt-in-required');
  check(/^[a-f0-9]{32}$/.test(env.R2_QA_SAMPLE || ''), 'invalid-sample-label');
  let origin;
  try { origin = new URL(env.R2_QA_ORIGIN); } catch { throw new Error('invalid-origin'); }
  check(origin.protocol === 'https:' && origin.pathname === '/' && !origin.username && !origin.password &&
    !origin.search && !origin.hash, 'invalid-origin');
  check(typeof env.R2_QA_PROJECT === 'string' && env.R2_QA_PROJECT.length > 0 && env.R2_QA_PROJECT.length <= 256 &&
    env.R2_QA_PROJECT === env.R2_QA_PROJECT.toLowerCase() && !/[\x00-\x20\x7f]/.test(env.R2_QA_PROJECT), 'invalid-project-selector');
  check(env.GITHUB_EVENT_NAME === 'workflow_dispatch' && env.GITHUB_WORKSPACE && env.RUNNER_TEMP &&
    /^[a-f0-9]{40}$/.test(env.GITHUB_SHA || ''), 'manual-signed-source-required');
  let event;
  try { event = JSON.parse(readFileSync(env.GITHUB_EVENT_PATH, 'utf8')); } catch { throw new Error('invalid-event'); }
  const defaultRef = `refs/heads/${event.repository?.default_branch || ''}`;
  check(event.repository?.default_branch && env.GITHUB_REF?.startsWith('refs/heads/') &&
    (env.R2_QA_MODE === 'producer' ? env.GITHUB_REF === defaultRef : env.GITHUB_REF !== defaultRef), 'wrong-authority-ref');
  return { origin: origin.origin, project: env.R2_QA_PROJECT, mode: env.R2_QA_MODE, defaultRef,
    workspace: join(resolve(env.GITHUB_WORKSPACE), '.layercache-r2-qa'),
    counter: join(resolve(env.RUNNER_TEMP), 'r2-native-executions.jsonl'),
    output: join(resolve(env.RUNNER_TEMP), 'r2-native-public-summary.json'),
    profile: `sample-${env.R2_QA_SAMPLE}` };
}

function execute(program, args, cwd, env) {
  try { return { status: 0, stdout: execFileSync(program, args, { cwd, env, encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'], timeout: 60_000, maxBuffer: 2 << 20 }) }; }
  catch (error) {
    if (Number.isInteger(error.status)) return { status: error.status, stdout: String(error.stdout || '') };
    throw new Error('bounded-command-failed');
  }
}

export function prepare(cfg, env) {
  check(process.platform === 'linux' && process.arch === 'x64' && process.version === 'v24.13.0', 'wrong-native-runtime');
  const git = args => execute('git', args, env.GITHUB_WORKSPACE, env);
  check(git(['rev-parse', 'HEAD']).stdout.trim() === env.GITHUB_SHA, 'checkout-source-mismatch');
  check(execute('pnpm', ['--version'], env.GITHUB_WORKSPACE, env).stdout.trim() === '10.34.5', 'wrong-pnpm-version');
  check(!existsSync(cfg.workspace) && !existsSync(cfg.counter) && !existsSync(cfg.output), 'owned-scratch-already-exists');
  const fixture = join(env.GITHUB_WORKSPACE, 'qa/fixtures/local-ci-reuse');
  const actual = fixtureFiles.map(name => `${name}\0${sha256(readFileSync(join(fixture, name)))}\n`).join('');
  check(sha256(actual) === fixtureDigest, 'pinned-fixture-mismatch');
  mkdirSync(cfg.workspace, { mode: 0o700 });
  for (const name of fixtureFiles) {
    const target = join(cfg.workspace, name);
    mkdirSync(resolve(target, '..'), { recursive: true, mode: 0o700 });
    copyFileSync(join(fixture, name), target);
  }
  for (const args of [['init', '-q', '-b', 'main'], ['add', '.'], ['-c', 'user.name=LayerCache QA',
    '-c', 'user.email=qa@example.invalid', 'commit', '-q', '-m', 'Pinned native fixture']]) {
    check(execute('git', args, cfg.workspace, env).status === 0, 'fixture-git-setup-failed');
  }
  writeFileSync(env.GITHUB_OUTPUT, `compatibility=${compatibility}\n`, { flag: 'a', mode: 0o600 });
}

export function assertAuthority(token, cfg, env, profile = compatibility) {
  let claims;
  try {
    const parts = token.split('.');
    check(parts.length === 3 && parts[0] === 'lc2' && token.length <= 32768 &&
      parts.slice(1).every(part => /^[A-Za-z0-9_-]+$/.test(part)), 'invalid-capability');
    claims = JSON.parse(Buffer.from(parts[1], 'base64url').toString('utf8'));
  } catch { throw new Error('invalid-capability'); }
  check(claims.project === cfg.project && claims.integration === 'turbo' && claims.compatibility === profile &&
    claims.repository === env.GITHUB_REPOSITORY.toLowerCase() && claims.ref === env.GITHUB_REF &&
    claims.defaultRef === cfg.defaultRef && claims.sourceCommit === env.GITHUB_SHA &&
    Array.isArray(claims.capabilities) && claims.capabilities.length === 1 &&
    claims.capabilities[0] === (cfg.mode === 'producer' ? 'write' : 'read') &&
    Date.parse(claims.expiresAt) > Date.now() + 30_000, 'unexpected-issued-authority');
  // This inspection does not verify the signature. Actual authenticated native
  // reads/publication and the denied reader PUT establish server acceptance.
}

export async function observe(origin, token, requester = requestHTTPS) {
  const rows = [], pending = new Set(), upstreams = new Set(), replies = new Set();
  const server = createServer((request, response) => {
    let url;
    try {
      check(request.url?.startsWith('/') && !request.url.startsWith('//'), 'invalid-native-url');
      url = new URL(request.url, origin);
      check(url.origin === origin && /^\/v8\/artifacts\/(?:status|events|[a-f0-9]{16,64})$/.test(url.pathname) &&
        ['GET', 'HEAD', 'PUT', 'POST'].includes(request.method), 'invalid-native-url');
      check(request.headers.authorization === `Bearer ${token}`, 'wrong-native-token');
    } catch { request.resume(); response.writeHead(400).end(); return; }
    const name = url.pathname.split('/').at(-1), artifact = /^[a-f0-9]{16,64}$/.test(name);
    const row = { method: request.method, kind: artifact ? 'artifact' : name, artifact,
      nativeKey: artifact ? name : undefined,
      status: 0, uploadBytes: 0, downloadBytes: 0, headersMs: 0, completeMs: 0,
      digestVerified: false, rejectedRedirect: false, failed: false };
    rows.push(row);
    const started = performance.now();
    if (rows.length > 256) { response.writeHead(429).end(); request.resume(); return; }
    let resolveComplete;
    const complete = new Promise(resolve => { resolveComplete = resolve; });
    pending.add(complete);
    const finish = failed => {
      row.failed ||= failed;
      pending.delete(complete); resolveComplete();
    };
    const headers = { authorization: `Bearer ${token}` };
    for (const name of ['content-length', 'content-type', 'x-artifact-duration', 'x-artifact-tag',
      'x-artifact-client-ci', 'x-artifact-source-sha', 'x-artifact-dirty-hash']) {
      if (typeof request.headers[name] === 'string') headers[name] = request.headers[name];
    }
    const upstream = requester(url, { method: request.method, headers, signal: AbortSignal.timeout(45_000) }, reply => {
      replies.add(reply); reply.once('close', () => replies.delete(reply));
      row.status = reply.statusCode;
      row.headersMs = performance.now() - started;
      if (reply.statusCode >= 300 && reply.statusCode < 400) {
        row.rejectedRedirect = true;
        finish(true); reply.resume(); response.writeHead(502).end(); return;
      }
      const selected = {};
      for (const name of ['content-length', 'content-type', 'cache-control', 'x-artifact-duration', 'x-artifact-tag', 'x-layercache-digest']) {
        if (reply.headers[name] !== undefined) selected[name] = reply.headers[name];
      }
      response.writeHead(reply.statusCode, selected);
      const digest = createHash('sha256');
      reply.on('data', chunk => {
        row.downloadBytes += chunk.length; digest.update(chunk);
        if (row.downloadBytes > 8 << 20) { finish(true); reply.destroy(); response.destroy(); }
      });
      reply.once('end', () => {
        if (row.failed) return;
        row.completeMs = performance.now() - started;
        row.digestVerified = row.method === 'GET' && row.artifact && row.status === 200 &&
          reply.headers['x-layercache-digest'] === `sha256:${digest.digest('hex')}`;
        finish(false);
      });
      reply.once('error', () => { finish(true); response.destroy(); });
      reply.once('aborted', () => { finish(true); response.destroy(); });
      reply.once('close', () => { if (!reply.readableEnded) finish(true); });
      // Turbo can finish after telemetry headers without consuming its body.
      // Drain the real upstream stream even when that downstream has closed.
      if (response.destroyed) reply.resume();
      else {
        reply.pipe(response);
        response.once('close', () => reply.resume());
      }
    });
    upstreams.add(upstream);
    upstream.once('close', () => upstreams.delete(upstream));
    upstream.once('error', () => {
      row.status = 502; finish(true);
      if (!response.headersSent) response.writeHead(502).end(); else response.destroy();
    });
    request.on('data', chunk => {
      row.uploadBytes += chunk.length;
      if (row.uploadBytes > 8 << 20) { finish(true); upstream.destroy(); request.destroy(); }
    });
    request.once('aborted', () => upstream.destroy());
    request.pipe(upstream);
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  return { rows, origin: `http://127.0.0.1:${server.address().port}`,
    settle: async (timeoutMs = 5000) => {
      await nextTurn();
      if (!pending.size) return;
      let timer;
      try {
        const completed = async () => { while (pending.size) await Promise.all([...pending]); };
        await Promise.race([completed(), new Promise((_, reject) => {
          timer = setTimeout(() => reject(new Error('native-observation-timeout')), timeoutMs);
        })]);
      } finally { clearTimeout(timer); }
      check(pending.size === 0, 'native-observation-timeout');
    },
    close: () => new Promise(resolve => {
      server.close(resolve); server.closeAllConnections();
      for (const upstream of upstreams) upstream.destroy();
      for (const reply of replies) reply.destroy();
    }) };
}

export function taskSummary(document) {
  check(Array.isArray(document.tasks) && document.tasks.length >= 1 && document.tasks.length <= 2, 'invalid-native-summary');
  return document.tasks.map(task => {
    check(taskIDs.includes(task.taskId) && /^[a-f0-9]{16}$/.test(task.hash), 'invalid-native-task');
    check(['HIT', 'MISS', 'DISABLED'].includes(task.cache?.status) && typeof task.cache.local === 'boolean' &&
      typeof task.cache.remote === 'boolean', 'invalid-native-cache-result');
    check(task.execution?.exitCode === undefined || Number.isInteger(task.execution.exitCode) &&
      task.execution.exitCode >= 0 && task.execution.exitCode <= 255, 'invalid-native-exit-code');
    return { task: task.taskId, nativeKey: task.hash, cache: task.cache.status,
      localHit: task.cache.local, remoteHit: task.cache.remote, exitCode: task.execution?.exitCode ?? null };
  }).sort((a, b) => a.task.localeCompare(b.task));
}

export function httpSummary(rows, mode) {
  check(rows.every(row => row.status > 0 && row.status < 600 && row.completeMs > 0 && !row.rejectedRedirect && !row.failed), 'native-transport-failed');
  const deniedEvents = row => mode === 'reader' && row.kind === 'events' && row.method === 'POST' && row.status === 401;
  check(rows.every(row => row.status === 200 || row.status === 404 || deniedEvents(row)), 'unexpected-native-http-status');
  return { requests: rows.length, artifactPUTs: rows.filter(row => row.artifact && row.method === 'PUT').length,
    artifactGETMisses: rows.filter(row => row.artifact && row.method === 'GET' && row.status === 404).length,
    deniedOptionalEvents: rows.filter(deniedEvents).length,
    uploadedBodyBytes: rows.reduce((n, row) => n + row.uploadBytes, 0),
    downloadedBodyBytes: rows.reduce((n, row) => n + row.downloadBytes, 0),
    verifiedArchiveGETs: rows.filter(row => row.digestVerified).length,
    statuses: rows.reduce((all, row) => ({ ...all, [row.status]: (all[row.status] || 0) + 1 }), {}),
    summedHeadersMs: rows.reduce((n, row) => n + row.headersMs, 0),
    summedCompleteMs: rows.reduce((n, row) => n + row.completeMs, 0) };
}

export async function qualify(cfg, env, { requester = requestHTTPS, exchanger = exchangeTurbo } = {}) {
  check(env.TURBO_API === cfg.origin && env.TURBO_TEAM === cfg.project && env.TURBO_TOKEN, 'pinned-action-environment-required');
  assertAuthority(env.TURBO_TOKEN, cfg, env);
  const turbo = join(env.GITHUB_WORKSPACE, 'qa/fixtures/local-ci-reuse/node_modules/.bin/turbo');
  check(execute(turbo, ['--version'], cfg.workspace, env).stdout.trim() === '2.10.12', 'wrong-turbo-version');
  const observer = await observe(cfg.origin, env.TURBO_TOKEN, requester);
  const evidence = { schemaVersion: 'r2-native-oidc-v1', passed: false, mode: cfg.mode, checkedAt: new Date().toISOString(),
    sourceRevision: env.GITHUB_SHA, fixtureRevision, nodeVersion: process.version, turboVersion: '2.10.12',
    pnpmVersion: '10.34.5', issuedAuthority: cfg.mode === 'producer' ? 'write' : 'read', scenarios: [],
    limits: ['Synthetic real native Turbo fixture, not an application benchmark or monthly cost evidence.',
      'Remote artifact bodies and request times are observed at a loopback relay to public HTTPS; they are not provider bytes or pure bandwidth.',
      'Overlapping request durations are summed separately from complete native command wall time.',
      'Optional evictable cache acceptance; required retained application handoffs stay on GitHub artifacts.'] };
  const baseEnv = { PATH: env.PATH, HOME: env.HOME, ...(env.TMPDIR ? { TMPDIR: env.TMPDIR } : {}),
    CI: 'true', NO_COLOR: '1', TURBO_TELEMETRY_DISABLED: '1', TURBO_DAEMON: 'false',
    TURBO_API: observer.origin, TURBO_TEAM: cfg.project, TURBO_TOKEN: env.TURBO_TOKEN,
    EXECUTION_COUNTER: cfg.counter, TEST_PROFILE: cfg.profile };
  const count = () => existsSync(cfg.counter) ? readFileSync(cfg.counter, 'utf8').trim().split('\n').filter(Boolean).length : 0;
  const input = (source = 'one', fail = false) => writeFileSync(join(cfg.workspace, 'packages/app/fixture.json'), JSON.stringify({ source, fail }) + '\n');
  const keys = profile => {
    const result = execute(turbo, ['run', 'build', 'test', '--dry=json', '--cache=local:,remote:'], cfg.workspace,
      { ...baseEnv, TEST_PROFILE: profile });
    check(result.status === 0, 'native-dry-plan-failed');
    let document; try { document = JSON.parse(result.stdout); } catch { throw new Error('native-dry-plan-failed'); }
    check(document.tasks?.length === 2, 'unexpected-native-plan');
    return document.tasks.map(task => ({ task: task.taskId, nativeKey: task.hash })).sort((a, b) => a.task.localeCompare(b.task));
  };
  let baseline;
  async function run(name, { profile = cfg.profile, executed = 2, hit = false, cache = 'local:,remote:r', fail = false, sameKeys = false } = {}) {
    for (const directory of ['packages/app/dist', 'packages/app/.test']) rmSync(join(cfg.workspace, directory), { recursive: true, force: true });
    const runs = join(cfg.workspace, '.turbo/runs');
    const before = new Set(existsSync(runs) ? readdirSync(runs) : []), startCounter = count(), httpStart = observer.rows.length;
    const expected = keys(profile), started = performance.now();
    let result;
    try {
      const output = await promisify(execFile)(turbo, ['run', ...(fail ? ['test'] : ['build', 'test']), '--summarize', `--cache=${cache}`],
        { cwd: cfg.workspace, env: { ...baseEnv, TEST_PROFILE: profile }, encoding: 'utf8',
          timeout: 60_000, maxBuffer: 2 << 20 });
      result = { status: 0, stdout: output.stdout };
    } catch (error) {
      check(Number.isInteger(error.code), 'bounded-command-failed');
      result = { status: error.code, stdout: String(error.stdout || '') };
    }
    const commandMs = performance.now() - started;
    const observationStart = performance.now();
    await observer.settle();
    const observationMs = performance.now() - observationStart;
    check(result.status === (fail ? 7 : 0), 'native-command-result-mismatch');
    const fresh = readdirSync(runs).filter(name => name.endsWith('.json') && !before.has(name));
    check(fresh.length === 1, 'missing-native-summary');
    const tasks = taskSummary(JSON.parse(readFileSync(join(runs, fresh[0]), 'utf8')));
    check(tasks.length === (fail ? 1 : 2) && new Set(tasks.map(task => task.task)).size === tasks.length, 'incomplete-native-task-set');
    const exchanges = observer.rows.slice(httpStart), http = httpSummary(exchanges, cfg.mode);
    check(count() - startCounter === executed, 'execution-counter-mismatch');
    check(tasks.every(task => !task.localHit && task.remoteHit === hit && task.cache === (hit ? 'HIT' : 'MISS')), 'native-cache-result-mismatch');
    check(tasks.every(task => expected.some(item => item.task === task.task && item.nativeKey === task.nativeKey)), 'native-plan-key-mismatch');
    if (sameKeys) check(JSON.stringify(expected) === JSON.stringify(baseline), 'native-keys-differ-between-profiles');
    if (name === 'changed-source' || name === 'changed-environment') {
      check(expected.every(task => !baseline.some(base => base.task === task.task && base.nativeKey === task.nativeKey)), 'changed-input-retained-native-key');
    }
    check(!existsSync(join(cfg.workspace, '.turbo/cache')), 'filesystem-task-cache-created');
    if (cache === 'local:,remote:r' || fail) check(http.artifactPUTs === 0, 'unexpected-artifact-publication');
    if (hit) check(http.verifiedArchiveGETs === 2, 'missing-exact-remote-archives');
    else {
      const misses = exchanges.filter(row => row.artifact && row.method === 'GET' && row.status === 404);
      check(misses.length === tasks.length && new Set(misses.map(row => row.nativeKey)).size === tasks.length &&
        tasks.every(task => misses.some(row => row.nativeKey === task.nativeKey)), 'native-miss-not-observed');
    }
    if (name === 'cold-writer') {
      const publications = exchanges.filter(row => row.artifact && row.method === 'PUT');
      check(publications.length === 2 && publications.every(row => row.status === 200), 'native-publication-not-acknowledged');
    }
    const outputs = [];
    if (!fail) for (const task of ['build', 'test']) {
      const path = join(cfg.workspace, 'packages/app', task === 'build' ? 'dist' : '.test', 'result.json');
      const bytes = readFileSync(path);
      const expectedBytes = JSON.stringify({ task, source: name === 'changed-source' ? 'two' : 'one', dependency: 'dependency-one', profile, passed: true }) + '\n';
      check(bytes.equals(Buffer.from(expectedBytes)), 'native-output-digest-mismatch');
      outputs.push({ task, bytes: bytes.length, sha256: sha256(bytes) });
    }
    evidence.scenarios.push({ name, exitCode: result.status, commandMs, observationMs, executedTasks: executed, tasks, outputs, http });
    // The action's post hook reports only the baseline under its original
    // compatibility. Controls deliberately use other native inputs/namespaces.
    if (!['cold-writer', 'independent-reader'].includes(name)) rmSync(join(runs, fresh[0]));
  }
  try {
    baseline = keys(cfg.profile);
    if (cfg.mode === 'producer') await run('cold-writer', { cache: 'local:,remote:rw' });
    else {
      await run('independent-reader', { hit: true, executed: 0, sameKeys: true });
      const response = await fetch(new URL(`/v8/artifacts/${'f'.repeat(16)}`, observer.origin), { method: 'PUT',
        headers: { Authorization: `Bearer ${env.TURBO_TOKEN}` }, body: 'reader authority control', redirect: 'error', signal: AbortSignal.timeout(15_000) });
      await response.arrayBuffer();
      check(response.status === 401, 'read-only-put-was-not-denied');
      evidence.deniedReadOnlyPUTStatus = response.status;
    }
    input('two'); await run('changed-source');
    input(); await run('changed-environment', { profile: `${cfg.profile}-changed` });
    const other = `${compatibility}-other`;
    const alternate = await exchanger({ endpoint: cfg.origin, project: cfg.project, compatibility: other, minutes: 5, env });
    assertAuthority(alternate.teamToken, cfg, env, other);
    await observer.close();
    const otherObserver = await observe(cfg.origin, alternate.teamToken, requester);
    Object.assign(observer, otherObserver);
    baseEnv.TURBO_API = observer.origin; baseEnv.TURBO_TOKEN = alternate.teamToken;
    await run('changed-compatibility', { sameKeys: true });
    input('one', true);
    const failurePolicy = cfg.mode === 'producer' ? 'local:,remote:rw' : 'local:,remote:r';
    await run('failed-test-first', { fail: true, executed: 1, cache: failurePolicy });
    await run('failed-test-second', { fail: true, executed: 1, cache: failurePolicy });
    evidence.passed = true;
  } finally {
    await observer.close();
    // The always-run action post hook uses the baseline compatibility. Failed
    // controls may leave a raw summary from another namespace or input set.
    if (!evidence.passed) rmSync(join(cfg.workspace, '.turbo/runs'), { recursive: true, force: true });
    writeFileSync(cfg.output, JSON.stringify(evidence, null, 2) + '\n', { mode: 0o600, flag: 'wx' });
    rmSync(cfg.counter, { force: true });
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const cfg = configuration(process.env);
    if (process.argv[2] === 'prepare') prepare(cfg, process.env);
    else if (process.argv[2] === 'run') await qualify(cfg, process.env);
    else throw new Error('unknown-qualification-command');
    console.log('Native R2 qualification step passed. Only the allowlisted summary may be published.');
  } catch (error) {
    const reason = /^[a-z][a-z0-9-]{1,100}$/.test(error.message || '') ? error.message : 'qualification-failed';
    console.error(`Native R2 qualification failed: ${reason}`);
    process.exitCode = 1;
  }
}
