import test, { after } from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { createReadStream, createWriteStream, existsSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { Transform } from 'node:stream';
import { pipeline } from 'node:stream/promises';
import { declaration, downloadFile, expectedReceipt, publicationID, receipt } from './client.mjs';
import { runRetained } from './main.mjs';

const repository = mkdtempSync(join(tmpdir(), 'layercache-retained-source-'));
execFileSync('git', ['init', '--quiet', '--initial-branch=main', repository]);
execFileSync('git', ['-C', repository, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', '-c', 'core.hooksPath=/dev/null', 'commit', '--quiet', '--allow-empty', '-m', 'fixture source']);
const sourceCommit = execFileSync('git', ['-C', repository, 'rev-parse', 'HEAD'], { encoding: 'utf8' }).trim();
after(() => rmSync(repository, { recursive: true, force: true }));
const baseProducer = { project: 'private-widget', repository: 'acme/widget', repositoryId: '100', sourceCommit,
  workflowRef: 'acme/widget/.github/workflows/check.yml@refs/heads/main', runId: '200', runAttempt: '1', jobId: '300', profile: 'android-debug-x86_64-toolchain-v1' };
const hash = data => createHash('sha256').update(data).digest('hex');
const jwt = payload => `${Buffer.from('{}').toString('base64url')}.${Buffer.from(JSON.stringify(payload)).toString('base64url')}.fixture`;
const retention = () => new Date(Date.now() + 3_600_000).toISOString();

function workspace() {
  const root = mkdtempSync(join(tmpdir(), 'layercache-retained-client-'));
  const env = { RUNNER_TEMP: root, GITHUB_OUTPUT: join(root, 'output'), GITHUB_WORKSPACE: repository,
    GITHUB_REPOSITORY: baseProducer.repository, GITHUB_REPOSITORY_ID: baseProducer.repositoryId,
    GITHUB_SHA: sourceCommit, GITHUB_WORKFLOW_REF: baseProducer.workflowRef, GITHUB_RUN_ID: baseProducer.runId,
    GITHUB_RUN_ATTEMPT: baseProducer.runAttempt,
    ACTIONS_ID_TOKEN_REQUEST_URL: 'https://oidc.example/token', ACTIONS_ID_TOKEN_REQUEST_TOKEN: 'fixture-request-secret',
    INPUT_MODE: 'publish', INPUT_PROJECT: baseProducer.project, INPUT_PROFILE: baseProducer.profile, INPUT_NAME: 'qa-apk',
    INPUT_PATH: join(root, 'artifact.apk'), 'INPUT_RETAIN-UNTIL': retention(),
    'INPUT_GITHUB-TOKEN': 'fixture-github-secret', 'INPUT_RECEIPT-OUTPUT': join(root, 'receipt.json'),
    'INPUT_TIMINGS-OUTPUT': join(root, 'timings.json') };
  writeFileSync(env.INPUT_PATH, Buffer.alloc(1024 * 1024, 0xa5));
  return { root, env, cleanup: () => rmSync(root, { recursive: true, force: true }) };
}

function fileValues(path) {
  const values = {};
  const lines = readFileSync(path, 'utf8').split('\n');
  for (let index = 0; index < lines.length - 1; index++) {
    const [key, delimiter] = lines[index].split('<<');
    assert.ok(delimiter);
    const value = [];
    while (lines[++index] !== delimiter) { assert.ok(index < lines.length); value.push(lines[index]); }
    values[key] = value.join('\n');
  }
  return values;
}

// Real HTTP exercises the client and Node streaming implementation. This
// disposable provider models the wire contract; it does not qualify real
// PostgreSQL, object storage or GitHub identity verification.
async function provider(options = {}) {
  const root = mkdtempSync(join(tmpdir(), 'layercache-retained-provider-'));
  const records = new Map();
  const capabilities = new Map();
  const requests = [];
  let jobId = baseProducer.jobId;
  let runAttempt = baseProducer.runAttempt;
  let fault = options.fault;
  let lostAck = options.lostAck;
  let server;
  let endpoint;
  function send(response, status, value) { response.writeHead(status, { 'Content-Type': 'application/json' }); response.end(JSON.stringify(value)); }
  async function handle(request, response) {
    const route = new URL(request.url, 'http://fixture').pathname;
    requests.push({ method: request.method, route });
    if (route === '/token') {
      assert.equal(request.headers.authorization, 'Bearer fixture-request-secret');
      assert.equal(new URL(request.url, 'http://fixture').searchParams.get('audience'), 'https://layercache.dev/oidc/projects/66d8209746b8e59fd6ac2748db407e7dbc93a70c97c3bfa3e2183fe129b91059');
      send(response, 200, { value: jwt({ repository: baseProducer.repository, repository_id: baseProducer.repositoryId,
        sha: sourceCommit, workflow_ref: baseProducer.workflowRef, run_id: baseProducer.runId, run_attempt: runAttempt, check_run_id: jobId }) });
      return;
    }
    if (route === '/v1/auth/github-oidc/exchange') {
      const chunks = []; for await (const chunk of request) chunks.push(chunk);
      const input = JSON.parse(Buffer.concat(chunks));
      assert.equal(input.integration, 'retained'); assert.equal(input.githubToken, 'fixture-github-secret');
      assert.equal(input.compatibility, baseProducer.profile);
      const current = { ...baseProducer, jobId, runAttempt };
      const token = `lc2.${Buffer.from(JSON.stringify({ mode: input.mode, producer: current })).toString('base64url')}.fixture`;
      capabilities.set(token, { current, mode: input.mode });
      send(response, 200, { teamToken: token, expiresAt: new Date(Date.now() + 3_600_000).toISOString(), mode: input.mode,
        producer: options.wrongAuthority ? { ...current, sourceCommit: 'b'.repeat(40) } : current });
      return;
    }
    const token = request.headers.authorization?.replace(/^Bearer /, '');
    const authority = capabilities.get(token);
    if (!authority) { send(response, 401, {}); return; }
    const path = route.replace('/v1/retained-artifacts', '');
    if (request.method === 'POST' && path === '') {
      if (authority.mode !== 'publish') { send(response, 403, {}); return; }
      if (options.admissionStatus) { send(response, options.admissionStatus, {}); return; }
      const chunks = []; for await (const chunk of request) chunks.push(chunk);
      const declared = declaration(JSON.parse(Buffer.concat(chunks)));
      assert.deepEqual(declared.producer, authority.current);
      const id = publicationID(declared);
      if (records.has(id)) {
        if (JSON.stringify(records.get(id).declaration) !== JSON.stringify(declared)) { send(response, 409, {}); return; }
        send(response, 200, records.get(id)); return;
      }
      const record = { schemaVersion: 'retained-artifact-v1', id, state: 'reserved', declaration: declared,
        reservedUntil: new Date(Date.now() + 900_000).toISOString(), createdAt: new Date().toISOString() };
      records.set(id, record); send(response, 201, record); return;
    }
    const [id, content] = path.slice(1).split('/');
    const record = records.get(id);
    if (!record) { send(response, 404, {}); return; }
    const same = Object.entries(record.declaration.producer).every(([key, value]) => ['jobId', 'runAttempt'].includes(key) || value === authority.current[key]);
    if (!same || BigInt(record.declaration.producer.runAttempt) > BigInt(authority.current.runAttempt) ||
        (authority.mode === 'read' && record.declaration.producer.jobId === authority.current.jobId)) { send(response, 403, {}); return; }
    if (request.method === 'GET' && !content) {
      const value = fault === 'foreign-receipt' ? { ...record, declaration: { ...record.declaration, producer: { ...record.declaration.producer, repositoryId: '999' } } } : record;
      send(response, 200, value); return;
    }
    if (request.method === 'PUT' && content === 'content') {
      if (authority.mode !== 'publish' || authority.current.jobId !== record.declaration.producer.jobId) { send(response, 403, {}); return; }
      const digest = createHash('sha256'); let size = 0;
      await pipeline(request, new Transform({ transform(chunk, encoding, callback) { digest.update(chunk); size += chunk.length; callback(null, chunk); } }),
        createWriteStream(join(root, `${id}.body`), { flags: 'w', mode: 0o600 }));
      if (size !== record.declaration.size || digest.digest('hex') !== record.declaration.digest) { send(response, 422, {}); return; }
      record.state = 'published'; record.publishedAt = new Date().toISOString();
      if (lostAck) { lostAck = false; response.destroy(); return; }
      send(response, 200, record); return;
    }
    if (request.method === 'GET' && content === 'content') {
      if (record.state !== 'published') { send(response, 410, {}); return; }
      response.writeHead(200, { 'Content-Type': 'application/octet-stream',
        'X-Layercache-Retained-ID': id, 'X-Layercache-Digest': `sha256:${record.declaration.digest}`,
        'Server-Timing': 'retained_open;dur=0.25',
        ...(fault === 'overlong' ? {} : { 'Content-Length': String(record.declaration.size) }),
        ...(fault === 'encoded' ? { 'Content-Encoding': 'gzip' } : {}) });
      if (fault === 'stalled') { response.flushHeaders(); return; }
      if (fault === 'partial') {
        await pipeline(createReadStream(join(root, `${id}.body`), { end: Math.floor(record.declaration.size / 2) }), response);
        return;
      }
      if (fault === 'corrupt') {
        let first = true;
        await pipeline(createReadStream(join(root, `${id}.body`)), new Transform({ transform(chunk, encoding, callback) {
          if (first) { first = false; chunk[0] ^= 1; } callback(null, chunk);
        } }), response);
        return;
      }
      if (fault === 'overlong') {
        for await (const chunk of createReadStream(join(root, `${id}.body`))) response.write(chunk);
        response.end('extra'); return;
      }
      await pipeline(createReadStream(join(root, `${id}.body`)), response); return;
    }
    send(response, 404, {});
  }
  async function start() {
    server = createServer((request, response) => { handle(request, response).catch(error => { if (!response.headersSent) send(response, 500, {}); else response.destroy(error); }); });
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    endpoint = `http://127.0.0.1:${server.address().port}`;
  }
  await start();
  return { requests, records, capabilities,
    endpoint: () => endpoint, setJob: value => { jobId = value; }, setAttempt: value => { runAttempt = value; }, setFault: value => { fault = value; },
    fetcher: (url, init) => {
      assert.equal(init.redirect, 'error'); assert.ok(init.signal instanceof AbortSignal);
      const target = new URL(url);
      return fetch(target.hostname === 'oidc.example' ? new URL(target.pathname + target.search, endpoint) : target, init);
    },
    async restart() { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); await start(); },
    async close() { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); rmSync(root, { recursive: true, force: true }); } };
}

function readInputs(env, published) {
  env.INPUT_MODE = 'read'; env.INPUT_RECEIPT = JSON.stringify(published);
  env.INPUT_PATH = join(env.RUNNER_TEMP, 'consumed.apk'); delete env['INPUT_RETAIN-UNTIL'];
  for (const [input, field] of Object.entries({ 'expected-repository': 'repository', 'expected-repository-id': 'repositoryId', 'expected-source': 'sourceCommit',
    'expected-workflow-ref': 'workflowRef', 'expected-run-id': 'runId', 'expected-run-attempt': 'runAttempt', 'expected-job-id': 'jobId' })) {
    env[`INPUT_${input.toUpperCase()}`] = published.declaration.producer[field];
  }
}

async function prepared(options) {
  const job = workspace(); const remote = await provider(options); job.env['INPUT_TEAM-URL'] = remote.endpoint();
  try {
    const result = await runRetained(job.env, { fetcher: remote.fetcher, log: () => {} });
    return { job, remote, result, async cleanup() { await remote.close(); job.cleanup(); } };
  } catch (error) { await remote.close(); job.cleanup(); throw error; }
}

test('real HTTP publishes one opaque file and a different job verifies the complete byte identity', async () => {
  const fixture = await prepared();
  try {
    const { job, remote, result } = fixture;
    assert.equal(result.receipt.state, 'published');
    assert.equal(result.receipt.declaration.digest, hash(readFileSync(job.env.INPUT_PATH)));
    assert.equal(statSync(result.receiptPath).mode & 0o777, 0o600);
    remote.setJob('301'); readInputs(job.env, result.receipt);
    const logs = [];
    const consumed = await runRetained(job.env, { fetcher: remote.fetcher, log: value => logs.push(value) });
    assert.equal(hash(readFileSync(job.env.INPUT_PATH)), result.receipt.declaration.digest);
    assert.deepEqual(consumed.receipt, result.receipt);
    assert.equal(fileValues(job.env.GITHUB_OUTPUT).published, 'true');
    assert.equal(remote.requests.filter(value => value.method === 'PUT').length, 1);
    assert.equal(remote.requests.filter(value => value.method === 'GET' && value.route.endsWith('/content')).length, 1);
    const report = JSON.parse(readFileSync(consumed.timingsPath));
    assert.equal(report.acceptedPayloadBytes, 1024 * 1024); assert.equal(report.outcome, 'success');
    assert.ok(report.attempts.some(value => value.operation === 'download' && value.consumedBytes === 1024 * 1024 && value.firstBodyMs >= value.headersMs));
    assert.ok(report.phases.some(value => value.name === 'disk-flush'));
    assert.ok(report.phases.some(value => value.name === 'local-publication'));
    assert.ok(!JSON.stringify(report).includes('fixture-') && !/https?:\/\//.test(JSON.stringify(report)) && !JSON.stringify(report).includes(job.root));
    assert.ok(logs.filter(value => !value.startsWith('::add-mask::')).every(value => !value.includes('fixture') && !value.includes('http')));
    assert.ok(!readFileSync(consumed.receiptPath, 'utf8').includes('Token'));
  } finally { await fixture.cleanup(); }
});

test('lost PUT acknowledgement is reconciled, and exact retry after provider restart performs no second upload', async () => {
  const fixture = await prepared({ lostAck: true });
  try {
    const { job, remote, result } = fixture;
    const firstReport = JSON.parse(readFileSync(result.timingsPath));
    assert.equal(firstReport.attempts.find(value => value.operation === 'upload').reconciled, true);
    assert.equal(remote.requests.filter(value => value.method === 'PUT').length, 1);
    await remote.restart(); job.env['INPUT_TEAM-URL'] = remote.endpoint();
    const retried = await runRetained(job.env, { fetcher: remote.fetcher, log: () => {} });
    assert.deepEqual(retried.receipt, result.receipt);
    assert.equal(remote.requests.filter(value => value.method === 'PUT').length, 1);
    job.env['INPUT_RETAIN-UNTIL'] = new Date(Date.parse(job.env['INPUT_RETAIN-UNTIL']) + 1000).toISOString();
    await assert.rejects(runRetained(job.env, { fetcher: remote.fetcher, log: () => {} }), /does not match/);
    assert.equal(remote.requests.filter(value => value.method === 'PUT').length, 1);
    assert.equal(existsSync(job.env['INPUT_RECEIPT-OUTPUT']), false);
  } finally { await fixture.cleanup(); }
});

test('partial, corrupt, oversized, encoded and foreign content never replace a prior artifact or receipt', async () => {
  const fixture = await prepared();
  try {
    const { job, remote, result } = fixture;
    remote.setJob('301'); readInputs(job.env, result.receipt);
    for (const fault of ['partial', 'corrupt', 'overlong', 'encoded', 'foreign-receipt']) {
      remote.setFault(fault);
      writeFileSync(job.env.INPUT_PATH, 'stale successful artifact');
      writeFileSync(job.env['INPUT_RECEIPT-OUTPUT'], JSON.stringify(result.receipt));
      await assert.rejects(runRetained(job.env, { fetcher: remote.fetcher, log: () => {} }), undefined, fault);
      assert.equal(existsSync(job.env.INPUT_PATH), false, fault);
      assert.equal(existsSync(job.env['INPUT_RECEIPT-OUTPUT']), false, fault);
      assert.equal(fileValues(job.env.GITHUB_OUTPUT).published, 'false', fault);
    }
  } finally { await fixture.cleanup(); }
});

test('read-only authority cannot reserve or upload and same-job or foreign expected producers are rejected', async () => {
  const fixture = await prepared();
  try {
    const { job, remote, result } = fixture;
    readInputs(job.env, result.receipt);
    await assert.rejects(runRetained(job.env, { fetcher: remote.fetcher, log: () => {} }), /another job/);
    remote.setJob('301'); job.env['INPUT_EXPECTED-JOB-ID'] = '999';
    await assert.rejects(runRetained(job.env, { fetcher: remote.fetcher, log: () => {} }), /Unexpected retained producer/);
    readInputs(job.env, result.receipt);
    await runRetained(job.env, { fetcher: remote.fetcher, log: () => {} });
    const [token] = [...remote.capabilities].find(([, value]) => value.mode === 'read');
    const response = await fetch(`${remote.endpoint()}/v1/retained-artifacts`, { method: 'POST', headers: { Authorization: `Bearer ${token}` }, body: '{}' });
    assert.equal(response.status, 403);
    const put = await fetch(`${remote.endpoint()}/v1/retained-artifacts/${result.receipt.id}/content`, { method: 'PUT', headers: { Authorization: `Bearer ${token}` }, body: 'x' });
    assert.equal(put.status, 403);
    assert.equal(remote.requests.filter(value => value.method === 'PUT').length, 2);
  } finally { await fixture.cleanup(); }
});

test('consumer-only rerun reads one explicit older producer attempt while future and foreign attempts fail', async () => {
  const fixture = await prepared();
  try {
    const { job, remote, result } = fixture;
    remote.setJob('302'); remote.setAttempt('2'); job.env.GITHUB_RUN_ATTEMPT = '2';
    readInputs(job.env, result.receipt);
    const retried = await runRetained(job.env, { fetcher: remote.fetcher, log: () => {} });
    assert.deepEqual(retried.receipt, result.receipt);
    assert.equal(remote.requests.filter(value => value.method === 'PUT').length, 1);
    const future = structuredClone(result.receipt);
    future.declaration.producer.runAttempt = '3'; future.id = publicationID(future.declaration);
    readInputs(job.env, future);
    await assert.rejects(runRetained(job.env, { fetcher: remote.fetcher, log: () => {} }), /current or earlier/);
    const foreign = structuredClone(result.receipt);
    foreign.declaration.producer.runId = '999'; foreign.id = publicationID(foreign.declaration);
    readInputs(job.env, foreign);
    await assert.rejects(runRetained(job.env, { fetcher: remote.fetcher, log: () => {} }), /same run/);
    assert.equal(existsSync(job.env.INPUT_PATH), false);
  } finally { await fixture.cleanup(); }
});

test('publisher fails before admission when checkout HEAD differs from signed source', async () => {
  const job = workspace(); const remote = await provider(); job.env['INPUT_TEAM-URL'] = remote.endpoint();
  const other = join(job.root, 'other-checkout');
  try {
    execFileSync('git', ['init', '--quiet', '--initial-branch=main', other]);
    execFileSync('git', ['-C', other, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', '-c', 'core.hooksPath=/dev/null', 'commit', '--quiet', '--allow-empty', '-m', 'different source']);
    job.env.GITHUB_WORKSPACE = other;
    await assert.rejects(runRetained(job.env, { fetcher: remote.fetcher, log: () => {} }), /Checked-out source does not match/);
    assert.equal(remote.requests.filter(value => value.method === 'PUT' || value.method === 'POST' && value.route === '/v1/retained-artifacts').length, 0);
    assert.equal(fileValues(job.env.GITHUB_OUTPUT).published, 'false');
  } finally { await remote.close(); job.cleanup(); }
});

test('quota refusal is a required failure before content, and wrong exchange authority cannot publish', async () => {
  for (const options of [{ admissionStatus: 507 }, { wrongAuthority: true }]) {
    const job = workspace(); const remote = await provider(options); job.env['INPUT_TEAM-URL'] = remote.endpoint();
    writeFileSync(job.env['INPUT_RECEIPT-OUTPUT'], 'stale receipt');
    try {
      await assert.rejects(runRetained(job.env, { fetcher: remote.fetcher, log: () => {} }));
      assert.equal(remote.requests.filter(value => value.method === 'PUT').length, 0);
      assert.equal(existsSync(job.env['INPUT_RECEIPT-OUTPUT']), false);
      assert.equal(fileValues(job.env.GITHUB_OUTPUT).published, 'false');
    } finally { await remote.close(); job.cleanup(); }
  }
});

test('authentication failure invalidates old reader output, and zero-byte publication remains exact', async () => {
  const fixture = await prepared();
  try {
    const { job, remote, result } = fixture;
    remote.setJob('301'); readInputs(job.env, result.receipt);
    writeFileSync(job.env.INPUT_PATH, 'old artifact');
    delete job.env.ACTIONS_ID_TOKEN_REQUEST_TOKEN;
    await assert.rejects(runRetained(job.env, { fetcher: remote.fetcher, log: () => {} }), /requires GitHub OIDC/);
    assert.equal(existsSync(job.env.INPUT_PATH), false);
    assert.equal(existsSync(job.env['INPUT_RECEIPT-OUTPUT']), false);
    assert.equal(fileValues(job.env.GITHUB_OUTPUT).published, 'false');
    writeFileSync(job.env.INPUT_PATH, 'old direct-client artifact');
    await assert.rejects(downloadFile({ endpoint: remote.endpoint(), token: '', path: job.env.INPUT_PATH,
      receipt: result.receipt, expected: { producer: result.receipt.declaration.producer, name: 'qa-apk' } }), /Missing retained authority/);
    assert.equal(existsSync(job.env.INPUT_PATH), false);
  } finally { await fixture.cleanup(); }
  const job = workspace(); const remote = await provider(); job.env['INPUT_TEAM-URL'] = remote.endpoint();
  try {
    writeFileSync(job.env.INPUT_PATH, '');
    const result = await runRetained(job.env, { fetcher: remote.fetcher, log: () => {} });
    assert.equal(result.receipt.declaration.size, 0);
    assert.equal(result.receipt.declaration.digest, hash(''));
    remote.setJob('301'); readInputs(job.env, result.receipt);
    await runRetained(job.env, { fetcher: remote.fetcher, log: () => {} });
    assert.equal(statSync(job.env.INPUT_PATH).size, 0);
  } finally { await remote.close(); job.cleanup(); }
});

test('body inactivity is bounded and never leaves a usable artifact', async () => {
  const fixture = await prepared();
  try {
    const { job, remote, result } = fixture;
    remote.setJob('301'); readInputs(job.env, result.receipt);
    await runRetained(job.env, { fetcher: remote.fetcher, log: () => {} });
    const [token] = [...remote.capabilities].find(([, value]) => value.mode === 'read');
    remote.setFault('stalled');
    await assert.rejects(downloadFile({ endpoint: remote.endpoint(), token, path: job.env.INPUT_PATH, receipt: result.receipt,
      expected: { producer: result.receipt.declaration.producer, name: 'qa-apk' }, fetcher: remote.fetcher, idleTimeoutMs: 30, bodyTimeoutMs: 1000 }));
    assert.equal(existsSync(job.env.INPUT_PATH), false);
  } finally { await fixture.cleanup(); }
});

test('staging, expired and unavailable receipts are authoritative failures without an upload retry', async () => {
  const fixture = await prepared();
  try {
    const { job, remote, result } = fixture;
    for (const state of ['staging', 'expired', 'unavailable']) {
      remote.records.get(result.receipt.id).state = state;
      await assert.rejects(runRetained(job.env, { fetcher: remote.fetcher, log: () => {} }), /not uploadable/);
    }
    assert.equal(remote.requests.filter(value => value.method === 'PUT').length, 1);
  } finally { await fixture.cleanup(); }
});

test('publication identity matches Go JSON field order and HTML escaping; forged metadata is rejected', () => {
  const declared = { producer: { ...baseProducer, project: 'private<&>', profile: 'profile-v1' }, name: 'a<&>\u2028\u2029z',
    digest: 'd'.repeat(64), size: 0, retainUntil: retention() };
  const encoded = JSON.stringify({ producer: declared.producer, name: declared.name }).replaceAll('<', '\\u003c').replaceAll('>', '\\u003e').replaceAll('&', '\\u0026').replaceAll('\u2028', '\\u2028').replaceAll('\u2029', '\\u2029');
  assert.equal(publicationID(declared), hash(encoded));
  const value = { schemaVersion: 'retained-artifact-v1', id: publicationID(declared), state: 'published', declaration: declared,
    reservedUntil: new Date().toISOString(), createdAt: new Date().toISOString(), publishedAt: new Date().toISOString() };
  assert.throws(() => expectedReceipt(value, { producer: baseProducer, name: 'qa-apk' }), /Unexpected/);
  assert.throws(() => receipt({ ...value, bearerToken: 'not allowed' }), /Invalid retained metadata/);
  assert.throws(() => receipt({ ...value, id: 'f'.repeat(64) }), /does not match/);
});

test('exact deadlines preserve microseconds and normalize timezone offsets without accepting finer precision', () => {
  const declared = { producer: baseProducer, name: 'qa-apk', digest: 'd'.repeat(64), size: 0, retainUntil: '2030-01-01T00:00:00.123456Z' };
  const value = { schemaVersion: 'retained-artifact-v1', id: publicationID(declared), state: 'published', declaration: declared,
    reservedUntil: '2029-01-01T00:15:00.123456Z', createdAt: '2029-01-01T00:00:00.123456Z', publishedAt: '2029-01-01T00:01:00.123456Z' };
  assert.equal(receipt(value, declared).declaration.retainUntil, declared.retainUntil);
  assert.throws(() => receipt({ ...value, declaration: { ...declared, retainUntil: '2030-01-01T00:00:00.123457Z' } }, declared), /does not match/);
  assert.doesNotThrow(() => receipt({ ...value, declaration: { ...declared, retainUntil: '2030-01-01T01:00:00.123456+01:00' } }, declared));
  assert.throws(() => declaration({ ...declared, retainUntil: '2030-01-01T00:00:00.1234567Z' }), /microsecond/);
  assert.throws(() => declaration({ ...declared, producer: { ...baseProducer, sourceCommit: 'a'.repeat(41) } }), /producer identity/);
});

test('124078900-byte file streams once per direction without chunk RPCs or a CLI', { timeout: 60_000 }, async () => {
  const job = workspace(); const remote = await provider(); job.env['INPUT_TEAM-URL'] = remote.endpoint();
  const bytes = 124_078_900;
  try {
    const source = createWriteStream(job.env.INPUT_PATH, { flags: 'w' });
    const block = Buffer.alloc(1024 * 1024, 0x6c);
    for (let offset = 0; offset < bytes; offset += block.length) {
      if (!source.write(block.subarray(0, Math.min(block.length, bytes - offset)))) await new Promise(resolve => source.once('drain', resolve));
    }
    source.end(); await new Promise(resolve => source.once('finish', resolve));
    const result = await runRetained(job.env, { fetcher: remote.fetcher, log: () => {} });
    remote.setJob('301'); readInputs(job.env, result.receipt);
    const restored = await runRetained(job.env, { fetcher: remote.fetcher, log: () => {} });
    assert.equal(statSync(job.env.INPUT_PATH).size, bytes);
    assert.equal(restored.receipt.declaration.size, bytes);
    const report = JSON.parse(readFileSync(restored.timingsPath));
    assert.equal(report.acceptedPayloadBytes, bytes);
    assert.equal(remote.requests.filter(value => value.method === 'PUT').length, 1);
    assert.equal(remote.requests.filter(value => value.route.endsWith('/content') && value.method === 'GET').length, 1);
    assert.ok(report.completeMs > 0);
    process.stdout.write(`# retained-client 124078900-byte local loopback fixture completeMs=${report.completeMs.toFixed(3)}; includes hashing/disk/backpressure, not bandwidth\n`);
  } finally { await remote.close(); job.cleanup(); }
});
