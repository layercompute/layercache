import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, writeFileSync, mkdirSync, readFileSync, existsSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { endpointURL, exchangeTurbo, fileCommand, setup } from './main.mjs';
import { cleanup } from './post.mjs';
import './execution.test.ts';

test('endpoints reject plaintext and credential-bearing remote URLs', () => {
  for (const value of ['http://cache.example', 'https://user:pass@cache.example', 'https://cache.example?q=secret', 'https://cache.example/#a']) {
    assert.throws(() => endpointURL(value));
  }
  assert.equal(endpointURL('https://cache.example/').origin, 'https://cache.example');
});

test('OIDC exchange requests only Turbo with bounded lifetime and no redirects', async () => {
  const requests = [];
  const result = await exchangeTurbo({
    log: () => {},
    endpoint: 'https://cache.example/v1', project: 'github.com/acme/widget', compatibility: 'linux-amd64-schema1', minutes: 60,
    env: { ACTIONS_ID_TOKEN_REQUEST_URL: 'https://oidc.example/token', ACTIONS_ID_TOKEN_REQUEST_TOKEN: 'request-fixture' },
    fetcher: async (url, init) => {
      requests.push([url, init]);
      return { ok: true, json: async () => requests.length === 1 ? { value: 'id-token-fixture' } : { teamToken: 'turbo-token-fixture', expiresAt: new Date(Date.now() + 3_600_000).toISOString() } };
    },
  });
  assert.equal(result.teamToken, 'turbo-token-fixture');
  assert.equal(requests[0][0].searchParams.get('audience'), 'layercache:github.com/acme/widget');
  assert.equal(requests[1][0].href, 'https://cache.example/v1/auth/github-oidc/exchange');
  assert.equal(JSON.parse(requests[1][1].body).integration, 'turbo');
  assert.equal(JSON.parse(requests[1][1].body).ttlSeconds, 3600);
  assert.ok(requests.every(([, init]) => init.redirect === 'error'));
});

test('optional execution reporting never changes native exchange authority or its success', async () => {
  for (const integration of ['turbo', 'gradle', 'buildkit']) {
    for (const failedReport of [false, true]) {
      const requests = [];
      const masks = [];
      const credentials = { teamToken: 'native-execution-token', expiresAt: new Date(Date.now() + 1800_000).toISOString(),
        executionObservations: { version: 1, path: '/v1/executions/observations' } };
      const result = await exchangeTurbo({ endpoint: 'https://cache.example', project: 'github.com/acme/widget',
        compatibility: 'linux-amd64-schema1', minutes: 30, integration, observationPhase: 'finish',
        env: { ACTIONS_ID_TOKEN_REQUEST_URL: 'https://oidc.example/token', ACTIONS_ID_TOKEN_REQUEST_TOKEN: 'request-token', 'INPUT_EXECUTION-PROVIDER': 'ubicloud' },
        log: value => masks.push(value),
        fetcher: async (url, input) => {
          requests.push([url, input]);
          if (requests.length === 1) return Response.json({ value: 'identity-token' });
          if (requests.length === 2) return Response.json(credentials);
          assert.ok(masks.some(value => value.includes('::add-mask::native-execution-token')));
          assert.equal(String(url), 'https://cache.example/v1/executions/observations');
          assert.equal(input.redirect, 'error');
          assert.equal(input.headers.Authorization, 'Bearer native-execution-token');
          assert.equal(input.headers['X-LayerCache-Project'], 'github.com/acme/widget');
          const body = JSON.parse(input.body);
          assert.equal(body.version, 1);
          assert.equal(body.phase, 'finish');
          assert.equal(body.provider, 'ubicloud');
          assert.equal(body.installationId, undefined);
          assert.equal(body.repository, undefined);
          assert.ok(!input.body.includes('identity-token'));
          assert.ok(!input.body.includes('native-execution-token'));
          if (failedReport) throw new Error('private-observation-error');
          return new Response('private-server-body', { status: 503 });
        },
      });
      assert.equal(requests.length, 3);
      assert.equal(result.teamToken, credentials.teamToken);
      assert.deepEqual(JSON.parse(requests[1][1].body), { project: 'github.com/acme/widget', compatibility: 'linux-amd64-schema1', idToken: 'identity-token', integration, ttlSeconds: 1800 });
    }
  }
});

test('file commands encode multiline values in independent records', () => {
  const root = mkdtempSync(join(tmpdir(), 'layercache-file-command-'));
  try {
    const path = join(root, 'env');
    fileCommand(path, 'TURBO_TOKEN', 'secret\nUNTRUSTED=value');
    const lines = readFileSync(path, 'utf8').split('\n');
    const delimiter = lines[0].split('<<')[1];
    assert.equal(lines[3], delimiter);
    assert.throws(() => fileCommand(path, 'KEY\nOTHER', 'value'));
  } finally { rmSync(root, { recursive: true }); }
});

test('cleanup refuses paths outside a job directory', () => {
  assert.throws(() => cleanup({ STATE_root: tmpdir(), RUNNER_TEMP: tmpdir(), STATE_config: join(tmpdir(), 'config.json') }));
});

test('failed shutdown preserves runtime files', () => {
  const root = mkdtempSync(join(tmpdir(), 'layercache-job-'));
  try {
    writeFileSync(join(root, 'config.json'), '{}');
    assert.throws(() => cleanup({ RUNNER_TEMP: tmpdir(), STATE_root: root, STATE_binary: '/bin/false', STATE_config: join(root, 'config.json') }));
    assert.equal(existsSync(join(root, 'config.json')), true);
  } finally { rmSync(root, { recursive: true }); }
});

test('installed setup starts a scoped runtime, serves Turbo, and post removes owned files', { skip: !process.env.LAYERCACHE_SETUP_BINARY }, async () => {
  const messages = [];
  const log = value => messages.push(value);
  const root = mkdtempSync(join(tmpdir(), 'layercache-setup-qa-'));
  const workspace = join(root, 'workspace');
  mkdirSync(workspace);
  const event = join(root, 'event.json');
  writeFileSync(event, JSON.stringify({ repository: { default_branch: 'main' } }));
  let installation;
  try {
    execFileSync('git', ['init', '--quiet', '-b', 'main', workspace]);
    execFileSync('git', ['-C', workspace, 'remote', 'add', 'origin', 'https://github.com/acme/widget.git']);
    execFileSync('git', ['-C', workspace, '-c', 'user.name=QA', '-c', 'user.email=qa@example.invalid', 'commit', '--quiet', '--allow-empty', '-m', 'fixture']);
    const sha = execFileSync('git', ['-C', workspace, 'rev-parse', 'HEAD'], { encoding: 'utf8' }).trim();
    const env = { ...process.env, 'INPUT_BINARY': resolve(process.env.LAYERCACHE_SETUP_BINARY), 'INPUT_MAX-SIZE': '10485760',
      RUNNER_TEMP: root, GITHUB_WORKSPACE: workspace, GITHUB_REPOSITORY: 'acme/widget', GITHUB_REF: 'refs/heads/main', GITHUB_SHA: sha,
      GITHUB_EVENT_PATH: event, GITHUB_STATE: join(root, 'state'), GITHUB_ENV: join(root, 'env'), GITHUB_PATH: join(root, 'path'), GITHUB_OUTPUT: join(root, 'output') };
    installation = await setup(env, { log });
    assert.match(installation.route.compatibility, new RegExp(`node@${process.versions.node.split('.')[0]}`));
    assert.match(installation.route.compatibility, /go@/);
    const turboToken = installation.route.environment.TURBO_TOKEN;
    const url = `${installation.endpoint}/v8/artifacts/setup-smoke`;
    const headers = { Authorization: `Bearer ${turboToken}`, 'x-layercache-compatibility': installation.route.compatibility };
    const saved = await fetch(url, { method: 'PUT', headers, body: 'setup-cache-payload' });
    assert.equal(saved.status, 200);
    const restored = await fetch(url, { headers });
    assert.equal(await restored.text(), 'setup-cache-payload');
    const denied = await fetch(`${installation.endpoint}/v1/gc`, { method: 'POST', headers });
    assert.equal(denied.status, 401);
    const exported = readFileSync(env.GITHUB_ENV, 'utf8');
    assert.match(exported, /TURBO_API<</);
    assert.doesNotMatch(exported, /ACTIONS_RUNTIME_TOKEN|ACTIONS_CACHE_URL/);
    cleanup({ RUNNER_TEMP: root, STATE_root: installation.root, STATE_binary: installation.binary, STATE_config: installation.config });
    assert.equal(existsSync(installation.root), false);
    installation = null;

    env['INPUT_TEAM-URL'] = 'https://offline-team.example';
    env.INPUT_PROJECT = 'team-widget';
    env.INPUT_COMPATIBILITY = 'explicit-test-schema1';
    delete env.ACTIONS_ID_TOKEN_REQUEST_URL;
    delete env.ACTIONS_ID_TOKEN_REQUEST_TOKEN;
    installation = await setup(env, { log });
    assert.equal(JSON.parse(readFileSync(installation.config, 'utf8')).teamUrl, undefined);
    assert.equal(installation.route.project, 'team-widget');
    assert.equal(installation.route.compatibility, 'explicit-test-schema1');
    assert.ok(messages.some(value => value.includes('Team Cache authentication unavailable')));
    cleanup({ RUNNER_TEMP: root, STATE_root: installation.root, STATE_binary: installation.binary, STATE_config: installation.config });
    installation = null;
  } finally {
    if (installation) cleanup({ RUNNER_TEMP: root, STATE_root: installation.root, STATE_binary: installation.binary, STATE_config: installation.config });
    rmSync(root, { recursive: true });
  }
});
