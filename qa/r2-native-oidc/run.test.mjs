import assert from 'node:assert/strict';
import test from 'node:test';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createHash, randomBytes } from 'node:crypto';
import { execFileSync, spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer, request } from 'node:http';
import { assertAuthority, compatibility, configuration, httpSummary, observe, prepare, qualify, taskSummary } from './run.mjs';

test('operator opt-in, exact event and actual non-default reader ref fail closed', () => {
  const root = mkdtempSync(join(tmpdir(), 'r2-native-policy-'));
  try {
    const event = join(root, 'event.json');
    writeFileSync(event, JSON.stringify({ repository: { default_branch: 'main' } }));
    const env = { R2_QA_ENABLED: 'true', R2_QA_MODE: 'reader', R2_QA_SAMPLE: 'a'.repeat(32),
      R2_QA_ORIGIN: 'https://cache.example', R2_QA_PROJECT: 'private-selector', GITHUB_EVENT_NAME: 'workflow_dispatch',
      GITHUB_WORKSPACE: root, RUNNER_TEMP: root, GITHUB_SHA: 'a'.repeat(40), GITHUB_EVENT_PATH: event,
      GITHUB_REF: 'refs/heads/reader-proof', GITHUB_REPOSITORY: 'example/repository' };
    const cfg = configuration(env);
    assert.equal(cfg.mode, 'reader');
    for (const patch of [{ R2_QA_ENABLED: 'false' }, { GITHUB_EVENT_NAME: 'pull_request' },
      { GITHUB_REF: 'refs/heads/main' }, { GITHUB_REF: 'refs/tags/reader-proof' },
      { R2_QA_SAMPLE: 'a\nprivate-selector' }, { R2_QA_PROJECT: '' },
      { R2_QA_ORIGIN: 'https://secret:password@cache.example' }, { R2_QA_ORIGIN: 'http://cache.example' },
      { R2_QA_ORIGIN: 'https://cache.example/?token=secret' }]) assert.throws(() => configuration({ ...env, ...patch }));
    const claims = { project: cfg.project, integration: 'turbo', compatibility, repository: env.GITHUB_REPOSITORY,
      ref: env.GITHUB_REF, defaultRef: cfg.defaultRef, sourceCommit: env.GITHUB_SHA, capabilities: ['read'],
      expiresAt: new Date(Date.now() + 60_000).toISOString() };
    const token = value => `lc2.${Buffer.from(JSON.stringify(value)).toString('base64url')}.not-a-real-signature`;
    assertAuthority(token(claims), cfg, env);
    assert.throws(() => assertAuthority(token(claims) + '\nprivate-selector', cfg, env), /invalid-capability/);
    for (const patch of [{ capabilities: ['write'] }, { project: 'foreign' }, { compatibility: compatibility + '-other' },
      { sourceCommit: 'b'.repeat(40) }, { expiresAt: new Date(0).toISOString() }]) {
      assert.throws(() => assertAuthority(token({ ...claims, ...patch }), cfg, env), /unexpected-issued-authority/);
    }
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('allowlisted native summaries never copy secret, path, selector or command fields', () => {
  const hidden = 'do-not-publish-this-secret-selector';
  const source = { environmentVariables: { hidden }, path: '/private/runner/workspace', tasks: [{
    taskId: '@layercache/local-ci-reuse-app#build', hash: 'a'.repeat(16), command: hidden,
    environmentVariables: { token: hidden }, cache: { status: 'HIT', local: false, remote: true, private: hidden },
    execution: { exitCode: 0, command: hidden } }] };
  const summary = taskSummary(source);
  assert.deepEqual(summary, [{ task: '@layercache/local-ci-reuse-app#build', nativeKey: 'a'.repeat(16),
    cache: 'HIT', localHit: false, remoteHit: true, exitCode: 0 }]);
  assert.ok(!JSON.stringify(summary).includes(hidden));
  assert.ok(!JSON.stringify(summary).includes('/private'));
  source.tasks[0].execution.exitCode = hidden;
  assert.throws(() => taskSummary(source), /invalid-native-exit-code/);
  source.tasks[0].taskId = hidden;
  assert.throws(() => taskSummary(source), /invalid-native-task/);
});

test('the HTTPS relay rejects 307/308 without sending credentials or requests to the target', async () => {
  let targetRequests = 0;
  const target = createServer((req, res) => { targetRequests++; req.resume(); res.writeHead(200).end(); });
  await new Promise(resolve => target.listen(0, '127.0.0.1', resolve));
  let status = 307, upstreamRequests = 0;
  const upstream = createServer((req, res) => {
    upstreamRequests++; req.resume();
    res.writeHead(status, { Location: `http://127.0.0.1:${target.address().port}/foreign` }).end();
  });
  await new Promise(resolve => upstream.listen(0, '127.0.0.1', resolve));
  // Change transport only inside this credential-free loopback control. The
  // real runner accepts HTTPS and always uses node:https without redirects.
  const requester = (url, options, callback) => request({ ...options, hostname: url.hostname,
    port: url.port, path: url.pathname + url.search }, callback);
  const observer = await observe(`https://127.0.0.1:${upstream.address().port}`, 'synthetic-test-token', requester);
  try {
    for (status of [307, 308]) {
      const result = await fetch(`${observer.origin}/v8/artifacts/${'a'.repeat(16)}`, {
        headers: { Authorization: 'Bearer synthetic-test-token' }, redirect: 'error' });
      assert.equal(result.status, 502);
      await result.arrayBuffer();
    }
    assert.equal(upstreamRequests, 2);
    assert.equal(targetRequests, 0);
    assert.equal(observer.rows.filter(row => row.rejectedRedirect).length, 2);
    assert.throws(() => httpSummary(observer.rows), /native-transport-failed/);
  } finally {
    await observer.close();
    for (const server of [upstream, target]) await new Promise(resolve => { server.close(resolve); server.closeAllConnections(); });
  }
});

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
test('real locked Turbo producer/reader and failure controls work against a credential-free local model', {
  skip: !existsSync(join(repo, 'qa/fixtures/local-ci-reuse/node_modules/.bin/turbo')),
}, async () => {
  const root = mkdtempSync(join(tmpdir(), 'r2-native-local-control-')), objects = new Map();
  let acceptedPUTs = 0;
  const provider = createServer(async (req, res) => {
    const url = new URL(req.url, 'http://localhost');
    const claims = JSON.parse(Buffer.from(req.headers.authorization.split('.')[1], 'base64url'));
    if (url.pathname.endsWith('/status')) { res.writeHead(200).end('{"status":"enabled"}'); return; }
    if (url.pathname.endsWith('/events')) { req.resume(); res.writeHead(200).end('{}'); return; }
    const key = claims.compatibility + url.pathname;
    if (req.method === 'PUT') {
      if (claims.capabilities[0] !== 'write') { req.resume(); res.writeHead(401).end('{}'); return; }
      const chunks = [];
      for await (const chunk of req) chunks.push(chunk);
      const bytes = Buffer.concat(chunks);
      assert.ok(bytes.length < 1 << 20);
      objects.set(key, bytes); acceptedPUTs++; res.writeHead(200).end('{}'); return;
    }
    const bytes = objects.get(key);
    if (!bytes) { res.writeHead(404).end('{}'); return; }
    res.writeHead(200, { 'content-length': bytes.length,
      'x-layercache-digest': `sha256:${createHash('sha256').update(bytes).digest('hex')}` });
    res.end(req.method === 'HEAD' ? undefined : bytes);
  });
  await new Promise(resolve => provider.listen(0, '127.0.0.1', resolve));
  const requester = (url, options, callback) => request({ ...options, hostname: url.hostname,
    port: url.port, path: url.pathname + url.search }, callback);
  const sample = randomBytes(16).toString('hex');
  try {
    for (const mode of ['producer', 'reader']) {
      const temp = join(root, mode); mkdirSync(temp);
      const event = join(temp, 'event.json'); writeFileSync(event, JSON.stringify({ repository: { default_branch: 'main' } }));
      const output = join(temp, 'output'); writeFileSync(output, '');
      const env = { ...process.env, R2_QA_ENABLED: 'true', R2_QA_MODE: mode, R2_QA_SAMPLE: sample,
        R2_QA_ORIGIN: `https://127.0.0.1:${provider.address().port}`, R2_QA_PROJECT: 'synthetic-private-selector',
        GITHUB_EVENT_NAME: 'workflow_dispatch', GITHUB_EVENT_PATH: event, GITHUB_WORKSPACE: repo,
        GITHUB_SHA: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: repo, encoding: 'utf8' }).trim(),
        GITHUB_REF: mode === 'producer' ? 'refs/heads/main' : 'refs/heads/synthetic-reader',
        GITHUB_REPOSITORY: 'example/repository', GITHUB_OUTPUT: output, RUNNER_TEMP: temp };
      const cfg = configuration(env);
      const token = profile => `lc2.${Buffer.from(JSON.stringify({ project: cfg.project, integration: 'turbo', compatibility: profile,
        repository: env.GITHUB_REPOSITORY, ref: env.GITHUB_REF, defaultRef: cfg.defaultRef, sourceCommit: env.GITHUB_SHA,
        capabilities: [mode === 'producer' ? 'write' : 'read'], expiresAt: new Date(Date.now() + 60_000).toISOString(),
      })).toString('base64url')}.synthetic-not-a-signature`;
      prepare(cfg, env);
      try {
        if (mode === 'producer') {
          const config = join(cfg.workspace, 'turbo.json'), original = readFileSync(config);
          rmSync(config);
          const failed = spawnSync(process.execPath, [join(repo, 'qa/r2-native-oidc/run.mjs'), 'run'], {
            cwd: repo, env: { ...env, TURBO_API: cfg.origin, TURBO_TEAM: cfg.project, TURBO_TOKEN: token(compatibility) },
            encoding: 'utf8', timeout: 10_000, maxBuffer: 64 << 10 });
          assert.equal(failed.status, 1, 'invalid dry plan must close the relay and exit without hanging');
          assert.match(failed.stderr, /native-dry-plan-failed/);
          assert.ok(!failed.stderr.includes(cfg.project) && !failed.stderr.includes(cfg.workspace));
          writeFileSync(config, original); rmSync(cfg.output, { force: true });
        }
        await qualify(cfg, { ...env, TURBO_API: cfg.origin, TURBO_TEAM: cfg.project, TURBO_TOKEN: token(compatibility) },
          { requester, exchanger: async input => ({ teamToken: token(input.compatibility) }) });
        const summary = JSON.parse(readFileSync(cfg.output, 'utf8'));
        assert.equal(summary.passed, true);
        assert.equal(summary.scenarios.length, 6);
        assert.equal(summary.scenarios[0].executedTasks, mode === 'producer' ? 2 : 0);
        if (mode === 'reader') assert.equal(summary.deniedReadOnlyPUTStatus, 401);
        const text = JSON.stringify(summary);
        for (const secret of [cfg.project, sample, cfg.origin, cfg.workspace, env.TURBO_TOKEN || 'synthetic-not-a-signature']) {
          assert.ok(!text.includes(secret));
        }
      } finally { rmSync(cfg.workspace, { recursive: true, force: true }); }
    }
    assert.equal(acceptedPUTs, 2);
  } finally {
    await new Promise(resolve => { provider.close(resolve); provider.closeAllConnections(); });
    rmSync(root, { recursive: true, force: true });
  }
});
