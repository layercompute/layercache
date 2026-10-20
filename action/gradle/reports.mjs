import {createHash} from 'node:crypto';
import {constants, closeSync, fstatSync, lstatSync, openSync, readdirSync, readSync} from 'node:fs';
import {join} from 'node:path';
import {parseGradleInfo} from './info.mjs';

const maximumReportBytes = 4 * 1024 ** 2;
const maximumBatchBytes = 16 * 1024 ** 2;
const maximumInfoBytes = 64 * 1024 ** 2;
const outcomes = new Set(['executed', 'fromCache', 'upToDate', 'noSource', 'skipped', 'failed', 'unknown']);
const controls = /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/u;
const unavailable = reason => {throw new Error(reason);};
function object(value) {return value !== null && typeof value === 'object' && !Array.isArray(value);}
function integer(value) {return Number.isSafeInteger(value) && value >= 0;}
function iso(value) {return new Date(value).toISOString();}
function workRef(project, compatibility, taskPath) {
  return 'sha256:' + createHash('sha256').update(`layercache-gradle-task-v1\0${project}\0${compatibility}\0${taskPath}`).digest('hex');
}

// Read only a caller-selected regular file. Never follow a leaf symlink, read
// beyond the bound after stat, or echo contents and paths in report errors.
export function readBoundedFile(path, maximum, {privateFile = false} = {}) {
  const descriptor = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
  try {
    const info = fstatSync(descriptor);
    if (!info.isFile() || info.nlink !== 1 || info.size > maximum ||
        typeof process.getuid === 'function' && info.uid !== process.getuid() ||
        privateFile && (info.mode & 0o077) !== 0) unavailable('unsafe-native-report-file');
    const chunks = [];
    let received = 0;
    for (;;) {
      const buffer = Buffer.alloc(Math.min(65536, maximum - received + 1));
      const length = readSync(descriptor, buffer, 0, buffer.length, null);
      if (!length) break;
      received += length;
      if (received > maximum) unavailable('native-report-file-too-large');
      chunks.push(buffer.subarray(0, length));
    }
    const after = fstatSync(descriptor);
    if (after.size !== received || after.size !== info.size || after.mtimeMs !== info.mtimeMs || after.ctimeMs !== info.ctimeMs) unavailable('native-report-file-changed');
    return {text: Buffer.concat(chunks, received).toString('utf8'), mtimeMs: info.mtimeMs, bytes: received};
  } finally {closeSync(descriptor);}
}

// The report format is private to the collector. Explicitly project its fields
// into the authenticated wire contract; raw task paths and info logs stay here.
export function projectGradleInvocation(document, {project, compatibility, infoText, infoMtimeMs, invocationCount = 1, now = Date.now()} = {}) {
  if (!object(document) || document.version !== 1 || !/^gradle-[0-9a-f-]{36}$/.test(document.invocationId) ||
      typeof document.gradleVersion !== 'string' || document.truncated !== false ||
      !integer(document.startedAtMs) || !integer(document.finishedAtMs) || document.startedAtMs <= 0 ||
      document.finishedAtMs < document.startedAtMs || document.finishedAtMs - document.startedAtMs > 86400000 ||
      document.finishedAtMs > now + 5000 || !['failed', 'unknown'].includes(document.status) ||
      !Array.isArray(document.tasks) || document.tasks.length < 1 || document.tasks.length > 4096 ||
      typeof project !== 'string' || !project || typeof compatibility !== 'string' || !compatibility) unavailable('invalid-native-invocation');
  const tasks = [];
  const seen = new Set();
  let earliest = Infinity, latest = 0;
  for (const row of document.tasks) {
    if (!object(row) || typeof row.taskPath !== 'string' || !row.taskPath.startsWith(':') || row.taskPath.length > 1024 || controls.test(row.taskPath) ||
        row.workRef !== workRef(project, compatibility, row.taskPath) || seen.has(row.workRef) || !outcomes.has(row.outcome) ||
        !integer(row.startedAtMs) || !integer(row.finishedAtMs) || row.startedAtMs < document.startedAtMs ||
        row.finishedAtMs > document.finishedAtMs || row.finishedAtMs < row.startedAtMs ||
        row.executionMs !== undefined && (row.outcome !== 'executed' || !integer(row.executionMs) || row.executionMs !== row.finishedAtMs - row.startedAtMs)) unavailable('invalid-native-task');
    seen.add(row.workRef);
    earliest = Math.min(earliest, row.startedAtMs);
    latest = Math.max(latest, row.finishedAtMs);
    tasks.push({workRef: row.workRef, outcome: row.outcome, startedAt: iso(row.startedAtMs), finishedAt: iso(row.finishedAtMs),
      ...(row.executionMs === undefined ? {} : {executionMs: row.executionMs})});
  }
  if (earliest !== document.startedAtMs || latest !== document.finishedAtMs ||
      document.status !== (tasks.some(row => row.outcome === 'failed') ? 'failed' : 'unknown')) unavailable('invalid-native-invocation-bounds');
  let keyEvidence = 'missing-info-log';
  let mapping;
  let completion;
  if (infoText !== undefined) {
    const completions = typeof infoText === 'string' ? [...infoText.matchAll(/^BUILD (SUCCESSFUL|FAILED) in [^\r\n]+\r?$/gm)] : [];
    if (invocationCount !== 1 || completions.length !== 1) keyEvidence = 'info-log-not-one-completed-invocation';
    else if (!Number.isFinite(infoMtimeMs) || infoMtimeMs < document.finishedAtMs - 5000 || infoMtimeMs > now + 5000) keyEvidence = 'info-log-outside-invocation-window';
    else if (completions[0][1] === 'SUCCESSFUL' && document.status === 'failed') keyEvidence = 'info-log-outcome-conflict';
    else {
      mapping = parseGradleInfo(infoText, {gradleVersion: document.gradleVersion});
      keyEvidence = mapping.reason ?? 'native-info-key-correlation';
      if (!mapping.reason) completion = completions[0][1];
    }
  }
  for (let index = 0; index < tasks.length; index++) {
    const row = document.tasks[index];
    const key = mapping && !mapping.reason ? mapping.keys.get(row.taskPath) : undefined;
    if (row.outcome === 'fromCache') tasks[index].cacheEligibility = 'eligible';
    else if (row.outcome === 'executed' && key) tasks[index].cacheEligibility = 'eligible';
    else if (row.outcome === 'executed' && mapping && !mapping.reason && mapping.ineligiblePaths.has(row.taskPath)) tasks[index].cacheEligibility = 'ineligible';
    // A key is evidence of cache eligibility only for successful execution or
    // an accepted native cache result. Other outcomes retain their own units.
    if (key && (row.outcome === 'executed' || row.outcome === 'fromCache')) tasks[index].nativeKey = key;
  }
  const body = {version: 1, invocationId: 'run-' + document.invocationId.slice('gradle-'.length), startedAt: iso(document.startedAtMs), finishedAt: iso(document.finishedAtMs),
    status: document.status === 'failed' || completion === 'FAILED' ? 'failed' : completion === 'SUCCESSFUL' ? 'success' : 'unknown', tasks};
  if (Buffer.byteLength(JSON.stringify(body)) > maximumReportBytes) unavailable('native-report-too-large');
  return {body, keyEvidence};
}

export function readGradleReports(directory, {project, compatibility, infoLogPath, now = Date.now()} = {}) {
  const directoryInfo = lstatSync(directory);
  if (!directoryInfo.isDirectory() || directoryInfo.isSymbolicLink() || (directoryInfo.mode & 0o077) !== 0 ||
      typeof process.getuid === 'function' && directoryInfo.uid !== process.getuid()) unavailable('unsafe-native-report-directory');
  const files = readdirSync(directory).filter(name => /^gradle-[0-9a-f-]{36}\.json$/.test(name)).sort();
  if (files.length > 32) unavailable('too-many-native-invocations');
  let total = 0;
  const documents = files.map(name => {
    const file = readBoundedFile(join(directory, name), maximumReportBytes, {privateFile: true});
    total += file.bytes;
    if (total > maximumBatchBytes) unavailable('native-report-batch-too-large');
    const document = JSON.parse(file.text);
    if (document.invocationId + '.json' !== name) unavailable('native-report-identity-mismatch');
    return document;
  });
  let info;
  if (infoLogPath) {
    try {info = readBoundedFile(infoLogPath, maximumInfoBytes);} catch { /* Missing optional key evidence leaves outcomes intact. */ }
  }
  return documents.map(document => projectGradleInvocation(document, {project, compatibility, now, invocationCount: documents.length,
    ...(info ? {infoText: info.text, infoMtimeMs: info.mtimeMs} : {})}));
}
