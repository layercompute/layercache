import test from 'node:test';
import assert from 'node:assert/strict';
import { createHmac } from 'node:crypto';
import { appendFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { fileCommands } from '../setup/auth.ts';
import { setupGradle } from './main.mjs';
import { cleanupGradle } from './post.mjs';

const here = dirname(fileURLToPath(import.meta.url));
function fileValues(path) {
  const values = {};
  const lines = readFileSync(path, 'utf8').split('\n');
  for (let index = 0; index < lines.length - 1; index++) {
    const [key, delimiter] = lines[index].split('<<');
    assert.ok(delimiter, 'complete file-command record');
    const content = [];
    while (lines[++index] !== delimiter) {
      assert.ok(index < lines.length, 'terminated file-command record');
      content.push(lines[index]);
    }
    values[key] = content.join('\n');
  }
  return values;
}
function job() {
  const root = mkdtempSync(join(tmpdir(), 'layercache-gradle-action-test-'));
  const env = {
    RUNNER_TEMP: root, GITHUB_ENV: join(root, 'env'), GITHUB_OUTPUT: join(root, 'output'), GITHUB_STATE: join(root, 'state'),
    GITHUB_REPOSITORY: 'acme/widget', GITHUB_REF: 'refs/heads/main', GITHUB_EVENT_NAME: 'push',
    'INPUT_TEAM-URL': 'https://cache.example/v1', INPUT_PROJECT: 'team-widget', INPUT_COMPATIBILITY: 'linux-x64-android36-schema1',
    ACTIONS_ID_TOKEN_REQUEST_URL: 'https://oidc.example/token?fixture=1', ACTIONS_ID_TOKEN_REQUEST_TOKEN: 'request-fixture',
    LAYER_CACHE_GRADLE_URL: 'https://stale.example/', LAYER_CACHE_GRADLE_TOKEN: 'stale-token', LAYER_CACHE_GRADLE_PUSH: '1',
    LAYER_CACHE_GRADLE_LOCAL_ENABLED: '0',
  };
  return { root, env, cleanup: () => rmSync(root, { recursive: true, force: true }) };
}

async function issuer(options = {}) {
  const requests = [];
  const expiresAt = options.expiresAt ?? new Date(Date.now() + 3_600_000).toISOString();
  let token;
  const server = createServer(async (request, response) => {
    const body = [];
    for await (const chunk of request) body.push(chunk);
    requests.push({ method: request.method, url: request.url, authorization: request.headers.authorization, body: Buffer.concat(body).toString() });
    response.setHeader('content-type', 'application/json');
    if (request.url.startsWith('/token?')) {
      response.end(JSON.stringify({ value: 'id-token-fixture' }));
      return;
    }
    assert.equal(request.url, '/v1/auth/github-oidc/exchange');
    if (options.status) { response.writeHead(options.status).end('{}'); return; }
    const input = JSON.parse(Buffer.concat(body).toString());
    assert.equal(input.idToken, 'id-token-fixture');
    assert.equal(input.integration, 'gradle');
    const claims = { project: input.project, integration: input.integration, compatibility: input.compatibility,
      repository: 'acme/widget', ref: 'refs/heads/main', defaultRef: 'refs/heads/main', capabilities: ['write'], expiresAt, ...options.claims };
    const payload = Buffer.from(JSON.stringify(claims)).toString('base64url');
    token = `lc2.${payload}.${createHmac('sha256', 'disposable-fixture').update(payload).digest('base64url')}`;
    response.end(JSON.stringify({ teamToken: token, expiresAt }));
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const base = `http://127.0.0.1:${server.address().port}`;
  const fetcher = (url, init) => {
    assert.equal(init.redirect, 'error');
    assert.ok(init.signal instanceof AbortSignal);
    return fetch(new URL(new URL(url).pathname + new URL(url).search, base), init);
  };
  return { requests, fetcher, token: () => token, close: () => new Promise(resolve => server.close(resolve)) };
}

test('OIDC setup exports one scoped native connection and an exact credential-free init script', async () => {
  const fixture = job();
  const oauth = await issuer();
  const messages = [];
  try {
    fixture.env['INPUT_PUSH'] = 'true';
    fixture.env['INPUT_TTL-MINUTES'] = '20';
    const result = await setupGradle(fixture.env, { fetcher: oauth.fetcher, log: text => messages.push(text) });
    assert.equal(result.configured, true);
    assert.equal(result.writable, true);
    assert.equal(statSync(result.root).mode & 0o777, 0o700);
    assert.equal(statSync(result.initScript).mode & 0o777, 0o600);
    assert.deepEqual(readFileSync(result.initScript), readFileSync(join(here, '../../native/gradle/init.gradle')));
    assert.equal(readFileSync(result.initScript, 'utf8').includes(oauth.token()), false);
    const exported = fileValues(fixture.env.GITHUB_ENV);
    assert.equal(exported.LAYER_CACHE_GRADLE_URL, 'https://cache.example/v1/gradle-cache/');
    assert.equal(exported.LAYER_CACHE_GRADLE_PUSH, '1');
    assert.equal(exported.LAYER_CACHE_GRADLE_TOKEN, oauth.token());
    assert.equal(fixture.env.LAYER_CACHE_GRADLE_LOCAL_ENABLED, '0');
    assert.equal(exported.LAYER_CACHE_GRADLE_LOCAL_ENABLED, undefined);
    assert.deepEqual(fileValues(fixture.env.GITHUB_OUTPUT), { configured: 'true', 'init-script': result.initScript, 'expires-at': result.expiresAt });
    assert.equal(oauth.requests[0].authorization, 'Bearer request-fixture');
    assert.equal(new URL(oauth.requests[0].url, 'https://oidc.example').searchParams.get('audience'), 'layercache:team-widget');
    assert.deepEqual(JSON.parse(oauth.requests[1].body), { project: 'team-widget', compatibility: 'linux-x64-android36-schema1', idToken: 'id-token-fixture', integration: 'gradle', ttlSeconds: 1200 });
    assert.ok(messages.includes(`::add-mask::${oauth.token()}\n`));
    assert.ok(messages.includes('::add-mask::id-token-fixture\n'));
    assert.ok(messages.filter(text => !text.startsWith('::add-mask::')).every(text => !text.includes(oauth.token()) && !text.includes('id-token-fixture')));
    cleanupGradle({ STATE_gradle_root: result.root, RUNNER_TEMP: fixture.root });
    assert.equal(existsSync(result.root), false);
  } finally { await oauth.close(); fixture.cleanup(); }
});

test('push is opt-in and limited to server writers on trusted default-branch events', async () => {
  const cases = [
    { label: 'default', push: undefined, event: 'push', ref: 'refs/heads/main', caps: ['write'], expected: '0' },
    { label: 'PR reader', push: 'true', event: 'pull_request', ref: 'refs/pull/1/merge', caps: ['read'], expected: '0' },
    { label: 'target event', push: 'true', event: 'pull_request_target', ref: 'refs/heads/main', caps: ['write'], expected: '0' },
    { label: 'workflow run', push: 'true', event: 'workflow_run', ref: 'refs/heads/main', caps: ['write'], expected: '0' },
    { label: 'unknown event', push: 'true', event: '', ref: 'refs/heads/main', caps: ['write'], expected: '0' },
    { label: 'feature branch', push: 'true', event: 'push', ref: 'refs/heads/feature', caps: ['write'], expected: '0' },
    { label: 'server reader', push: 'true', event: 'push', ref: 'refs/heads/main', caps: ['read'], expected: '0' },
    { label: 'scheduled main', push: 'true', event: 'schedule', ref: 'refs/heads/main', caps: ['write'], expected: '1' },
    { label: 'manual main', push: 'true', event: 'workflow_dispatch', ref: 'refs/heads/main', caps: ['write'], expected: '1' },
  ];
  for (const row of cases) {
    const fixture = job();
    const oauth = await issuer({ claims: { ref: row.ref, capabilities: row.caps } });
    try {
      fixture.env.GITHUB_EVENT_NAME = row.event;
      fixture.env.GITHUB_REF = row.ref;
      if (row.push) fixture.env.INPUT_PUSH = row.push;
      const result = await setupGradle(fixture.env, { fetcher: oauth.fetcher, log: () => {} });
      assert.equal(result.configured, true, row.label);
      assert.equal(fileValues(fixture.env.GITHUB_ENV).LAYER_CACHE_GRADLE_PUSH, row.expected, row.label);
    } finally { await oauth.close(); fixture.cleanup(); }
  }
});

test('authentication denial, network outage, missing permission and stale credentials leave a fresh fallback', async () => {
  for (const failure of ['denied', 'offline', 'permission', 'expired']) {
    const fixture = job();
    const oauth = await issuer(failure === 'denied' ? { status: 401 } : failure === 'expired' ? { expiresAt: new Date(Date.now() - 1000).toISOString() } : {});
    const messages = [];
    try {
      if (failure === 'permission') delete fixture.env.ACTIONS_ID_TOKEN_REQUEST_TOKEN;
      const fetcher = failure === 'offline' ? async () => { throw new Error('network error containing a token that must not be printed'); } : oauth.fetcher;
      const result = await setupGradle(fixture.env, { fetcher, log: text => messages.push(text) });
      assert.deepEqual(result, { configured: false });
      assert.deepEqual(fileValues(fixture.env.GITHUB_ENV), { LAYER_CACHE_GRADLE_URL: '', LAYER_CACHE_GRADLE_PUSH: '0', LAYER_CACHE_GRADLE_TOKEN: '' });
      assert.equal(fixture.env.LAYER_CACHE_GRADLE_TOKEN, '');
      assert.equal(fixture.env.LAYER_CACHE_GRADLE_URL, '');
      assert.equal(fileValues(fixture.env.GITHUB_OUTPUT).configured, 'false');
      assert.ok(messages.some(text => text.includes('authentication unavailable')));
      assert.ok(messages.every(text => !text.includes('network error containing')));
    } finally { await oauth.close(); fixture.cleanup(); }
  }
});

test('a capability for another profile, project or integration is not exported', async () => {
  for (const claims of [{ compatibility: 'another-profile' }, { project: 'another-project' }, { integration: 'turbo' }, { repository: 'other/repository' }, { expiresAt: new Date(Date.now() + 60_000).toISOString() }]) {
    const fixture = job();
    const oauth = await issuer({ claims });
    try {
      const result = await setupGradle(fixture.env, { fetcher: oauth.fetcher, log: () => {} });
      assert.equal(result.configured, false);
      assert.equal(fileValues(fixture.env.GITHUB_ENV).LAYER_CACHE_GRADLE_TOKEN, '');
    } finally { await oauth.close(); fixture.cleanup(); }
  }
});

test('invalid explicit inputs fail clearly after clearing inherited Gradle authority', async () => {
  for (const [key, value] of [
    ['INPUT_TEAM-URL', ''], ['INPUT_TEAM-URL', 'http://cache.example/'], ['INPUT_TEAM-URL', 'https://user:secret@cache.example/'],
    ['INPUT_TEAM-URL', 'https://cache.example/?token=secret'], ['INPUT_TEAM-URL', 'https://cache.example/#secret'], ['INPUT_TEAM-URL', 'https://cache.example:0/'],
    ['INPUT_PROJECT', ''], ['INPUT_PROJECT', 'project\nTOKEN=bad'], ['INPUT_COMPATIBILITY', ''], ['INPUT_COMPATIBILITY', 'Uppercase'],
    ['INPUT_PUSH', 'yes'], ['INPUT_TTL-MINUTES', '61'], ['INPUT_TTL-MINUTES', '0'], ['INPUT_TTL-MINUTES', '1.2'], ['GITHUB_REPOSITORY', ''],
  ]) {
    const fixture = job();
    try {
      fixture.env[key] = value;
      await assert.rejects(setupGradle(fixture.env, { fetcher: async () => assert.fail('invalid inputs must not request credentials'), log: () => {} }));
      assert.equal(fileValues(fixture.env.GITHUB_ENV).LAYER_CACHE_GRADLE_TOKEN, '');
      assert.equal(fileValues(fixture.env.GITHUB_OUTPUT).configured, 'false');
    } finally { fixture.cleanup(); }
  }
});

test('GitHub file-command batches roll back a partial credential append', () => {
  const fixture = job();
  try {
    fileCommands(fixture.env.GITHUB_ENV, { LAYER_CACHE_GRADLE_URL: '', LAYER_CACHE_GRADLE_TOKEN: '' });
    const before = readFileSync(fixture.env.GITHUB_ENV);
    assert.throws(() => fileCommands(fixture.env.GITHUB_ENV, { LAYER_CACHE_GRADLE_URL: 'https://cache.example/', LAYER_CACHE_GRADLE_TOKEN: 'secret' },
      (descriptor, records) => { appendFileSync(descriptor, records.slice(0, records.indexOf('secret') + 3)); throw new Error('simulated full disk'); }), /simulated full disk/);
    assert.deepEqual(readFileSync(fixture.env.GITHUB_ENV), before);
    assert.equal(fileValues(fixture.env.GITHUB_ENV).LAYER_CACHE_GRADLE_TOKEN, '');
    assert.throws(() => fileCommands(fixture.env.GITHUB_ENV, { VALID: 'ok', 'INVALID\nKEY': 'bad' }));
    assert.deepEqual(readFileSync(fixture.env.GITHUB_ENV), before);
  } finally { fixture.cleanup(); }
});

test('runner file I/O failures remain fatal and are not authentication fallback', async () => {
  const fixture = job();
  const messages = [];
  try {
    fixture.env.GITHUB_ENV = fixture.root;
    await assert.rejects(setupGradle(fixture.env, { log: text => messages.push(text) }));
    assert.equal(messages.some(text => text.includes('authentication unavailable')), false);
  } finally { fixture.cleanup(); }
});

test('failed output publication clears the authenticated connection and removes the staged script', async () => {
  const fixture = job();
  const oauth = await issuer();
  const messages = [];
  try {
    const fetcher = async (url, init) => {
      const response = await oauth.fetcher(url, init);
      if (init.method === 'POST') {
        rmSync(fixture.env.GITHUB_OUTPUT);
        mkdirSync(fixture.env.GITHUB_OUTPUT);
      }
      return response;
    };
    await assert.rejects(setupGradle(fixture.env, { fetcher, log: text => messages.push(text) }));
    assert.equal(fileValues(fixture.env.GITHUB_ENV).LAYER_CACHE_GRADLE_TOKEN, '');
    assert.equal(fileValues(fixture.env.GITHUB_ENV).LAYER_CACHE_GRADLE_URL, '');
    assert.equal(fixture.env.LAYER_CACHE_GRADLE_TOKEN, '');
    assert.equal(existsSync(fileValues(fixture.env.GITHUB_STATE).gradle_root), false);
    assert.equal(messages.some(text => text.includes('authentication unavailable')), false);
  } finally { await oauth.close(); fixture.cleanup(); }
});

test('post cleanup refuses directories outside the owned runner location', () => {
  const fixture = job();
  try {
    assert.throws(() => cleanupGradle({ STATE_gradle_root: fixture.root, RUNNER_TEMP: fixture.root }));
    assert.equal(existsSync(fixture.root), true);
  } finally { fixture.cleanup(); }
});
