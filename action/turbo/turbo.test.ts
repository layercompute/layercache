import test from 'node:test';
import assert from 'node:assert/strict';
import { setupTurbo } from './main.ts';

const env = {
  'INPUT_TEAM-URL': 'https://cache.example',
  INPUT_COMPATIBILITY: 'linux-amd64-node24',
  GITHUB_REPOSITORY: 'Acme/Widget',
  GITHUB_WORKSPACE: process.cwd(),
  GITHUB_STATE: 'state',
  GITHUB_ENV: 'env',
  GITHUB_OUTPUT: 'output',
  ACTIONS_ID_TOKEN_REQUEST_URL: 'https://oidc.example/token',
  ACTIONS_ID_TOKEN_REQUEST_TOKEN: 'request-fixture',
};
test('native Turbo action exchanges OIDC and exports only Turbo settings', async () => {
  const requests: Array<[string | URL | Request, RequestInit | undefined]> = [];
  const writes: Array<[string, string, string]> = [];
  const logs: string[] = [];
  await setupTurbo(env, {
    fetcher: async (url, init) => {
      requests.push([url, init]);
      return Response.json(
        requests.length === 1
          ? { value: 'identity-fixture' }
          : {
              teamToken: 'token-fixture',
              expiresAt: new Date(Date.now() + 1800_000).toISOString(),
            },
      );
    },
    log: (value) => logs.push(value),
    write: (...args) => writes.push(args),
  });
  const body = requests[1]?.[1]?.body;
  assert.equal(typeof body, 'string');
  assert.equal(JSON.parse(String(body)).project, 'github.com/acme/widget');
  assert.equal(JSON.parse(String(body)).integration, 'turbo');
  assert.deepEqual(
    writes.filter(([path]) => path === 'env').map(([, key]) => key),
    ['TURBO_API', 'TURBO_TEAM', 'TURBO_TOKEN', 'TURBO_RUN_SUMMARY'],
  );
  assert.ok(logs.some((line) => line.includes('::add-mask::token-fixture')));
});
test('invalid configuration makes no request or environment write', async () => {
  for (const override of [
    { INPUT_COMPATIBILITY: '' },
    { 'INPUT_TEAM-URL': 'http://cache.example' },
    { 'INPUT_TTL-MINUTES': '61' },
  ]) {
    await assert.rejects(
      setupTurbo(
        { ...env, ...override },
        {
          fetcher: () => assert.fail('request'),
          write: () => assert.fail('write'),
        },
      ),
    );
  }
});
test('cache modes export Turbo policy only after authentication succeeds', async () => {
  for (const [mode, policy] of [
    ['read-only', 'local:rw,remote:r'],
    ['read-write', 'local:rw,remote:rw'],
    ['fresh', 'local:,remote:'],
  ]) {
    const writes: Array<[string, string, string]> = [];
    let requests = 0;
    await setupTurbo({ ...env, 'INPUT_CACHE-MODE': mode }, {
      fetcher: async () => {
        assert.equal(writes.length, 0, 'policy must wait for authentication');
        requests++;
        return Response.json(
          requests === 1
            ? { value: 'identity-fixture' }
            : {
                teamToken: 'token-fixture',
                expiresAt: new Date(Date.now() + 1800_000).toISOString(),
              },
        );
      },
      log: () => {},
      write: (...args) => writes.push(args),
    });
    assert.equal(requests, 2);
    assert.deepEqual(
      writes.filter(([path, key]) => path === 'env' && key === 'TURBO_CACHE'),
      [['env', 'TURBO_CACHE', policy]],
    );
  }
});
test('invalid cache modes fail before authentication or exports', async () => {
  for (const mode of [
    'Read-only',
    'READ-WRITE',
    'read_only',
    'read-write,fresh',
    'local:rw,remote:r',
    ' read-only',
    'fresh\n',
    'constructor',
  ]) {
    await assert.rejects(
      setupTurbo({ ...env, 'INPUT_CACHE-MODE': mode }, {
        fetcher: () => assert.fail('request'),
        write: () => assert.fail('write'),
      }),
      /Invalid cache mode/,
    );
  }
});
test('authentication failure exports no credential', async () => {
  for (const mode of ['', 'read-only', 'read-write', 'fresh']) {
    for (const deniedRequest of [1, 2]) {
      let requests = 0;
      await assert.rejects(
        setupTurbo({ ...env, 'INPUT_CACHE-MODE': mode }, {
          fetcher: async () => {
            requests++;
            return requests === deniedRequest
              ? new Response(null, { status: 403 })
              : Response.json({ value: 'identity-fixture' });
          },
          log: () => {},
          write: () => assert.fail('write'),
        }),
      );
    }
  }
});
