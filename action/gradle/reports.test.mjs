import test from 'node:test';
import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {execFileSync} from 'node:child_process';
import {chmodSync, linkSync, mkdtempSync, readFileSync, rmSync, symlinkSync, utimesSync, writeFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {projectGradleInvocation, readBoundedFile, readGradleReports} from './reports.mjs';

const project = 'synthetic-project', compatibility = 'synthetic-gradle931';
const nativeKey = 'a'.repeat(32);
const nonce = '11111111-1111-4111-8111-111111111111';
function fixture(states = ['executed', 'fromCache']) {
  const now = Date.now(), startedAtMs = now - 1000, finishedAtMs = now - 500;
  const tasks = states.map((outcome, index) => {
    const taskPath = `:private-${index}:task`;
    const workRef = 'sha256:' + createHash('sha256').update(`layercache-gradle-task-v1\0${project}\0${compatibility}\0${taskPath}`).digest('hex');
    return {taskPath, workRef, outcome, startedAtMs, finishedAtMs, ...(outcome === 'executed' ? {executionMs: 500} : {})};
  });
  const document = {version: 1, invocationId: 'gradle-' + nonce, gradleVersion: '9.3.1', startedAtMs, finishedAtMs,
    status: states.includes('failed') ? 'failed' : 'unknown', truncated: false, tasks};
  const infoText = tasks.map((task, index) => `Build cache key for task '${task.taskPath}' is ${String(index).padStart(32, 'a')}\n`).join('') + `BUILD ${states.includes('failed') ? 'FAILED' : 'SUCCESSFUL'} in 1s\n`;
  return {document, options: {project, compatibility, now, infoText, infoMtimeMs: now}};
}

test('projects native completed outcomes and keys without uploading paths or unrelated data', () => {
  const {document, options} = fixture();
  document.privateEnvironment = {TOKEN: 'never-upload-this'};
  document.tasks[0].privateInput = 'never-upload-this-either';
  options.infoText += 'private log contains bearer secret and an absolute path\n';
  const {body, keyEvidence} = projectGradleInvocation(document, options);
  assert.equal(body.invocationId, 'run-' + nonce);
  assert.equal(body.status, 'success');
  assert.equal(body.tasks.length, 2);
  assert.equal(body.tasks[0].executionMs, 500);
  assert.equal(body.tasks[1].executionMs, undefined);
  assert.ok(body.tasks.every(task => /^[a-f0-9]{32}$/.test(task.nativeKey)));
  assert.equal(keyEvidence, 'native-info-key-correlation');
  for (const text of ['private-', 'taskPath', 'privateEnvironment', 'privateInput', 'never-upload', 'bearer secret', 'producer', 'source']) assert.equal(JSON.stringify(body).includes(text), false);
  assert.deepEqual(projectGradleInvocation(document, options).body, body, 'Exact final retry bytes stay stable');
});

test('missing or unsupported key evidence preserves accepted outcomes without a false denominator', () => {
  for (const change of [options => {delete options.infoText;}, options => {options.invocationCount = 2;}, options => {options.infoMtimeMs -= 60000;}, options => {options.infoText = options.infoText.replace(/BUILD SUCCESSFUL.*\n/, '');}]) {
    const {document, options} = fixture();change(options);
    const {body, keyEvidence} = projectGradleInvocation(document, options);
    assert.equal(body.tasks[1].outcome, 'fromCache');
    assert.equal(body.tasks[1].cacheEligibility, 'eligible');
    assert.ok(body.tasks.every(task => task.nativeKey === undefined));
    assert.notEqual(keyEvidence, 'native-info-key-correlation');
    assert.equal(body.eligible, undefined);
  }
  const {document, options} = fixture();document.gradleVersion = '9.4.1';
  assert.ok(projectGradleInvocation(document, options).body.tasks.every(task => task.nativeKey === undefined));
});

test('up-to-date skipped no-source and failed tasks never become eligible misses', () => {
  const {document, options} = fixture(['fromCache', 'upToDate', 'noSource', 'skipped', 'failed', 'unknown']);
  const {body} = projectGradleInvocation(document, options);
  assert.equal(body.status, 'failed');
  assert.equal(body.tasks[0].nativeKey?.length, 32);
  assert.ok(body.tasks.slice(1).every(task => task.nativeKey === undefined && task.executionMs === undefined));
});

test('shared native keys preserve exact per-task mappings and leave producer ambiguity to the server', () => {
  const {document, options} = fixture();
  options.infoText = document.tasks.map(task => `Build cache key for task '${task.taskPath}' is ${nativeKey}\n`).join('') + 'BUILD SUCCESSFUL in 1s\n';
  assert.ok(projectGradleInvocation(document, options).body.tasks.every(task => task.nativeKey === nativeKey && task.cacheEligibility === 'eligible'));
});

test('explicit native disabled output distinguishes ineligible execution from missing evidence', () => {
  const {document, options} = fixture();
  options.infoText = `Caching disabled for task '${document.tasks[0].taskPath}' because:\n  Native reason remains local\nBUILD SUCCESSFUL in 1s\n`;
  const result = projectGradleInvocation(document, options);
  assert.equal(result.body.tasks[0].cacheEligibility, 'ineligible');
  assert.equal(result.body.tasks[0].nativeKey, undefined);
  assert.equal(result.body.tasks[1].cacheEligibility, 'eligible');
  assert.equal(result.body.tasks[1].nativeKey, undefined);
});

test('unrecognized and truncated native key messages cannot establish eligibility', () => {
  for (const suffix of ['Build cache key for task', "Build cache key for task ':other' is not-a-native-key\n"]) {
    const {document, options} = fixture();options.infoText += suffix;
    const result = projectGradleInvocation(document, options);
    assert.ok(result.body.tasks.every(task => task.nativeKey === undefined));
  }
});

test('invalid scope identity bounds duplicate task refs and partial collections are rejected', () => {
  const changes = [
    document => {document.truncated = true;},
    document => {document.tasks[1].workRef = document.tasks[0].workRef;},
    document => {document.tasks[0].taskPath = ':different';},
    document => {document.startedAtMs++;},
    document => {document.tasks[0].finishedAtMs = document.finishedAtMs + 1;},
    document => {document.tasks[0].executionMs = 999999;},
    document => {document.tasks[1].executionMs = 1;},
    document => {document.tasks = [];},
  ];
  for (const change of changes) {const {document, options} = fixture();change(document);assert.throws(() => projectGradleInvocation(document, options));}
  const {document, options} = fixture();options.project = 'another-project';assert.throws(() => projectGradleInvocation(document, options));
});

test('private report reads reject symlinks hardlinks broad permissions and oversized files', () => {
  const root = mkdtempSync(join(tmpdir(), 'layercache-gradle-report-files-'));
  try {
    const path = join(root, 'report');writeFileSync(path, 'safe', {mode: 0o600});
    assert.equal(readBoundedFile(path, 4, {privateFile: true}).text, 'safe');
    assert.throws(() => readBoundedFile(path, 3, {privateFile: true}));
    const symlink = join(root, 'symlink');symlinkSync(path, symlink);assert.throws(() => readBoundedFile(symlink, 4));
    const hardlink = join(root, 'hardlink');linkSync(path, hardlink);assert.throws(() => readBoundedFile(path, 4));rmSync(hardlink);
    chmodSync(path, 0o644);assert.throws(() => readBoundedFile(path, 4, {privateFile: true}));
    if (process.platform !== 'win32') {
      const fifo = join(root, 'fifo');execFileSync('mkfifo', [fifo]);
      assert.throws(() => readBoundedFile(fifo, 4), /unsafe-native-report-file/);
    }
  } finally {rmSync(root, {recursive: true, force: true});}
});

test('one private invocation and its explicit complete log produce the exact wire projection', () => {
  const root = mkdtempSync(join(tmpdir(), 'layercache-gradle-report-projection-'));
  try {
    chmodSync(root, 0o700);
    const {document, options} = fixture();
    const report = join(root, document.invocationId + '.json'), info = join(root, 'native-info.log');
    writeFileSync(report, JSON.stringify(document), {mode: 0o600});writeFileSync(info, options.infoText, {mode: 0o600});
    utimesSync(info, options.now / 1000, options.now / 1000);
    const [result] = readGradleReports(root, {project, compatibility, infoLogPath: info, now: options.now});
    assert.deepEqual(result.body, projectGradleInvocation(document, options).body);
    const copy = structuredClone(document);copy.invocationId = 'gradle-22222222-2222-4222-8222-222222222222';
    writeFileSync(join(root, copy.invocationId + '.json'), JSON.stringify(copy), {mode: 0o600});
    const mixed = readGradleReports(root, {project, compatibility, infoLogPath: info, now: options.now});
    assert.equal(mixed.length, 2);assert.ok(mixed.flatMap(item => item.body.tasks).every(task => task.nativeKey === undefined));
    assert.equal(readFileSync(report, 'utf8'), JSON.stringify(document), 'Projection preserves original private evidence');
  } finally {rmSync(root, {recursive: true, force: true});}
});
