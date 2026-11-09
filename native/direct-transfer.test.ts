import { beforeEach, test } from 'node:test';
import assert from 'node:assert/strict';
import { createServer, type IncomingHttpHeaders, type ServerResponse } from 'node:http';
import { createHash, randomBytes } from 'node:crypto';
import { chmod, mkdir, mkdtemp, readdir, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { inspect } from 'node:util';
import { NativeCache, NativeCacheConflictError, NativeCachePublicationError, artifactKey, type Options } from './client.ts';
import { TRANSFER_MEDIA_TYPE, directBreaker } from './direct-transfer.ts';

const identity = { project: 'github.com/example/app', compatibility: 'xcode26-iossim-arm64-debug', key: 'sources-123' };
const secret = 'presigned-secret-signature';
const query = `X-Amz-Signature=${secret}`;

interface Seen { method: string; path: string; rawPath: string; teamId: string | null; headers: IncomingHttpHeaders; body: Buffer }
type Respond = (response: ServerResponse, request: Seen) => void;
interface Scenario {
  ignoreOptIn?: boolean;
  missing?: boolean;
  descriptor?: (base: Record<string, unknown>) => unknown;
  storageGet?: Respond;
  proxiedGet?: Respond;
  complete?: Respond;
  admission?: Respond;
  grant?: (base: Record<string, unknown>) => unknown;
  storagePut?: Respond;
  commit?: Respond;
  proxiedPut?: Respond;
}

beforeEach(() => directBreaker.reset());

async function setup(t: { after: (fn: () => Promise<void>) => void }) {
  const root = await mkdtemp(join(tmpdir(), 'layercache-direct-test-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const app = join(root, 'Client.app');
  await mkdir(app);
  await writeFile(join(app, 'binary'), 'native code');
  await chmod(join(app, 'binary'), 0o755);
  const saved = await new NativeCache({ cacheDir: join(root, 'producer') }).save(identity, app);
  const archive = await readFile(saved.path!);
  const digest = createHash('sha256').update(archive).digest('hex');
  const seen: Seen[] = [];
  const scenario: Scenario = {};
  const base = (request: Seen) => request.path.slice(0, request.path.indexOf('/transfers/'));
  const server = createServer(async (incoming, response) => {
    const chunks: Buffer[] = [];
    for await (const chunk of incoming) chunks.push(chunk);
    const url = new URL(incoming.url!, 'http://gateway');
    const request: Seen = { method: incoming.method!, path: url.pathname, rawPath: incoming.url!, teamId: url.searchParams.get('teamId'), headers: incoming.headers, body: Buffer.concat(chunks) };
    seen.push(request);
    if (request.path === '/storage/object') {
      if (scenario.storageGet) return scenario.storageGet(response, request);
      response.writeHead(200, { 'Content-Length': archive.length }).end(archive); return;
    }
    if (request.path === '/storage/upload') {
      if (scenario.storagePut) return scenario.storagePut(response, request);
      response.writeHead(200).end(); return;
    }
    if (request.path.endsWith('/complete')) {
      if (scenario.complete) return scenario.complete(response, request);
      response.writeHead(204).end(); return;
    }
    if (request.path.endsWith('/transfers/upload')) {
      if (scenario.admission) return scenario.admission(response, request);
      const declared = JSON.parse(request.body.toString('utf8'));
      const grant = { url: `${endpoint}/storage/upload?${query}`, headers: { 'Content-Length': String(declared.size), 'x-amz-checksum-sha256': 'c2lnbmVkLWNoZWNrc3Vt' }, upload: 'upload-1', expiresAt: new Date(Date.now() + 60_000).toISOString() };
      const answer = scenario.grant ? scenario.grant(grant) : grant;
      response.writeHead(200, { 'Content-Type': 'application/json' }).end(typeof answer === 'string' ? answer : JSON.stringify(answer)); return;
    }
    if (request.path.endsWith('/commit')) {
      if (scenario.commit) return scenario.commit(response, request);
      response.writeHead(200, { 'Content-Type': 'application/json' }).end('{"stored":true}'); return;
    }
    if (request.method === 'PUT') {
      if (scenario.proxiedPut) return scenario.proxiedPut(response, request);
      response.writeHead(200).end(); return;
    }
    if (scenario.missing) { response.writeHead(404).end(); return; }
    if (request.headers.accept === TRANSFER_MEDIA_TYPE && !scenario.ignoreOptIn) {
      const descriptor = { transfer: 'direct-v1', method: 'GET', url: `${endpoint}/storage/object?${query}`, digest: `sha256:${digest}`, size: archive.length, expiresAt: new Date(Date.now() + 60_000).toISOString(), lease: 'lease-1' };
      const answer = scenario.descriptor ? scenario.descriptor(descriptor) : descriptor;
      response.writeHead(200, { 'Content-Type': `${TRANSFER_MEDIA_TYPE}; charset=utf-8` }).end(typeof answer === 'string' ? answer : JSON.stringify(answer)); return;
    }
    if (scenario.proxiedGet) return scenario.proxiedGet(response, request);
    response.writeHead(200, { 'Content-Length': archive.length, 'x-layercache-digest': `sha256:${digest}` }).end(archive);
  });
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(async () => { server.closeAllConnections(); await new Promise<void>(resolve => server.close(() => resolve())); });
  const address = server.address();
  assert.ok(address && typeof address !== 'string');
  const endpoint = `http://127.0.0.1:${address.port}`;
  let caches = 0;
  const cache = (options: Options = {}) => new NativeCache({ endpoint, token: 'test-token', cacheDir: join(root, `cache-${caches++}`), ...options });
  const only = (method: string, match: (path: string) => boolean) => () => seen.filter(request => request.method === method && match(request.path));
  return {
    root, app, archive, digest, seen, scenario, endpoint, cache, base,
    artifactGets: only('GET', path => path.startsWith('/v8/artifacts/')),
    storageGets: only('GET', path => path === '/storage/object'),
    completions: only('POST', path => path.endsWith('/complete')),
    admissions: only('POST', path => path.endsWith('/transfers/upload')),
    storagePuts: only('PUT', path => path === '/storage/upload'),
    commits: only('POST', path => path.endsWith('/commit')),
    proxiedPuts: only('PUT', path => path.startsWith('/v8/artifacts/')),
  };
}
type Fixture = Awaited<ReturnType<typeof setup>>;

const optedIn = (request: Seen) => request.headers.accept === TRANSFER_MEDIA_TYPE;
const authorized = (request: Seen) => {
  assert.equal(request.headers.authorization, 'Bearer test-token');
  assert.equal(request.headers['x-layercache-compatibility'], identity.compatibility);
  assert.equal(request.teamId, identity.project);
};
const installedArchives = async (cache: NativeCache) => (await readdir(cache.root)).filter(name => name.endsWith('.tgz'));

test('direct read restores from storage with no credentials and completes exactly once', async t => {
  const f = await setup(t);
  const cache = f.cache();
  const restored = await cache.restore(identity, join(f.root, 'restored'));
  assert.equal(restored.hit, true);
  assert.equal(restored.source, 'team');
  assert.equal(restored.digest, f.digest);
  assert.equal(await readFile(join(restored.path!, 'Client.app/binary'), 'utf8'), 'native code');
  const [get] = f.artifactGets();
  assert.equal(f.artifactGets().length, 1);
  assert.ok(optedIn(get!));
  authorized(get!);
  assert.equal(f.storageGets().length, 1);
  const [object] = f.storageGets();
  assert.equal(object!.headers.authorization, undefined);
  assert.equal(object!.headers['x-layercache-compatibility'], undefined);
  assert.equal(object!.rawPath, `/storage/object?${query}`);
  assert.equal(f.completions().length, 1);
  const [completion] = f.completions();
  authorized(completion!);
  assert.equal(completion!.path, `/v8/artifacts/${artifactKey(identity)}/transfers/lease-1/complete`);
  assert.deepEqual(JSON.parse(completion!.body.toString('utf8')), { digest: `sha256:${f.digest}`, size: f.archive.length });
  await new NativeCache({ cacheDir: cache.root }).restore(identity);
  assert.equal(f.completions().length, 1, 'a local hit does not complete again');
});

test('a lease is escaped into one path segment', async t => {
  const f = await setup(t);
  f.scenario.descriptor = base => ({ ...base, lease: 'a/b' });
  await f.cache().restore(identity);
  assert.equal(f.completions()[0]!.path, `/v8/artifacts/${artifactKey(identity)}/transfers/a%2Fb/complete`);
});

const invalid = (name: string, change: (base: Record<string, unknown>) => unknown): [string, (f: Fixture) => void] =>
  [`invalid descriptor: ${name}`, f => { f.scenario.descriptor = change; }];

const readFailures: Array<[string, (f: Fixture) => void, Options?]> = [
  ['storage answers 403', f => { f.scenario.storageGet = response => { response.writeHead(403).end(); }; }],
  ['storage answers 206', f => { f.scenario.storageGet = response => { response.writeHead(206, { 'Content-Length': f.archive.length }).end(f.archive); }; }],
  ['connection drops before a response', f => { f.scenario.storageGet = response => { response.destroy(); }; }],
  ['truncated mid-body under a declared length', f => { f.scenario.storageGet = response => {
    response.writeHead(200, { 'Content-Length': f.archive.length });
    response.write(f.archive.subarray(0, 10));
    setImmediate(() => response.destroy());
  }; }],
  ['truncated by a clean short body', f => { f.scenario.storageGet = response => { response.writeHead(200); response.end(f.archive.subarray(0, 10)); }; }],
  ['wrong digest at the right size', f => { f.scenario.storageGet = response => { response.writeHead(200, { 'Content-Length': f.archive.length }).end(Buffer.alloc(f.archive.length)); }; }],
  ['oversize without a declared length', f => { f.scenario.storageGet = response => { response.writeHead(200); response.end(Buffer.concat([f.archive, Buffer.alloc(1)])); }; }],
  ['oversize under a declared length', f => { f.scenario.storageGet = response => { response.writeHead(200, { 'Content-Length': f.archive.length + 1 }).end(Buffer.alloc(f.archive.length + 1)); }; }],
  ['redirect refused', f => { f.scenario.storageGet = response => { response.writeHead(302, { Location: `${f.endpoint}/storage/followed` }).end(); }; }],
  ['the attempt times out', f => { f.scenario.storageGet = () => {}; }, { timeoutMs: 250 }],
  invalid('malformed JSON', () => '{nope'),
  invalid('over 64 KiB', base => JSON.stringify({ ...base, padding: 'x'.repeat(65 << 10) })),
  invalid('wrong transfer', base => ({ ...base, transfer: 'direct-v2' })),
  invalid('wrong method', base => ({ ...base, method: 'POST' })),
  invalid('uppercase digest', base => ({ ...base, digest: `sha256:${'A'.repeat(64)}` })),
  invalid('bare digest', base => ({ ...base, digest: 'a'.repeat(64) })),
  invalid('empty lease', base => ({ ...base, lease: '' })),
  invalid('lease over 128 chars', base => ({ ...base, lease: 'a'.repeat(129) })),
  invalid('dot segment lease', base => ({ ...base, lease: '..' })),
  invalid('expired', base => ({ ...base, expiresAt: new Date(Date.now() - 1).toISOString() })),
  invalid('expiry is not a timestamp', base => ({ ...base, expiresAt: 'tomorrow' })),
  invalid('zero size', base => ({ ...base, size: 0 })),
  invalid('fractional size', base => ({ ...base, size: 1.5 })),
  invalid('size as a string', base => ({ ...base, size: '123' })),
  invalid('size above the local budget', base => ({ ...base, size: 5 * 1024 ** 3 + 1 })),
  invalid('plain http off loopback', base => ({ ...base, url: `http://example.com/object?${query}` })),
  invalid('userinfo in the URL', base => ({ ...base, url: `https://user:pass@example.com/object?${query}` })),
  invalid('fragment in the URL', base => ({ ...base, url: `https://example.com/object?${query}#fragment` })),
  invalid('not a URL', base => ({ ...base, url: 'not a url' })),
];

for (const [name, arrange, options] of readFailures) {
  test(`direct read falls back once and counts toward the breaker: ${name}`, async t => {
    const f = await setup(t);
    arrange(f);
    for (let round = 0; round < 3; round += 1) {
      const before = f.artifactGets().length;
      const cache = f.cache(options);
      const restored = await cache.restore(identity, join(f.root, `restored-${round}`));
      assert.equal(restored.source, 'team');
      assert.equal(restored.digest, f.digest);
      assert.equal(await readFile(join(restored.path!, 'Client.app/binary'), 'utf8'), 'native code');
      assert.deepEqual(f.artifactGets().slice(before).map(optedIn), [true, false]);
      assert.deepEqual((await readdir(cache.root)).filter(entry => entry.endsWith('.partial')), []);
    }
    assert.ok(f.storageGets().length <= 3, 'the presigned URL is never retried');
    assert.equal(f.seen.some(request => request.path === '/storage/followed'), false);
    assert.equal(f.completions().length, 0);
    const before = f.artifactGets().length;
    await f.cache(options).restore(identity);
    assert.deepEqual(f.artifactGets().slice(before).map(optedIn), [false]);
  });
}

test('a repeated proxied miss after a direct failure is a miss', async t => {
  const f = await setup(t);
  f.scenario.storageGet = response => { response.writeHead(403).end(); };
  f.scenario.proxiedGet = response => { response.writeHead(404).end(); };
  assert.equal((await f.cache().restore(identity)).hit, false);
  assert.equal(f.artifactGets().length, 2);
});

test('a 404 to the opted-in read is a miss with no repeat', async t => {
  const f = await setup(t);
  f.scenario.missing = true;
  const result = await f.cache().restore(identity);
  assert.equal(result.hit, false);
  assert.equal(result.source, 'miss');
  assert.equal(f.artifactGets().length, 1);
});

test('an endpoint that ignores the opt-in streams bytes without a repeat or a breaker failure', async t => {
  const f = await setup(t);
  f.scenario.ignoreOptIn = true;
  for (let round = 0; round < 4; round += 1) {
    const before = f.artifactGets().length;
    assert.equal((await f.cache().restore(identity)).source, 'team');
    assert.deepEqual(f.artifactGets().slice(before).map(optedIn), [true]);
  }
  assert.equal(f.storageGets().length, 0);
  assert.equal(f.completions().length, 0);
});

test('a breaker failure streak is cleared by a verified read', async t => {
  const f = await setup(t);
  const failing: Respond = response => { response.writeHead(403).end(); };
  for (const storageGet of [failing, failing, undefined, failing, failing]) {
    f.scenario.storageGet = storageGet;
    await f.cache().restore(identity);
  }
  f.scenario.storageGet = undefined;
  const before = f.artifactGets().length;
  await f.cache().restore(identity);
  assert.deepEqual(f.artifactGets().slice(before).map(optedIn), [true]);
});

test('an open breaker closes again after ten minutes', t => {
  t.mock.timers.enable({ apis: ['Date'], now: 1_000 });
  directBreaker.fail();
  directBreaker.fail();
  assert.equal(directBreaker.allow(), true);
  directBreaker.fail();
  assert.equal(directBreaker.allow(), false);
  t.mock.timers.tick(10 * 60_000 - 1);
  assert.equal(directBreaker.allow(), false);
  t.mock.timers.tick(1);
  assert.equal(directBreaker.allow(), true);
});

test('caller abort during a direct read rethrows with no repeat or breaker failure', async t => {
  const f = await setup(t);
  for (let round = 0; round < 3; round += 1) {
    const before = f.artifactGets().length;
    const controller = new AbortController();
    f.scenario.storageGet = () => controller.abort();
    const cache = f.cache();
    await assert.rejects(cache.restore(identity, undefined, controller.signal), { name: 'AbortError' });
    assert.equal(f.artifactGets().length, before + 1);
    assert.deepEqual(await installedArchives(cache), []);
  }
  f.scenario.storageGet = undefined;
  const before = f.artifactGets().length;
  await f.cache().restore(identity);
  assert.deepEqual(f.artifactGets().slice(before).map(optedIn), [true]);
});

test('caller abort while direct bytes stream rethrows with no repeat or breaker failure', async t => {
  const f = await setup(t);
  for (let round = 0; round < 3; round += 1) {
    const before = f.artifactGets().length;
    const controller = new AbortController();
    f.scenario.storageGet = response => {
      response.writeHead(200, { 'Content-Length': f.archive.length });
      response.write(f.archive.subarray(0, 10), () => setTimeout(() => controller.abort(), 20));
    };
    const cache = f.cache();
    await assert.rejects(cache.restore(identity, undefined, controller.signal), { name: 'AbortError' });
    assert.equal(f.artifactGets().length, before + 1);
    assert.deepEqual((await readdir(cache.root)).filter(entry => entry.endsWith('.partial') || entry.endsWith('.tgz')), []);
  }
  assert.equal(directBreaker.allow(), true);
  assert.equal(f.completions().length, 0);
});

test('a local write failure surfaces without a repeat or a breaker failure', async t => {
  const f = await setup(t);
  const cache = f.cache();
  for (let round = 0; round < 3; round += 1) {
    f.scenario.storageGet = async response => {
      await rename(cache.root, join(f.root, `moved-${round}`));
      response.writeHead(200, { 'Content-Length': f.archive.length }).end(f.archive);
    };
    await assert.rejects(cache.restore(identity), { code: 'ENOENT' });
  }
  assert.deepEqual(f.artifactGets().map(optedIn), [true, true, true]);
  assert.equal(directBreaker.allow(), true);
});

test('a local admission failure surfaces without a repeat or a breaker failure', async t => {
  const f = await setup(t);
  const large = join(f.root, 'large.apk');
  await writeFile(large, randomBytes(200_000));
  const producer = new NativeCache({ cacheDir: join(f.root, 'large-producer') });
  await producer.save(identity, large);
  await writeFile(large, randomBytes(250_000));
  const other = { ...identity, key: 'other' };
  const oversized = await readFile((await producer.save(other, large)).path!);
  const digest = createHash('sha256').update(oversized).digest('hex');
  const cacheDir = join(f.root, 'pinned');
  await mkdir(cacheDir);
  const local = new NativeCache({ cacheDir, maxBytes: 600_000 });
  await writeFile(large, randomBytes(200_000));
  await local.save(identity, large);
  await local.restoreManaged(identity);
  f.scenario.descriptor = base => ({ ...base, digest: `sha256:${digest}`, size: oversized.length });
  const pinned = f.cache({ cacheDir, maxBytes: 600_000 });
  for (let round = 0; round < 3; round += 1) await assert.rejects(pinned.restore(other), /live Expo consumers are pinned/);
  assert.deepEqual(f.artifactGets().map(optedIn), [true, true, true]);
  assert.equal(f.storageGets().length, 0);
  assert.equal(directBreaker.allow(), true);
});

for (const [name, complete] of [
  ['rejected', (response: ServerResponse) => { response.writeHead(404).end(); }],
  ['dropped', (response: ServerResponse) => { response.destroy(); }],
] as Array<[string, Respond]>) {
  test(`completion ${name} does not fail the restore`, async t => {
    const f = await setup(t);
    f.scenario.complete = complete;
    const restored = await f.cache().restore(identity, join(f.root, 'restored'));
    assert.equal(restored.source, 'team');
    assert.equal(await readFile(join(restored.path!, 'Client.app/binary'), 'utf8'), 'native code');
    assert.equal(f.completions().length, 1);
  });
}

test('a stalled completion delays the restore by at most its timeout', async t => {
  const f = await setup(t);
  f.scenario.complete = () => {};
  const started = performance.now();
  const restored = await f.cache().restore(identity);
  assert.equal(restored.source, 'team');
  assert.ok(performance.now() - started < 6_500);
});

test('direct write sends the signed headers unchanged, no credentials, then commits', async t => {
  const f = await setup(t);
  const cache = f.cache();
  const saved = await cache.save(identity, f.app);
  assert.equal(saved.source, 'team');
  const file = await readFile(saved.path!);
  assert.deepEqual(f.seen.map(request => `${request.method} ${request.path.replace(artifactKey(identity), 'key')}`), [
    'POST /v8/artifacts/key/transfers/upload', 'PUT /storage/upload', 'POST /v8/artifacts/key/transfers/upload-1/commit',
  ]);
  const [admission] = f.admissions();
  authorized(admission!);
  assert.deepEqual(JSON.parse(admission!.body.toString('utf8')), { digest: `sha256:${saved.digest}`, size: file.length });
  const [storage] = f.storagePuts();
  assert.equal(storage!.headers['content-length'], String(file.length));
  assert.equal(storage!.headers['transfer-encoding'], undefined);
  assert.equal(storage!.headers['x-amz-checksum-sha256'], 'c2lnbmVkLWNoZWNrc3Vt');
  assert.equal(storage!.headers.authorization, undefined);
  assert.equal(storage!.headers['x-layercache-compatibility'], undefined);
  assert.equal(storage!.rawPath, `/storage/upload?${query}`);
  assert.deepEqual(storage!.body, file);
  authorized(f.commits()[0]!);
});

test('saveImmutable publishes directly and installs only after the commit', async t => {
  const f = await setup(t);
  const cache = f.cache();
  f.scenario.commit = async (response) => {
    assert.deepEqual(await installedArchives(cache), []);
    response.writeHead(201).end();
  };
  const saved = await cache.saveImmutable(identity, f.app);
  assert.equal(saved.source, 'team');
  assert.deepEqual(await installedArchives(cache), [`${artifactKey(identity)}.tgz`]);
  assert.equal(f.proxiedPuts().length, 0);
});

test('an upload refusal goes proxied with no breaker failure', async t => {
  const f = await setup(t);
  f.scenario.admission = response => { response.writeHead(404).end(); };
  for (let round = 0; round < 4; round += 1) await f.cache().save(identity, f.app);
  assert.equal(f.admissions().length, 4);
  assert.equal(f.proxiedPuts().length, 4);
  authorized(f.proxiedPuts()[0]!);
  assert.equal(f.storagePuts().length, 0);
});

test('507 at admission fails with no proxied repeat and no breaker failure', async t => {
  const f = await setup(t);
  f.scenario.admission = response => { response.writeHead(507).end(); };
  for (let round = 0; round < 4; round += 1) await assert.rejects(f.cache().save(identity, f.app), /HTTP 507/);
  assert.equal(f.admissions().length, 4);
  assert.equal(f.proxiedPuts().length, 0);
});

test('507 at commit fails with no proxied repeat', async t => {
  const f = await setup(t);
  f.scenario.commit = response => { response.writeHead(507).end(); };
  await assert.rejects(f.cache().save(identity, f.app), /HTTP 507/);
  await assert.rejects(f.cache().saveImmutable(identity, f.app), /HTTP 507/);
  assert.equal(f.proxiedPuts().length, 0);
});

test('a commit conflict is the same conflict as a proxied one', async t => {
  const f = await setup(t);
  f.scenario.commit = response => { response.writeHead(409).end(); };
  await assert.rejects(f.cache().save(identity, f.app), /already has different content; use a complete build key/);
  const cache = f.cache();
  await assert.rejects(cache.saveImmutable(identity, f.app), NativeCacheConflictError);
  assert.deepEqual(await installedArchives(cache), []);
  assert.equal(f.proxiedPuts().length, 0);
});

const writeFailures: Array<[string, (f: Fixture) => void]> = [
  ['storage answers 403', f => { f.scenario.storagePut = response => { response.writeHead(403).end(); }; }],
  ['storage connection drops', f => { f.scenario.storagePut = response => { response.destroy(); }; }],
  ['storage redirects', f => { f.scenario.storagePut = response => { response.writeHead(307, { Location: `${f.endpoint}/storage/followed` }).end(); }; }],
  ['commit answers 500', f => { f.scenario.commit = response => { response.writeHead(500).end(); }; }],
  ['commit answers 404', f => { f.scenario.commit = response => { response.writeHead(404).end(); }; }],
  ['commit connection drops', f => { f.scenario.commit = response => { response.destroy(); }; }],
  ['admission answer is not JSON', f => { f.scenario.grant = () => '{nope'; }],
  ['admission has an empty upload', f => { f.scenario.grant = base => ({ ...base, upload: '' }); }],
  ['admission upload is over 128 chars', f => { f.scenario.grant = base => ({ ...base, upload: 'a'.repeat(129) }); }],
  ['admission URL is invalid', f => { f.scenario.grant = base => ({ ...base, url: `http://example.com/upload?${query}` }); }],
  ['admission headers are not strings', f => { f.scenario.grant = base => ({ ...base, headers: { 'x-amz-checksum-sha256': 1 } }); }],
  ['admission signed a different length', f => { f.scenario.grant = base => ({ ...base, headers: { 'Content-Length': '1' } }); }],
];

for (const [name, arrange] of writeFailures) {
  test(`direct write falls back to the proxied PUT and counts toward the breaker: ${name}`, async t => {
    const f = await setup(t);
    arrange(f);
    let file = Buffer.alloc(0);
    for (let round = 0; round < 3; round += 1) {
      const saved = await f.cache().save(identity, f.app);
      file = await readFile(saved.path!);
    }
    assert.equal(f.proxiedPuts().length, 3);
    authorized(f.proxiedPuts()[0]!);
    assert.deepEqual(f.proxiedPuts()[0]!.body, file);
    assert.equal(f.seen.some(request => request.path === '/storage/followed'), false);
    await f.cache().save(identity, f.app);
    assert.equal(f.admissions().length, 3, 'an open breaker skips admission');
    assert.equal(f.proxiedPuts().length, 4);
  });
}

for (const status of [401, 403]) {
  test(`commit ${status} goes proxied with no breaker failure`, async t => {
    const f = await setup(t);
    f.scenario.commit = response => { response.writeHead(status).end(); };
    for (let round = 0; round < 4; round += 1) await f.cache().save(identity, f.app);
    assert.equal(f.admissions().length, 4);
    assert.equal(f.proxiedPuts().length, 4);
  });
}

test('an admission transport error fails like the upload with no proxied repeat', async t => {
  const f = await setup(t);
  f.scenario.admission = response => { response.destroy(); };
  await assert.rejects(f.cache().save(identity, f.app), /fetch failed/);
  assert.equal(f.proxiedPuts().length, 0);
  assert.equal(directBreaker.allow(), true);
});

test('caller abort during a direct write rethrows with no repeat or breaker failure', async t => {
  const f = await setup(t);
  for (let round = 0; round < 3; round += 1) {
    const controller = new AbortController();
    f.scenario.storagePut = () => controller.abort();
    const cache = f.cache();
    await assert.rejects(cache.saveImmutable(identity, f.app, controller.signal), { name: 'AbortError' });
    assert.deepEqual(await installedArchives(cache), []);
  }
  assert.equal(f.proxiedPuts().length, 0);
  assert.equal(directBreaker.allow(), true);
});

for (const [name, headers] of [
  ['degraded', { 'x-layercache-degraded': 'team-upload-failed' }],
  ['queued', { 'x-layercache-upload-queue': 'queued' }],
  ['partial team upload', { 'x-layercache-team-upload': 'pending' }],
] as Array<[string, Record<string, string>]>) {
  test(`saveImmutable installs nothing when the final response is ${name}`, async t => {
    const f = await setup(t);
    f.scenario.commit = response => { response.writeHead(200, headers).end(); };
    const direct = f.cache();
    await assert.rejects(direct.saveImmutable(identity, f.app), NativeCachePublicationError);
    assert.deepEqual(await installedArchives(direct), []);
    f.scenario.commit = response => { response.writeHead(500).end(); };
    f.scenario.proxiedPut = response => { response.writeHead(200, headers).end(); };
    const proxied = f.cache();
    await assert.rejects(proxied.saveImmutable(identity, f.app), NativeCachePublicationError);
    assert.deepEqual(await installedArchives(proxied), []);
  });
}

test('failing direct paths never surface the presigned query', async t => {
  const f = await setup(t);
  const logged: string[] = [];
  for (const method of ['log', 'info', 'warn', 'error', 'debug'] as const) {
    t.mock.method(console, method, (...values: unknown[]) => { logged.push(inspect(values, { depth: null })); });
  }
  const assertHidden = (error: unknown) => assert.ok(!inspect(error, { depth: null, showHidden: true }).includes(secret));
  f.scenario.proxiedGet = response => { response.writeHead(500).end(); };
  f.scenario.proxiedPut = response => { response.writeHead(500).end(); };
  f.scenario.storageGet = response => { response.destroy(); };
  await assert.rejects(f.cache().restore(identity), (error: unknown) => { assertHidden(error); return true; });
  f.scenario.descriptor = base => ({ ...base, url: `http://example.com/object?${query}` });
  await assert.rejects(f.cache().restore(identity), (error: unknown) => { assertHidden(error); return true; });
  f.scenario.descriptor = undefined;
  f.scenario.storageGet = response => { response.writeHead(302, { Location: `${f.endpoint}/storage/followed?${query}` }).end(); };
  await assert.rejects(f.cache().restore(identity), (error: unknown) => { assertHidden(error); return true; });
  directBreaker.reset();
  const controller = new AbortController();
  f.scenario.storageGet = () => controller.abort();
  await assert.rejects(f.cache().restore(identity, undefined, controller.signal), (error: unknown) => { assertHidden(error); return true; });
  f.scenario.storagePut = response => { response.destroy(); };
  await assert.rejects(f.cache().save(identity, f.app), (error: unknown) => { assertHidden(error); return true; });
  directBreaker.reset();
  f.scenario.storagePut = response => { response.writeHead(403).end(); };
  await assert.rejects(f.cache().saveImmutable(identity, f.app), (error: unknown) => { assertHidden(error); return true; });
  assert.ok(!logged.join('\n').includes(secret));
});
