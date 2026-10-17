import { randomUUID } from 'node:crypto';
import { execFile } from 'node:child_process';
import { isAbsolute, join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { promisify } from 'node:util';
import { endpointURL, fileCommands } from '../setup/auth.ts';
import { atomicJSON, downloadFile, evidence, exchangeRetained, expectedReceipt, invalidateOutput, producer,
  publishFile, readReceipt, RetainedError } from './client.mjs';

const emptyOutputs = { published: 'false', receipt: '', 'receipt-file': '', 'artifact-id': '', 'sha256': '', size: '', 'retain-until': '', 'timings-file': '' };
const defaultLog = value => process.stdout.write(value);
const producerKeys = ['project', 'repository', 'repositoryId', 'sourceCommit', 'workflowRef', 'runId', 'runAttempt', 'jobId', 'profile'];
const execute = promisify(execFile);

async function selectedSource(env, expected) {
  if (!env.GITHUB_WORKSPACE || !isAbsolute(env.GITHUB_WORKSPACE) || /[\x00-\x1f\x7f]/.test(env.GITHUB_WORKSPACE)) throw new RetainedError('publish requires the checked-out GitHub workspace');
  const gitEnv = { ...process.env, ...env, GIT_OPTIONAL_LOCKS: '0' };
  for (const key of ['GIT_DIR', 'GIT_COMMON_DIR', 'GIT_WORK_TREE', 'GIT_OBJECT_DIRECTORY', 'GIT_ALTERNATE_OBJECT_DIRECTORIES']) delete gitEnv[key];
  let current;
  try { current = (await execute('git', ['-C', env.GITHUB_WORKSPACE, 'rev-parse', '--verify', 'HEAD'], { env: gitEnv, timeout: 10_000, maxBuffer: 1024, windowsHide: true })).stdout.trim(); }
  catch { throw new RetainedError('Cannot verify the checked-out producer source'); }
  if (current !== expected) throw new RetainedError('Checked-out source does not match the signed producer commit');
}

export async function runRetained(env = process.env, { fetcher = fetch, log = defaultLog } = {}) {
  const report = evidence();
  let receiptPath;
  let timingsPath;
  let artifactPath;
  let reading = false;
  try {
    fileCommands(env.GITHUB_OUTPUT, emptyOutputs);
    const setup = report.start();
    const input = name => env[`INPUT_${name.toUpperCase()}`] || '';
    const mode = input('mode');
    if (!['publish', 'read'].includes(mode)) throw new RetainedError('mode must be publish or read');
    for (const key of ['RUNNER_TEMP', 'GITHUB_OUTPUT']) {
      if (!env[key] || !isAbsolute(env[key]) || /[\x00-\x1f\x7f]/.test(env[key])) throw new RetainedError('Invalid runner output paths');
    }
    if (!input('path') || /[\x00-\x1f\x7f]/.test(input('path'))) throw new RetainedError('path must select one explicit file');
    const path = resolve(input('path'));
    artifactPath = path;
    reading = mode === 'read';
    receiptPath = resolve(input('receipt-output') || join(env.RUNNER_TEMP, `layercache-retained-receipt-${randomUUID()}.json`));
    timingsPath = resolve(input('timings-output') || join(env.RUNNER_TEMP, `layercache-retained-transfer-${randomUUID()}.json`));
    if (new Set([path, receiptPath, timingsPath, resolve(env.GITHUB_OUTPUT)]).size !== 4) throw new RetainedError('Artifact, receipt, timings and GitHub output paths must differ');
    // Invalidate caller-visible success before authentication, network or input
    // validation can fail. A failed download cannot leave an earlier artifact.
    await invalidateOutput(receiptPath);
    await invalidateOutput(timingsPath);
    if (mode === 'read') await invalidateOutput(path);
    endpointURL(input('team-url'));
    const project = input('project');
    const profile = input('profile');
    const name = input('name');
    if (!name || name.length > 512 || name.trim() !== name || /[\x00-\x1f\x7f]/.test(name)) throw new RetainedError('name must identify one immutable output');
    let expected;
    if (mode === 'read') {
      const inline = input('receipt');
      const file = input('receipt-file');
      if (Boolean(inline) === Boolean(file) || inline.length > 64 * 1024) throw new RetainedError('read requires exactly one bounded receipt or receipt-file');
      const identity = producer({ project, profile, repository: input('expected-repository'), repositoryId: input('expected-repository-id'),
        sourceCommit: input('expected-source'), workflowRef: input('expected-workflow-ref'), runId: input('expected-run-id'),
        runAttempt: input('expected-run-attempt'), jobId: input('expected-job-id') });
      expected = expectedReceipt(inline ? JSON.parse(inline) : await readReceipt(file), { producer: identity, name });
      if (input('retain-until')) throw new RetainedError('read uses the immutable receipt deadline');
    } else if (!input('retain-until')) throw new RetainedError('publish requires an explicit immutable retain-until deadline');
    report.phase('client-setup', setup);
    const authorized = await exchangeRetained({ endpoint: input('team-url'), project, profile, mode, env, fetcher, log, evidence: report });
    let result;
    if (mode === 'publish') {
      const sourceCheck = report.start();
      await selectedSource(env, authorized.current.sourceCommit);
      report.phase('checked-out-source', sourceCheck);
      result = await publishFile({ endpoint: input('team-url'), token: authorized.token, producer: authorized.current, name,
        path, retainUntil: input('retain-until'), fetcher, evidence: report });
    } else {
      const origin = expected.declaration.producer;
      if (!producerKeys.filter(key => key !== 'jobId' && key !== 'runAttempt').every(key => origin[key] === authorized.current[key]) ||
          BigInt(origin.runAttempt) > BigInt(authorized.current.runAttempt) ||
          origin.jobId === authorized.current.jobId) throw new RetainedError('read requires another job in the same run and a current or earlier explicit producer attempt');
      result = await downloadFile({ endpoint: input('team-url'), token: authorized.token, path, receipt: expected,
        expected: { producer: origin, name }, fetcher, evidence: report });
    }
    await atomicJSON(receiptPath, result);
    await atomicJSON(timingsPath, report.json());
    try {
      fileCommands(env.GITHUB_OUTPUT, { published: 'true', receipt: JSON.stringify(result), 'receipt-file': receiptPath,
        'artifact-id': result.id, sha256: result.declaration.digest, size: String(result.declaration.size),
        'retain-until': result.declaration.retainUntil, 'timings-file': timingsPath });
    } catch (error) { await invalidateOutput(receiptPath); throw error; }
    log(`Layer Cache retained ${mode} verified ${result.declaration.size} bytes; deadline ${result.declaration.retainUntil}.\n`);
    return { receipt: result, receiptPath, timingsPath };
  } catch (error) {
    report.outcome = 'failed';
    if (Number.isInteger(error.status)) report.httpStatus = error.status;
    if (receiptPath) await invalidateOutput(receiptPath).catch(() => {});
    if (reading && artifactPath) await invalidateOutput(artifactPath).catch(() => {});
    if (timingsPath) {
      await atomicJSON(timingsPath, report.json()).catch(() => {});
      try { fileCommands(env.GITHUB_OUTPUT, { ...emptyOutputs, 'timings-file': timingsPath }); } catch { /* The action still fails. */ }
    }
    throw error;
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  runRetained().catch(() => {
    process.stdout.write('::error::Layer Cache retained transfer failed. Required transfer is incomplete; inspect private phase evidence and retain the qualified provider.\n');
    process.exitCode = 1;
  });
}
