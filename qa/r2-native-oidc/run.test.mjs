import assert from 'node:assert/strict';
import test from 'node:test';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { createHash, randomBytes } from 'node:crypto';
import { execFileSync, spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
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

test('an action capability from one origin cannot be retargeted before any request', async () => {
  let requests = 0, exchanges = 0;
  await assert.rejects(qualify({ origin: 'https://origin-b.example', project: 'synthetic-selector' }, {
    TURBO_API: 'https://origin-a.example', TURBO_TEAM: 'synthetic-selector', TURBO_TOKEN: 'already-issued-origin-a-token',
  }, { requester: () => { requests++; throw new Error('must-not-request'); },
    exchanger: () => { exchanges++; throw new Error('must-not-exchange'); } }), /pinned-action-environment-required/);
  assert.equal(requests, 0);
  assert.equal(exchanges, 0);
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

test('body truncation, stalls, byte limits and wrong digests retain strict relay failures', async () => {
  for (const mode of ['truncated', 'stalled', 'oversize', 'wrong-digest']) {
    const upstream = createServer((req, res) => {
      req.resume();
      if (mode === 'wrong-digest') {
        res.writeHead(200, { 'x-layercache-digest': `sha256:${'0'.repeat(64)}` }).end('fixture'); return;
      }
      if (mode === 'oversize') { res.writeHead(200).end(Buffer.alloc((8 << 20) + 1)); return; }
      res.writeHead(200, { 'Content-Length': 10 }); res.flushHeaders(); res.write('partial');
      if (mode === 'truncated') setTimeout(() => res.destroy(), 20);
    });
    await new Promise(resolve => upstream.listen(0, '127.0.0.1', resolve));
    const requester = (url, options, callback) => request({ ...options, hostname: url.hostname,
      port: url.port, path: url.pathname + url.search }, callback);
    const observer = await observe(`https://127.0.0.1:${upstream.address().port}`, 'synthetic', requester);
    try {
      // Like Turbo's optional event response, close after headers. The relay
      // still has to consume the real upstream body or record a hard failure.
      await new Promise((resolve, reject) => {
        const client = request(`${observer.origin}/v8/artifacts/${'a'.repeat(16)}`,
          { headers: { Authorization: 'Bearer synthetic' } }, response => { response.destroy(); resolve(); });
        client.once('error', reject); client.end();
      });
      if (mode === 'stalled') await assert.rejects(observer.settle(40), /native-observation-timeout/);
      else await observer.settle();
      assert.ok(observer.rows.every(row => !row.digestVerified));
      if (mode === 'wrong-digest') assert.equal(httpSummary(observer.rows).verifiedArchiveGETs, 0);
      else {
        assert.throws(() => httpSummary(observer.rows), /native-transport-failed/);
        assert.ok(observer.rows.every(row => row.completeMs === 0));
      }
    } finally {
      await observer.close();
      await new Promise(resolve => { upstream.close(resolve); upstream.closeAllConnections(); });
    }
  }
});

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
test('real locked Turbo waits for delayed alternate-profile events and preserves producer/reader controls', {
  skip: !existsSync(join(repo, 'qa/fixtures/local-ci-reuse/node_modules/.bin/turbo')),
}, async () => {
  const root = mkdtempSync(join(tmpdir(), 'r2-native-local-control-')), objects = new Map();
  let acceptedPUTs = 0, artifactPUTStatus = 200, rejectedPUTs = 0, metadataSuccesses = 0, failAlternateBody = false;
  const provider = createServer(async (req, res) => {
    const url = new URL(req.url, 'http://localhost');
    const claims = JSON.parse(Buffer.from(req.headers.authorization.split('.')[1], 'base64url'));
    if (url.pathname.endsWith('/status')) { metadataSuccesses++; res.writeHead(200).end('{"status":"enabled"}'); return; }
    if (url.pathname.endsWith('/events')) {
      metadataSuccesses++; req.resume();
      res.writeHead(200, { 'Content-Length': 2 }); res.flushHeaders();
      if (claims.compatibility.endsWith('-other') && failAlternateBody) {
        res.write('{'); await delay(20); res.destroy(); return;
      }
      if (claims.compatibility.endsWith('-other')) await delay(1000);
      res.end('{}'); return;
    }
    const key = claims.compatibility + url.pathname;
    if (req.method === 'PUT') {
      if (claims.capabilities[0] !== 'write') { req.resume(); res.writeHead(401).end('{}'); return; }
      const chunks = [];
      for await (const chunk of req) chunks.push(chunk);
      const bytes = Buffer.concat(chunks);
      assert.ok(bytes.length < 1 << 20);
      if (artifactPUTStatus !== 200) { rejectedPUTs++; res.writeHead(artifactPUTStatus).end('{}'); return; }
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
    for (const label of ['producer', 'reader', 'failed-alternate-body', 'rejected-publication']) {
      const mode = label === 'reader' ? 'reader' : 'producer';
      artifactPUTStatus = label === 'rejected-publication' ? 404 : 200;
      failAlternateBody = label === 'failed-alternate-body';
      rejectedPUTs = 0; metadataSuccesses = 0;
      const temp = join(root, label); mkdirSync(temp);
      const event = join(temp, 'event.json'); writeFileSync(event, JSON.stringify({ repository: { default_branch: 'main' } }));
      const output = join(temp, 'output'); writeFileSync(output, '');
      const env = { ...process.env, R2_QA_ENABLED: 'true', R2_QA_MODE: mode,
        R2_QA_SAMPLE: ['producer', 'reader'].includes(label) ? sample : randomBytes(16).toString('hex'),
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
      const summaries = () => existsSync(join(cfg.workspace, '.turbo/runs'))
        ? readdirSync(join(cfg.workspace, '.turbo/runs')).filter(name => name.endsWith('.json')) : [];
      try {
        if (label === 'failed-alternate-body') {
          await assert.rejects(qualify(cfg, { ...env, TURBO_API: cfg.origin, TURBO_TEAM: cfg.project, TURBO_TOKEN: token(compatibility) },
            { requester, exchanger: async input => ({ teamToken: token(input.compatibility) }) }), /native-transport-failed/);
          const partial = JSON.parse(readFileSync(cfg.output, 'utf8'));
          assert.equal(partial.passed, false);
          assert.deepEqual(partial.scenarios.map(scenario => scenario.name), ['cold-writer', 'changed-source', 'changed-environment']);
          assert.deepEqual(summaries(), [], 'failed alternate-profile controls must leave nothing for the baseline post hook');
          continue;
        }
        if (label === 'rejected-publication') {
          let rejection;
          try {
            await qualify(cfg, { ...env, TURBO_API: cfg.origin, TURBO_TEAM: cfg.project, TURBO_TOKEN: token(compatibility) },
              { requester, exchanger: async input => ({ teamToken: token(input.compatibility) }) });
          } catch (error) { rejection = error; }
          assert.equal(rejectedPUTs, 2);
          assert.ok(metadataSuccesses >= 2);
          assert.ok(rejection, 'two rejected artifact PUTs must not qualify despite successful status/events requests');
          assert.match(rejection.message, /native-publication-not-acknowledged/);
          assert.equal(JSON.parse(readFileSync(cfg.output, 'utf8')).passed, false);
          assert.deepEqual(summaries(), []);
          continue;
        }
        if (mode === 'producer') {
          const config = join(cfg.workspace, 'turbo.json'), original = readFileSync(config);
          rmSync(config);
          const failed = spawnSync(process.execPath, [join(repo, 'qa/r2-native-oidc/run.mjs'), 'run'], {
            cwd: repo, env: { ...env, TURBO_API: cfg.origin, TURBO_TEAM: cfg.project, TURBO_TOKEN: token(compatibility) },
            encoding: 'utf8', timeout: 10_000, maxBuffer: 64 << 10 });
          assert.equal(failed.status, 1, 'invalid dry plan must close the relay and exit without hanging');
          assert.match(failed.stderr, /native-dry-plan-failed/);
          assert.ok(!failed.stderr.includes(cfg.project) && !failed.stderr.includes(cfg.workspace));
          assert.deepEqual(summaries(), []);
          writeFileSync(config, original); rmSync(cfg.output, { force: true });
        }
        await qualify(cfg, { ...env, TURBO_API: cfg.origin, TURBO_TEAM: cfg.project, TURBO_TOKEN: token(compatibility) },
          { requester, exchanger: async input => ({ teamToken: token(input.compatibility) }) });
        const summary = JSON.parse(readFileSync(cfg.output, 'utf8'));
        assert.equal(summary.passed, true);
        assert.equal(summary.scenarios.length, 6);
        assert.equal(summaries().length, 1, 'successful qualification retains only its baseline for the post hook');
        assert.ok(summary.scenarios.every(scenario => Number.isFinite(scenario.observationMs) && scenario.observationMs >= 0));
        assert.equal(summary.scenarios[0].executedTasks, mode === 'producer' ? 2 : 0);
        if (mode === 'reader') assert.equal(summary.deniedReadOnlyPUTStatus, 401);
        const text = JSON.stringify(summary);
        for (const secret of [cfg.project, sample, cfg.origin, cfg.workspace, env.TURBO_TOKEN || 'synthetic-not-a-signature']) {
          assert.ok(!text.includes(secret));
        }
      } finally { rmSync(cfg.workspace, { recursive: true, force: true }); }
    }
    assert.equal(acceptedPUTs, 4);
  } finally {
    await new Promise(resolve => { provider.close(resolve); provider.closeAllConnections(); });
    rmSync(root, { recursive: true, force: true });
  }
});
