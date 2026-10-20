import assert from 'node:assert/strict';
import test from 'node:test';
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { createServer } from 'node:http';
import { mkdtemp, mkdir, readFile, writeFile, rm, chmod, symlink, link, stat, readdir } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const project = 'github.com/example/snapshot';
const compatibility = 'xcode-fixture-v1';
const key = 'snapshots/closed-generation-1';
const writerBootstrap = `lcs1.writer.${'a'.repeat(64)}`;
const readerBootstrap = `lcs1.reader.${'b'.repeat(64)}`;
const nativeKey = (key: string) => `native-v1-${createHash('sha256').update(JSON.stringify([project, compatibility, key])).digest('hex')}`;

async function cli(args: string[], env: NodeJS.ProcessEnv = {}): Promise<{ code: number | null; stdout: string; stderr: string }> {
  const child = spawn(process.execPath, [resolve('native/cli.ts'), ...args], { env: { ...process.env, ...env }, stdio: ['ignore', 'pipe', 'pipe'] });
  let stdout = '', stderr = '';
  child.stdout.on('data', chunk => { stdout += chunk; });
  child.stderr.on('data', chunk => { stderr += chunk; });
  return new Promise((resolveResult, reject) => {
    const timer = setTimeout(() => { child.kill('SIGKILL'); reject(new Error('Snapshot CLI fixture timed out')); }, 10000);
    child.on('error', error => { clearTimeout(timer); reject(error); });
    child.on('close', code => { clearTimeout(timer); resolveResult({ code, stdout, stderr }); });
  });
}

async function fixture(t: { after: (cleanup: () => Promise<void>) => void }) {
  const root = await mkdtemp(join(tmpdir(), 'layercache-snapshot-auth-'));
  const writer = join(root, 'writer-credential'), reader = join(root, 'reader-credential');
  await writeFile(writer, writerBootstrap + '\n', { mode: 0o600 });
  await writeFile(reader, readerBootstrap + '\n', { mode: 0o600 });
  const calls: Array<{ method: string; path: string; authorization: string; body: string }> = [];
  const objects = new Map<string, Buffer>();
  const tokens = new Map<string, { mode: string; nativeKey: string }>();
  const state = { response: 'normal', revoked: false, artifactHeaders: {} as Record<string, string>, artifactDelayMs: 0 };
  const server = createServer(async (request, response) => {
    const url = new URL(request.url!, 'http://fixture');
    const chunks: Buffer[] = [];
    for await (const chunk of request) chunks.push(Buffer.from(chunk));
    const body = Buffer.concat(chunks);
    calls.push({ method: request.method!, path: url.pathname, authorization: request.headers.authorization ?? '', body: url.pathname.endsWith('/exchange') ? body.toString() : '' });
    if (url.pathname === '/v1/auth/snapshot/exchange') {
      if (state.response === 'unavailable') { response.writeHead(503); response.end('private-service-response'); return; }
      if (state.response === 'redirect') { response.writeHead(302, { Location: '/stolen-bootstrap' }); response.end(); return; }
      const input = JSON.parse(body.toString());
      const credential = request.headers.authorization;
      if (state.revoked || input.project !== project || input.compatibility !== compatibility || !input.key.startsWith('snapshots/') ||
          ![`Bearer ${writerBootstrap}`, `Bearer ${readerBootstrap}`].includes(credential ?? '') ||
          input.mode === 'write' && credential !== `Bearer ${writerBootstrap}`) {
        response.writeHead(401); response.end('private-denial-response'); return;
      }
      const token = `lc2.fixture.token-${tokens.size}`;
      tokens.set(token, { mode: input.mode, nativeKey: nativeKey(input.key) });
      const output = { version: 1, token, expiresAt: new Date(Date.now() + 900000).toISOString(), project, compatibility,
        nativeKey: nativeKey(input.key), mode: input.mode, capabilities: [input.mode], credentialKind: 'shared-snapshot-bootstrap' };
      if (state.response === 'expired') output.expiresAt = new Date(Date.now() - 1000).toISOString();
      if (state.response === 'long-lived') output.expiresAt = new Date(Date.now() + 3600000).toISOString();
      if (state.response === 'invalid-date') output.expiresAt = 'not-a-date';
      if (state.response === 'foreign') output.nativeKey = nativeKey('snapshots/foreign-generation');
      if (state.response === 'broad-role') output.capabilities = ['admin'];
      if (state.response === 'wrong-mode') output.mode = input.mode === 'read' ? 'write' : 'read';
      if (state.response === 'invalid-token') output.token = readerBootstrap;
      if (state.response === 'oversized') { response.end(JSON.stringify({ ...output, unused: 'x'.repeat(20000) })); return; }
      response.setHeader('Content-Type', 'application/json'); response.end(JSON.stringify(output)); return;
    }
    const token = tokens.get((request.headers.authorization ?? '').replace(/^Bearer /, ''));
    const hash = url.pathname.slice('/v8/artifacts/'.length);
    if (!token || token.nativeKey !== hash || request.headers['x-layercache-compatibility'] !== compatibility || url.searchParams.get('teamId') !== project) {
      response.writeHead(401); response.end(); return;
    }
    if (request.method === 'PUT') {
      if (token.mode !== 'write') { response.writeHead(401); response.end(); return; }
      const prior = objects.get(hash);
      if (prior && !prior.equals(body)) { response.writeHead(409); response.end(); return; }
      objects.set(hash, body); response.writeHead(200, state.artifactHeaders); response.end(); return;
    }
    const stored = objects.get(hash);
    if (!stored) { response.writeHead(404); response.end(); return; }
    if (state.artifactDelayMs) await delay(state.artifactDelayMs);
    if (response.destroyed) return;
    response.writeHead(200, { 'Content-Length': String(stored.length), 'x-layercache-digest': `sha256:${createHash('sha256').update(stored).digest('hex')}` });
    response.end(stored);
  });
  await new Promise<void>(resolveReady => server.listen(0, '127.0.0.1', resolveReady));
  const address = server.address(); assert.ok(address && typeof address !== 'string');
  const endpoint = `http://127.0.0.1:${address.port}`;
  t.after(async () => { server.closeAllConnections(); await new Promise<void>(done => server.close(() => done())); await rm(root, { recursive: true, force: true }); });
  const args = (operation: string, credential = reader, generation = key) => [operation, '--endpoint', endpoint, '--credential-file', credential,
    '--project', project, '--compatibility', compatibility, '--key', generation, '--max-bytes', '1048576'];
  return { root, endpoint, server, writer, reader, calls, objects, state, args };
}

test('scoped CLI publishes a cold archive and a fresh read-only consumer restores without PUT', async t => {
  const f = await fixture(t);
  const input = join(f.root, 'ClosedCache'); await mkdir(input); await writeFile(join(input, 'metadata.db'), 'closed fixture database');
  const saved = await cli([...f.args('save', f.writer), '--path', input, '--cache-dir', join(f.root, 'writer-cache')], {
    LAYER_CACHE_URL: 'https://ambient.invalid', LAYER_CACHE_TOKEN: 'ambient-token-canary',
  });
  assert.equal(saved.code, 0, saved.stderr);
  assert.equal(JSON.parse(saved.stdout).source, 'team');
  assert.equal(f.objects.size, 1);
  const readStart = f.calls.length;
  const destination = join(f.root, 'fresh-worktree');
  const restored = await cli([...f.args('restore'), '--path', destination, '--cache-dir', join(f.root, 'reader-cache')]);
  assert.equal(restored.code, 0, restored.stderr);
  assert.equal(JSON.parse(restored.stdout).hit, true);
  assert.equal(JSON.parse(restored.stdout).source, 'team');
  assert.equal(await readFile(join(destination, 'ClosedCache', 'metadata.db'), 'utf8'), 'closed fixture database');
  assert.deepEqual(f.calls.slice(readStart).map(call => call.method), ['POST', 'GET']);
  assert.equal(f.calls[0]?.authorization, `Bearer ${writerBootstrap}`);
  assert.equal(f.calls[1]?.authorization.startsWith('Bearer lc2.'), true);
  for (const value of [writerBootstrap, readerBootstrap, 'lc2.fixture.token-0', 'lc2.fixture.token-1', 'ambient-token-canary']) {
    assert.ok(!(saved.stdout + saved.stderr + restored.stdout + restored.stderr).includes(value));
  }
});

test('read-only grants cannot publish and every local reuse reauthenticates before extraction', async t => {
  const f = await fixture(t);
  const input = join(f.root, 'ClosedCache'); await mkdir(input); await writeFile(join(input, 'metadata.db'), 'authorized archive');
  const cacheDir = join(f.root, 'shared-archive-cache');
  const denied = await cli([...f.args('save'), '--path', input, '--cache-dir', cacheDir]);
  assert.notEqual(denied.code, 0); assert.equal(f.calls.length, 1); assert.equal(f.objects.size, 0);
  const saved = await cli([...f.args('save', f.writer), '--path', input, '--cache-dir', cacheDir]);
  assert.equal(saved.code, 0, saved.stderr);
  const first = await cli([...f.args('restore'), '--path', join(f.root, 'local-reader'), '--cache-dir', cacheDir]);
  assert.equal(first.code, 0, first.stderr); assert.equal(JSON.parse(first.stdout).source, 'local');
  const start = f.calls.length; f.state.revoked = true;
  const destination = join(f.root, 'revoked-reader');
  const revoked = await cli([...f.args('restore'), '--path', destination, '--cache-dir', cacheDir]);
  assert.notEqual(revoked.code, 0); assert.deepEqual(f.calls.slice(start).map(call => call.method), ['POST']);
  await assert.rejects(stat(destination), { code: 'ENOENT' });
  assert.match(revoked.stderr, /HTTP 401/);
  for (const result of [denied, saved, first, revoked]) assert.ok(!(result.stdout + result.stderr).includes(readerBootstrap));
});

test('scoped misses are explicit and project, profile and prefix denials never reach artifacts', async t => {
  const f = await fixture(t);
  const miss = await cli([...f.args('restore'), '--path', join(f.root, 'missing-output'), '--cache-dir', join(f.root, 'cache')]);
  assert.equal(miss.code, 0, miss.stderr); assert.equal(JSON.parse(miss.stdout).hit, false); assert.equal(JSON.parse(miss.stdout).source, 'miss');
  await assert.rejects(stat(join(f.root, 'missing-output')), { code: 'ENOENT' });
  for (const [flag, value] of [['--project', 'github.com/example/foreign'], ['--compatibility', 'foreign-v1'], ['--key', 'foreign/generation']]) {
    const args = f.args('restore'); args[args.indexOf(flag!) + 1] = value!;
    const start = f.calls.length;
    const denied = await cli([...args, '--path', join(f.root, 'foreign-output'), '--cache-dir', join(f.root, 'cache')]);
    assert.notEqual(denied.code, 0); assert.deepEqual(f.calls.slice(start).map(call => call.method), ['POST']);
  }
});

test('invalid, expired, broad and unavailable exchange responses cannot return a local hit', async t => {
  const f = await fixture(t);
  const input = join(f.root, 'ClosedCache'); await mkdir(input); await writeFile(join(input, 'metadata.db'), 'private fixture');
  const cacheDir = join(f.root, 'cache');
  assert.equal((await cli([...f.args('save', f.writer), '--path', input, '--cache-dir', cacheDir])).code, 0);
  for (const response of ['expired', 'long-lived', 'invalid-date', 'foreign', 'broad-role', 'wrong-mode', 'invalid-token', 'oversized', 'unavailable', 'redirect']) {
    f.state.response = response; const start = f.calls.length;
    const result = await cli([...f.args('restore'), '--path', join(f.root, `destination-${response}`), '--cache-dir', cacheDir]);
    assert.notEqual(result.code, 0, response); assert.deepEqual(f.calls.slice(start).map(call => call.method), ['POST']);
    assert.ok(!(result.stdout + result.stderr).includes('private-service-response'));
    assert.ok(!(result.stdout + result.stderr).includes('lc2.fixture.'));
    assert.ok(!(result.stdout + result.stderr).includes(readerBootstrap));
    await assert.rejects(stat(join(f.root, `destination-${response}`)), { code: 'ENOENT' });
  }
});

test('credential files reject unsafe permissions, symlinks, extra links and oversized or malformed bytes before HTTP', async t => {
  const f = await fixture(t);
  const files = [join(f.root, 'public-token'), join(f.root, 'linked-token'), join(f.root, 'hardlinked-token'), join(f.root, 'oversized-token'), join(f.root, 'malformed-token')];
  await writeFile(files[0]!, readerBootstrap, { mode: 0o600 }); await chmod(files[0]!, 0o644);
  await symlink(f.reader, files[1]!);
  await writeFile(files[2]!, readerBootstrap, { mode: 0o600 }); await link(files[2]!, files[2]! + '-second');
  await writeFile(files[3]!, 'x'.repeat(17000), { mode: 0o600 });
  await writeFile(files[4]!, readerBootstrap + '\nsecond-secret-token', { mode: 0o600 });
  for (const file of [...files, f.root, join(f.root, 'missing-credential')]) {
    const result = await cli([...f.args('restore', file), '--path', join(f.root, 'output'), '--cache-dir', join(f.root, 'cache')]);
    assert.notEqual(result.code, 0); assert.match(result.stderr, /credential file/);
    assert.ok(!(result.stdout + result.stderr).includes(readerBootstrap));
    assert.ok(!(result.stdout + result.stderr).includes(file));
  }
  assert.equal(f.calls.length, 0);
  await assert.rejects(stat(join(f.root, 'cache')), { code: 'ENOENT' });
});

test('scoped mode needs its explicit endpoint and a new destination, without falling back to ambient credentials', async t => {
  const f = await fixture(t);
  for (const missing of ['--endpoint', '--credential-file', '--path']) {
    const args = [...f.args('restore'), '--path', join(f.root, 'output')]; args.splice(args.indexOf(missing), 2);
    const result = await cli(args, { LAYER_CACHE_URL: f.endpoint, LAYER_CACHE_TOKEN: 'ambient-canary' });
    assert.notEqual(result.code, 0); assert.match(result.stderr, /requires --credential-file, --endpoint and --path/);
  }
  assert.equal(f.calls.length, 0);
  const input = join(f.root, 'ClosedCache'); await mkdir(input); await writeFile(join(input, 'data'), 'bytes');
  assert.equal((await cli([...f.args('save', f.writer), '--path', input, '--cache-dir', join(f.root, 'producer-cache')])).code, 0);
  const existing = join(f.root, 'existing'); await mkdir(existing); await writeFile(join(existing, 'source'), 'preserved');
  const result = await cli([...f.args('restore'), '--path', existing, '--cache-dir', join(f.root, 'reader-cache')]);
  assert.notEqual(result.code, 0); assert.equal(await readFile(join(existing, 'source'), 'utf8'), 'preserved');
});

test('immutable scoped saves reject degraded or queued acknowledgment and preserve the canonical winner on conflict', async t => {
  const f = await fixture(t);
  const input = join(f.root, 'ClosedCache'); await mkdir(input); await writeFile(join(input, 'data'), 'original immutable bytes');
  const cacheDir = join(f.root, 'cache');
  const degradedHeaders: Array<Record<string, string>> = [{ 'x-layercache-degraded': 'upload-failed' }, { 'x-layercache-upload-queue': 'pending' }, { 'x-layercache-team-upload': 'queued' }];
  for (const headers of degradedHeaders) {
    f.state.artifactHeaders = headers;
    const generation = 'snapshots/degraded-' + Object.keys(headers)[0];
    const result = await cli([...f.args('save', f.writer, generation), '--path', input, '--cache-dir', cacheDir]);
    assert.notEqual(result.code, 0); assert.match(result.stderr, /publication was not confirmed/);
    const directories = await readdir(join(cacheDir, 'snapshot-v1'));
    const entries = await readdir(join(cacheDir, 'snapshot-v1', directories[0]!));
    assert.ok(!entries.includes(nativeKey(generation) + '.tgz'));
  }
  f.state.artifactHeaders = {};
  const saved = await cli([...f.args('save', f.writer), '--path', input, '--cache-dir', cacheDir]);
  assert.equal(saved.code, 0, saved.stderr);
  const canonical = JSON.parse(saved.stdout).path as string; const before = await readFile(canonical);
  await writeFile(join(input, 'data'), 'different bytes');
  const conflict = await cli([...f.args('save', f.writer), '--path', input, '--cache-dir', cacheDir]);
  assert.notEqual(conflict.code, 0); assert.match(conflict.stderr, /different immutable content/);
  assert.deepEqual(await readFile(canonical), before);
});

test('one configured archive root isolates bytes across authenticated endpoints', async t => {
  const first = await fixture(t), second = await fixture(t);
  const input = join(first.root, 'ClosedCache'); await mkdir(input); await writeFile(join(input, 'data'), 'origin-one-only');
  const cacheDir = join(first.root, 'shared-cache');
  assert.equal((await cli([...first.args('save', first.writer), '--path', input, '--cache-dir', cacheDir])).code, 0);
  const result = await cli([...second.args('restore'), '--path', join(first.root, 'other-origin'), '--cache-dir', cacheDir]);
  assert.equal(result.code, 0, result.stderr); assert.equal(JSON.parse(result.stdout).hit, false);
  assert.deepEqual(second.calls.map(call => call.method), ['POST', 'GET']);
});

test('archive timeout is caller-bounded from 1 to 900 seconds without retrying transfers', async t => {
  const f = await fixture(t);
  for (const value of ['0', '-1', '901', '1.5', 'Infinity', '1e2', '01']) {
    const invalid = await cli([...f.args('restore'), '--path', join(f.root, 'invalid-output'), '--timeout-seconds', value]);
    assert.notEqual(invalid.code, 0); assert.match(invalid.stderr, /integer from 1 to 900/);
  }
  assert.equal(f.calls.length, 0);
  const allowed = await cli([...f.args('restore'), '--path', join(f.root, 'max-timeout-output'), '--cache-dir', join(f.root, 'max-timeout-cache'), '--timeout-seconds', '900']);
  assert.equal(allowed.code, 0, allowed.stderr); assert.equal(JSON.parse(allowed.stdout).hit, false);
  const input = join(f.root, 'ClosedCache'); await mkdir(input); await writeFile(join(input, 'data'), 'bounded download');
  assert.equal((await cli([...f.args('save', f.writer), '--path', input, '--cache-dir', join(f.root, 'producer')])).code, 0);
  f.state.artifactDelayMs = 1500; const start = f.calls.length;
  const result = await cli([...f.args('restore'), '--path', join(f.root, 'timed-out-output'), '--cache-dir', join(f.root, 'consumer'), '--timeout-seconds', '1']);
  assert.notEqual(result.code, 0); assert.deepEqual(f.calls.slice(start).map(call => call.method), ['POST', 'GET']);
  await assert.rejects(stat(join(f.root, 'timed-out-output')), { code: 'ENOENT' });
});

test('the existing credential-free local CLI mode keeps its archive roundtrip', async t => {
  const f = await fixture(t);
  const input = join(f.root, 'LegacyCache'); await mkdir(input); await writeFile(join(input, 'data'), 'legacy local bytes');
  const args = ['--project', project, '--compatibility', compatibility, '--key', 'legacy-key', '--cache-dir', join(f.root, 'legacy-cache')];
  const env = { LAYER_CACHE_URL: '', LAYER_CACHE_TOKEN: '', TURBO_API: '', TURBO_TOKEN: '' };
  const saved = await cli(['save', ...args, '--path', input], env);
  assert.equal(saved.code, 0, saved.stderr); assert.equal(JSON.parse(saved.stdout).source, 'local');
  const destination = join(f.root, 'legacy-destination');
  const restored = await cli(['restore', ...args, '--path', destination], env);
  assert.equal(restored.code, 0, restored.stderr); assert.equal(JSON.parse(restored.stdout).source, 'local');
  assert.equal(await readFile(join(destination, 'LegacyCache', 'data'), 'utf8'), 'legacy local bytes'); assert.equal(f.calls.length, 0);
});
