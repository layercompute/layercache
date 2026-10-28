import { createHash, randomUUID } from 'node:crypto';
import { createWriteStream, constants } from 'node:fs';
import { lstat, mkdir, mkdtemp, open, realpath, rename, rm } from 'node:fs/promises';
import { basename, dirname, join, resolve } from 'node:path';
import { Readable, Transform } from 'node:stream';
import { pipeline } from 'node:stream/promises';
import { performance } from 'node:perf_hooks';
import { endpointURL } from '../setup/auth.ts';
import { projectAudience } from '../setup/oidc.ts';

const SCHEMA = 'retained-artifact-v1';
const MAX_METADATA = 64 * 1024;
const digestPattern = /^[0-9a-f]{64}$/;
const numberPattern = /^[1-9][0-9]{0,19}$/;
const producerFields = ['project', 'repository', 'repositoryId', 'sourceCommit', 'workflowRef', 'runId', 'runAttempt', 'jobId', 'profile'];
const states = new Set(['reserved', 'staging', 'published', 'expired', 'revoked', 'unavailable']);
const escapeCommand = value => value.replaceAll('%', '%25').replaceAll('\r', '%0D').replaceAll('\n', '%0A');

export class RetainedError extends Error {
  constructor(message, status) { super(message); this.name = 'RetainedError'; this.status = status; }
}

function exactKeys(value, keys, optional = []) {
  if (!value || typeof value !== 'object' || Array.isArray(value) ||
      keys.some(key => !Object.hasOwn(value, key)) ||
      Object.keys(value).some(key => !keys.includes(key) && !optional.includes(key))) throw new RetainedError('Invalid retained metadata');
}

function textValue(value, max = 512) {
  return typeof value === 'string' && value.length > 0 && value.length <= max &&
    value.trim() === value && !/[\x00-\x1f\x7f]/.test(value);
}

function timestamp(value) {
  if (typeof value !== 'string' || !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?(?:Z|[+-]\d\d:\d\d)$/.test(value) ||
      !Number.isFinite(Date.parse(value))) throw new RetainedError('Invalid retained timestamp');
  return Date.parse(value);
}

function timestampIdentity(value) {
  timestamp(value);
  const match = /^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?(Z|[+-]\d\d:\d\d)$/.exec(value);
  return BigInt(Date.parse(`${match[1]}${match[3]}`)) * 1_000_000n + BigInt((match[2] || '').padEnd(9, '0'));
}

export function producer(value) {
  exactKeys(value, producerFields);
  if (!producerFields.every(key => textValue(value[key])) || value.project.length > 256 ||
      !/^[a-z0-9][a-z0-9_.:+@-]{0,255}$/.test(value.profile) ||
      !/^[a-z0-9_.-]+\/[a-z0-9_.-]+$/.test(value.repository) ||
      !['repositoryId', 'runId', 'runAttempt', 'jobId'].every(key => numberPattern.test(value[key])) ||
      !/^(?:[0-9a-f]{40}|[0-9a-f]{64})$/.test(value.sourceCommit) ||
      !value.workflowRef.toLowerCase().startsWith(`${value.repository}/.github/workflows/`) || !value.workflowRef.includes('@')) throw new RetainedError('Invalid retained producer identity');
  return Object.fromEntries(producerFields.map(key => [key, value[key]]));
}

export function declaration(value) {
  exactKeys(value, ['producer', 'name', 'digest', 'size', 'retainUntil']);
  const identity = producer(value.producer);
  if (!textValue(value.name) || !digestPattern.test(value.digest) || !Number.isSafeInteger(value.size) || value.size < 0) throw new RetainedError('Invalid retained declaration');
  timestamp(value.retainUntil);
  if (/\.\d{7,}/.test(value.retainUntil)) throw new RetainedError('Retention deadline exceeds microsecond precision');
  return { producer: identity, name: value.name, digest: value.digest, size: value.size, retainUntil: value.retainUntil };
}

export function publicationID(value) {
  // Match encoding/json's stable struct field order and HTML escaping.
  const json = JSON.stringify({ producer: producer(value.producer), name: value.name })
    .replace(/[<>&\u2028\u2029]/g, character => `\\u${character.charCodeAt(0).toString(16).padStart(4, '0')}`);
  return createHash('sha256').update(json).digest('hex');
}

function sameDeclaration(actual, expected) {
  return producerFields.every(key => actual.producer[key] === expected.producer[key]) &&
    actual.name === expected.name && actual.digest === expected.digest && actual.size === expected.size &&
    timestampIdentity(actual.retainUntil) === timestampIdentity(expected.retainUntil);
}

export function receipt(value, expected, { published = false, now = Date.now() } = {}) {
  exactKeys(value, ['schemaVersion', 'id', 'state', 'declaration', 'reservedUntil', 'createdAt'], ['publishedAt']);
  const content = declaration(value.declaration);
  if (value.schemaVersion !== SCHEMA || !digestPattern.test(value.id) || value.id !== publicationID(content) || !states.has(value.state) ||
      (expected && !sameDeclaration(content, expected))) throw new RetainedError('Retained receipt does not match the requested identity');
  timestamp(value.reservedUntil);
  timestamp(value.createdAt);
  if (value.publishedAt !== undefined && timestampIdentity(value.publishedAt) < timestampIdentity(value.createdAt)) throw new RetainedError('Invalid retained publication timestamp');
  if (value.state === 'published' && value.publishedAt === undefined) throw new RetainedError('Published receipt is incomplete');
  if (published && (value.state !== 'published' || timestamp(content.retainUntil) <= now)) throw new RetainedError('Retained publication is not currently active');
  return { schemaVersion: SCHEMA, id: value.id, state: value.state, declaration: content,
    reservedUntil: value.reservedUntil, createdAt: value.createdAt, ...(value.publishedAt === undefined ? {} : { publishedAt: value.publishedAt }) };
}

export function expectedReceipt(value, expected, now = Date.now()) {
  const result = receipt(value, undefined, { published: true, now });
  const identity = producer(expected.producer);
  if (!producerFields.every(key => result.declaration.producer[key] === identity[key]) || result.declaration.name !== expected.name) throw new RetainedError('Unexpected retained producer');
  return result;
}

export function evidence() {
  const start = performance.now();
  return { schemaVersion: 'retained-transfer-v1', outcome: 'failed', attempts: [], phases: [], acceptedPayloadBytes: 0,
    clock: 'monotonic offsets in milliseconds; overlapping intervals are not additive',
    transport: 'one opaque streaming request; consumed body bytes include local hashing/disk backpressure and are not pure network bandwidth',
    unavailablePhases: ['DNS', 'TCP connection', 'TLS handshake', 'server durable commit internals unless reported in Server-Timing'],
    start: () => performance.now() - start,
    phase(name, from, extra = {}) { this.phases.push({ name, startMs: from, endMs: performance.now() - start, ...extra }); },
    json() { const { start: ignoredStart, phase: ignoredPhase, json: ignoredJSON, ...result } = this; return { ...result, completeMs: performance.now() - start }; } };
}

function urlFor(endpoint, path) {
  const url = endpointURL(endpoint);
  url.pathname = `${url.pathname.replace(/\/+$/, '').replace(/\/v1$/, '')}/v1/${path}`;
  return url;
}

function requestBudget(milliseconds, signal) {
  return signal ? AbortSignal.any([signal, AbortSignal.timeout(milliseconds)]) : AbortSignal.timeout(milliseconds);
}

async function boundedJSON(response, signal, observe = () => {}) {
  if (!response.body) throw new RetainedError('Retained metadata response has no body');
  const data = [];
  let size = 0;
  for await (const chunk of Readable.fromWeb(response.body, { signal })) {
    size += chunk.length;
    observe(chunk.length);
    if (size > MAX_METADATA) throw new RetainedError('Retained metadata response exceeds its bound');
    data.push(chunk);
  }
  try { return JSON.parse(Buffer.concat(data).toString('utf8')); }
  catch { throw new RetainedError('Invalid retained metadata response'); }
}

function failStatus(response) {
  if (!response.ok) {
    response.body?.cancel().catch(() => {});
    throw new RetainedError(`Retained request returned HTTP ${response.status}`, response.status);
  }
}

function serverTiming(response) {
  const raw = response.headers.get('server-timing');
  if (!raw || raw.length > 1024) return undefined;
  const intervals = raw.split(',').flatMap(value => {
    const result = /^\s*([a-zA-Z][a-zA-Z0-9_-]{0,63});dur=(\d+(?:\.\d+)?)\s*$/.exec(value);
    return result && Number.isFinite(Number(result[2])) ? [{ name: result[1], durationMs: Number(result[2]) }] : [];
  });
  return intervals.length ? intervals : undefined;
}

async function metadataRequest(options, path, { method = 'GET', body, operation = path, token = options.token } = {}) {
  const signal = requestBudget(options.metadataTimeoutMs ?? 30_000, options.signal);
  const attempt = { operation, attempt: options.evidence.attempts.filter(value => value.operation === operation).length + 1,
    startMs: options.evidence.start(), consumedBytes: 0, outcome: 'failed' };
  options.evidence.attempts.push(attempt);
  try {
    const response = await options.fetcher(urlFor(options.endpoint, path), { method, headers: {
      ...(token ? { Authorization: `Bearer ${token}` } : {}), ...(body ? { 'Content-Type': 'application/json' } : {}),
      Accept: 'application/json' }, body: body ? JSON.stringify(body) : undefined, redirect: 'error', signal });
    attempt.headersMs = options.evidence.start();
    attempt.httpStatus = response.status;
    attempt.serverTiming = serverTiming(response);
    failStatus(response);
    const value = await boundedJSON(response, signal, bytes => { attempt.consumedBytes += bytes; });
    attempt.outcome = 'success';
    return value;
  } finally { attempt.endMs = options.evidence.start(); }
}

function decodeClaims(token) {
  if (typeof token !== 'string' || token.length > MAX_METADATA || !/^[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+$/.test(token)) throw new RetainedError('Invalid identity token');
  try { return JSON.parse(Buffer.from(token.split('.')[1], 'base64url').toString('utf8')); }
  catch { throw new RetainedError('Invalid identity claims'); }
}

export async function exchangeRetained({ endpoint, project, profile, mode, env = process.env, fetcher = fetch,
  log = value => process.stdout.write(value), evidence: report = evidence(), metadataTimeoutMs = 30_000, signal } = {}) {
  if (!['publish', 'read'].includes(mode) || !textValue(project, 256) || !/^[a-z0-9][a-z0-9_.:+@-]{0,255}$/.test(profile)) throw new RetainedError('Invalid retained exchange inputs');
  if (!env.ACTIONS_ID_TOKEN_REQUEST_URL || !env.ACTIONS_ID_TOKEN_REQUEST_TOKEN || !env['INPUT_GITHUB-TOKEN']) throw new RetainedError('Retained transfer requires GitHub OIDC and repository metadata authorization');
  const oidc = new URL(env.ACTIONS_ID_TOKEN_REQUEST_URL);
  if (oidc.protocol !== 'https:' || oidc.username || oidc.password) throw new RetainedError('GitHub OIDC endpoint must use HTTPS');
  oidc.searchParams.set('audience', projectAudience(project));
  const started = report.start();
  const request = { operation: 'github-oidc', attempt: 1, startMs: started, consumedBytes: 0, outcome: 'failed' };
  report.attempts.push(request);
  let identity;
  try {
    const response = await fetcher(oidc, { headers: { Authorization: `Bearer ${env.ACTIONS_ID_TOKEN_REQUEST_TOKEN}` },
      redirect: 'error', signal: requestBudget(metadataTimeoutMs, signal) });
    request.headersMs = report.start(); request.httpStatus = response.status;
    failStatus(response);
    identity = await boundedJSON(response, requestBudget(metadataTimeoutMs, signal), bytes => { request.consumedBytes += bytes; });
    request.outcome = 'success';
  } finally { request.endMs = report.start(); }
  const claims = decodeClaims(identity.value);
  log(`::add-mask::${escapeCommand(identity.value)}\n`);
  const current = producer({ project, profile, repository: claims.repository?.toLowerCase(), repositoryId: claims.repository_id,
    sourceCommit: claims.sha, workflowRef: claims.workflow_ref, runId: claims.run_id, runAttempt: claims.run_attempt, jobId: claims.check_run_id });
  // These decoded fields only form the request and detect a runner mismatch.
  // The service verifies the signed identity and metadata independently.
  if (current.repository !== env.GITHUB_REPOSITORY?.toLowerCase() || current.repositoryId !== env.GITHUB_REPOSITORY_ID ||
      current.sourceCommit !== env.GITHUB_SHA || current.runId !== env.GITHUB_RUN_ID || current.runAttempt !== env.GITHUB_RUN_ATTEMPT ||
      current.workflowRef !== env.GITHUB_WORKFLOW_REF) throw new RetainedError('GitHub runner and identity claims differ');
  const result = await metadataRequest({ endpoint, fetcher, metadataTimeoutMs, signal, evidence: report }, 'auth/github-oidc/exchange', {
    method: 'POST', operation: 'exchange', body: { project, compatibility: profile, integration: 'retained', mode,
      idToken: identity.value, githubToken: env['INPUT_GITHUB-TOKEN'], ttlSeconds: 3600 } });
  if (typeof result.teamToken !== 'string' || !/^lc2\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+$/.test(result.teamToken) ||
      timestamp(result.expiresAt) < Date.now() + 30_000 || timestamp(result.expiresAt) > Date.now() + 3_660_000) throw new RetainedError('Invalid retained capability');
  const authorized = producer(result.producer);
  if (result.mode !== mode || !producerFields.every(key => authorized[key] === current[key])) throw new RetainedError('Retained authority does not match this job');
  log(`::add-mask::${escapeCommand(result.teamToken)}\n`);
  report.phase('identity-and-exchange', started);
  return { token: result.teamToken, expiresAt: result.expiresAt, current };
}

async function hashFile(path, report, signal) {
  const started = report.start();
  const handle = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const before = await handle.stat({ bigint: true });
    if (!before.isFile() || before.size > BigInt(Number.MAX_SAFE_INTEGER)) throw new RetainedError('Retained publication requires one regular file');
    const hash = createHash('sha256');
    let size = 0;
    for await (const chunk of handle.createReadStream({ autoClose: false, signal })) {
      hash.update(chunk); size += chunk.length;
      if (BigInt(size) > before.size) throw new RetainedError('Retained source grew during hashing');
    }
    const after = await handle.stat({ bigint: true });
    if (before.size !== after.size || before.mtimeNs !== after.mtimeNs || before.ctimeNs !== after.ctimeNs || BigInt(size) !== before.size) throw new RetainedError('Retained source changed during hashing');
    report.phase('source-hash', started, { consumedBytes: size });
    return { digest: hash.digest('hex'), size, signature: { dev: after.dev, ino: after.ino, size: after.size, mtimeNs: after.mtimeNs, ctimeNs: after.ctimeNs } };
  } finally { await handle.close(); }
}

function sameFile(stat, signature) {
  return ['dev', 'ino', 'size', 'mtimeNs', 'ctimeNs'].every(key => stat[key] === signature[key]);
}

function optionsFor(options) {
  if (!textValue(options.endpoint, 4096) || /\s/.test(options.endpoint)) throw new RetainedError('Invalid retained endpoint');
  endpointURL(options.endpoint);
  if (!options.token || /[\x00-\x20\x7f]/.test(options.token)) throw new RetainedError('Missing retained authority');
  return { fetcher: fetch, evidence: evidence(), bodyTimeoutMs: 30 * 60_000, idleTimeoutMs: 120_000, ...options };
}

async function inspect(options, id, expected) {
  return receipt(await metadataRequest(options, `retained-artifacts/${id}`, { operation: 'inspect' }), expected);
}

export async function publishFile(options) {
  const settings = optionsFor(options);
  const { evidence: report } = settings;
  const source = resolve(settings.path);
  const content = await hashFile(source, report, requestBudget(settings.bodyTimeoutMs, settings.signal));
  const declared = declaration({ producer: settings.producer, name: settings.name, digest: content.digest,
    size: content.size, retainUntil: settings.retainUntil });
  if (timestamp(declared.retainUntil) <= Date.now() || timestamp(declared.retainUntil) > Date.now() + 86_400_000) throw new RetainedError('Retention deadline must be in the next 24 hours');
  const id = publicationID(declared);
  let reserved;
  try { reserved = await inspect(settings, id, declared); }
  catch (error) { if (error.status !== 404) throw error; }
  if (!reserved) {
    try { reserved = receipt(await metadataRequest(settings, 'retained-artifacts', { method: 'POST', operation: 'reserve', body: declared }), declared); }
    catch (error) {
      // Reservation acknowledgement may be lost. Resolve the exact identity;
      // never create a different identity or extend its deadline.
      try { reserved = await inspect(settings, id, declared); }
      catch { throw error; }
    }
  }
  if (reserved.state === 'published') {
    report.outcome = 'success'; report.acceptedPayloadBytes = declared.size;
    return receipt(reserved, declared, { published: true });
  }
  if (reserved.state !== 'reserved' || timestamp(reserved.reservedUntil) <= Date.now()) throw new RetainedError('Retained reservation is not uploadable');
  const transfer = { operation: 'upload', attempt: 1, startMs: report.start(), consumedBytes: 0, outcome: 'failed' };
  report.attempts.push(transfer);
  const abort = new AbortController();
  const signal = requestBudget(settings.bodyTimeoutMs, settings.signal ? AbortSignal.any([settings.signal, abort.signal]) : abort.signal);
  let idle;
  const touch = () => { clearTimeout(idle); idle = setTimeout(() => abort.abort(new RetainedError('Retained upload stopped making progress')), settings.idleTimeoutMs); idle.unref(); };
  const hash = createHash('sha256');
  let finished = false;
  const handle = await open(source, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    if (!sameFile(await handle.stat({ bigint: true }), content.signature)) throw new RetainedError('Retained source changed before upload');
    async function* chunks() {
      for await (const chunk of handle.createReadStream({ autoClose: false, signal, start: 0 })) {
        if (transfer.firstBodyMs === undefined) transfer.firstBodyMs = report.start();
        transfer.consumedBytes += chunk.length; hash.update(chunk); touch(); yield chunk;
      }
      transfer.bodyEndMs = report.start(); finished = true;
    }
    touch();
    let published;
    try {
      const response = await settings.fetcher(urlFor(settings.endpoint, `retained-artifacts/${id}/content`), {
        method: 'PUT', headers: { Authorization: `Bearer ${settings.token}`, 'Content-Type': 'application/octet-stream',
          'Content-Length': String(declared.size), Accept: 'application/json' }, body: Readable.from(chunks()), duplex: 'half', redirect: 'error', signal });
      transfer.headersMs = report.start(); transfer.httpStatus = response.status;
      transfer.serverTiming = serverTiming(response);
      failStatus(response);
      published = receipt(await boundedJSON(response, signal), declared, { published: true });
      if (!finished || transfer.consumedBytes !== declared.size || hash.digest('hex') !== declared.digest ||
          !sameFile(await handle.stat({ bigint: true }), content.signature)) throw new RetainedError('Retained source changed during upload');
    } catch (error) {
      // A failed PUT response is an unknown commit. One authoritative GET may
      // confirm success. There is no blind second upload.
      abort.abort();
      try { published = receipt(await inspect(settings, id, declared), declared, { published: true }); transfer.reconciled = true; }
      catch { throw error; }
    }
    transfer.outcome = 'success'; report.acceptedPayloadBytes = declared.size; report.outcome = 'success';
    return published;
  } finally {
    clearTimeout(idle); abort.abort(); await handle.close(); transfer.endMs = report.start();
  }
}

export async function invalidateOutput(path) {
  const destination = resolve(path);
  const previous = await lstat(destination).catch(error => { if (error.code === 'ENOENT') return undefined; throw error; });
  if (previous && !previous.isFile() && !previous.isSymbolicLink()) throw new RetainedError('Retained output must be a file path');
  if (previous) await rm(destination);
  return destination;
}

export async function atomicJSON(path, value) {
  const destination = resolve(path);
  await mkdir(dirname(destination), { recursive: true, mode: 0o700 });
  const temporary = join(await realpath(dirname(destination)), `.layercache-receipt-${randomUUID()}`);
  const handle = await open(temporary, 'wx', 0o600);
  try {
    try { await handle.writeFile(`${JSON.stringify(value, null, 2)}\n`); await handle.sync(); }
    finally { await handle.close(); }
    await rename(temporary, destination);
  }
  finally { await rm(temporary, { force: true }); }
}

export async function downloadFile(options) {
  const destination = await invalidateOutput(options.path);
  const settings = optionsFor(options);
  const { evidence: report } = settings;
  const expected = expectedReceipt(settings.receipt, settings.expected);
  const current = receipt(await inspect(settings, expected.id, expected.declaration), expected.declaration, { published: true });
  if (timestampIdentity(current.createdAt) !== timestampIdentity(expected.createdAt) || timestampIdentity(current.reservedUntil) !== timestampIdentity(expected.reservedUntil) ||
      timestampIdentity(current.publishedAt) !== timestampIdentity(expected.publishedAt)) throw new RetainedError('Retained receipt history differs');
  await mkdir(dirname(destination), { recursive: true, mode: 0o700 });
  const parent = await realpath(dirname(destination));
  const stage = await mkdtemp(join(parent, '.layercache-retained-'));
  const stagingFile = join(stage, 'content');
  const transfer = { operation: 'download', attempt: 1, startMs: report.start(), consumedBytes: 0, outcome: 'failed' };
  report.attempts.push(transfer);
  const abort = new AbortController();
  const signal = requestBudget(settings.bodyTimeoutMs, settings.signal ? AbortSignal.any([settings.signal, abort.signal]) : abort.signal);
  let idle;
  const touch = () => { clearTimeout(idle); idle = setTimeout(() => abort.abort(new RetainedError('Retained download stopped making progress')), settings.idleTimeoutMs); idle.unref(); };
  try {
    touch();
    const response = await settings.fetcher(urlFor(settings.endpoint, `retained-artifacts/${expected.id}/content`), {
      headers: { Authorization: `Bearer ${settings.token}`, Accept: 'application/octet-stream', 'Accept-Encoding': 'identity' }, redirect: 'error', signal });
    transfer.headersMs = report.start(); transfer.httpStatus = response.status;
    transfer.serverTiming = serverTiming(response);
    failStatus(response);
    if (response.headers.get('x-layercache-retained-id') !== expected.id ||
        response.headers.get('x-layercache-digest') !== `sha256:${expected.declaration.digest}`) throw new RetainedError('Retained response identity headers differ');
    if (response.headers.get('content-encoding') && response.headers.get('content-encoding') !== 'identity') throw new RetainedError('Retained content encoding changed the byte identity');
    const length = response.headers.get('content-length');
    if (length !== null && length !== String(expected.declaration.size)) throw new RetainedError('Retained response size differs');
    if (!response.body) throw new RetainedError('Retained response has no content');
    const hash = createHash('sha256');
    const verify = new Transform({ transform(chunk, encoding, callback) {
      if (transfer.firstBodyMs === undefined) transfer.firstBodyMs = report.start();
      transfer.consumedBytes += chunk.length; touch();
      if (transfer.consumedBytes > expected.declaration.size) { callback(new RetainedError('Retained body exceeds its declaration')); return; }
      hash.update(chunk); callback(null, chunk);
    } });
    await pipeline(Readable.fromWeb(response.body), verify, createWriteStream(stagingFile, { flags: 'wx', mode: 0o600, highWaterMark: 1024 * 1024 }), { signal });
    transfer.bodyEndMs = report.start();
    const verification = report.start();
    if (transfer.consumedBytes !== expected.declaration.size || hash.digest('hex') !== expected.declaration.digest) throw new RetainedError('Retained content failed complete size and SHA256 verification');
    report.phase('complete-size-and-digest', verification);
    const flush = report.start();
    const handle = await open(stagingFile, constants.O_RDONLY | constants.O_NOFOLLOW);
    try { await handle.sync(); } finally { await handle.close(); }
    report.phase('disk-flush', flush);
    const commit = report.start();
    await rename(stagingFile, join(parent, basename(destination)));
    report.phase('local-publication', commit);
    transfer.outcome = 'success'; report.acceptedPayloadBytes = transfer.consumedBytes; report.outcome = 'success';
    return current;
  } finally {
    clearTimeout(idle); abort.abort(); transfer.endMs = report.start(); await rm(stage, { recursive: true, force: true });
  }
}

export async function readReceipt(path) {
  const handle = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const stat = await handle.stat();
    if (!stat.isFile() || stat.size > MAX_METADATA) throw new RetainedError('Invalid retained receipt file');
    const chunks = [];
    let bytes = 0;
    for await (const chunk of handle.createReadStream({ autoClose: false })) {
      bytes += chunk.length;
      if (bytes > MAX_METADATA) throw new RetainedError('Retained receipt file exceeds its bound');
      chunks.push(chunk);
    }
    return JSON.parse(Buffer.concat(chunks).toString('utf8'));
  } finally { await handle.close(); }
}
