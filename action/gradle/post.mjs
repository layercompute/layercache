import { existsSync, lstatSync, realpathSync, rmSync } from 'node:fs';
import { basename, dirname, isAbsolute, join } from 'node:path';
import { pathToFileURL } from 'node:url';
import {exchangeCapability, endpointURL} from '../setup/auth.ts';
import {capabilityScope} from './main.mjs';
import {readGradleReports} from './reports.mjs';

function ownedRoot(env) {
  const root = env.STATE_gradle_root;
  if (!root || !existsSync(root)) return;
  if (!lstatSync(root).isDirectory() || lstatSync(root).isSymbolicLink()) throw new Error('Refusing Gradle cleanup outside its owned job directory');
  if (!env.RUNNER_TEMP || dirname(realpathSync(root)) !== realpathSync(env.RUNNER_TEMP) ||
      !basename(root).startsWith('layercache-gradle-job-')) throw new Error('Refusing Gradle cleanup outside its owned job directory');
  return root;
}
export function cleanupGradle(env = process.env) {
  const root = ownedRoot(env);
  if (!root) return;
  rmSync(root, { recursive: true });
}

async function confirmedReceipt(response, report) {
  if (response.status !== 200 || !response.body) {
    await response.body?.cancel();
    throw new Error('Native report not confirmed');
  }
  const reader = response.body.getReader();
  const chunks = [];
  let bytes = 0;
  try {
    for (;;) {
      const {done, value} = await reader.read();
      if (done) break;
      bytes += value.byteLength;
      if (bytes > 16 * 1024) throw new Error('Native report receipt exceeds its bound');
      chunks.push(value);
    }
    const receipt = JSON.parse(Buffer.concat(chunks, bytes).toString('utf8'));
    if (!receipt || typeof receipt !== 'object' || Array.isArray(receipt) || receipt.version !== 1 || receipt.recorded !== true ||
        receipt.invocationId !== report.invocationId || receipt.tasks !== report.tasks.length ||
        receipt.repeated !== undefined && typeof receipt.repeated !== 'boolean') throw new Error('Native report receipt does not match');
  } finally {
    await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}

export async function completeGradle(env = process.env, {fetcher = fetch, log = value => process.stdout.write(value)} = {}) {
  const root = ownedRoot(env);
  if (!root || env.STATE_gradle_metering !== '1') return {reported: 0};
  try {
    const directory = join(root, 'reports');
    if (dirname(realpathSync(directory)) !== realpathSync(root)) throw new Error('Unsafe native report directory');
    const project = env.STATE_gradle_project, compatibility = env.STATE_gradle_compatibility;
    const infoLogPath = env.STATE_gradle_info_log;
    if (infoLogPath && (!isAbsolute(infoLogPath) || /[\x00-\x1f\x7f]/.test(infoLogPath))) throw new Error('Unsafe native info log');
    const reports = readGradleReports(directory, {project, compatibility, infoLogPath});
    if (!reports.length) return {reported: 0};
    // Renew this action's own scope. A later setup action may have replaced the
    // job environment with credentials for a different project or profile.
    const endpoint = env.STATE_gradle_endpoint;
    const result = await exchangeCapability({endpoint, project, compatibility, minutes: 5, env, integration: 'gradle', fetcher, log, observationPhase: 'finish'});
    capabilityScope(result, {project, compatibility, push: false}, env);
    if (result.nativeReports?.version !== 1 || result.nativeReports.path !== '/v1/reports/native') return {reported: 0};
    const target = endpointURL(endpoint);
    target.pathname = target.pathname.replace(/\/+$/, '').replace(/\/v1$/, '') + '/v1/reports/native';
    let reported = 0;
    for (const {body} of reports) {
      const response = await fetcher(target, {method: 'POST', headers: {'Content-Type': 'application/json', Authorization: `Bearer ${result.teamToken}`},
        body: JSON.stringify(body), redirect: 'error', signal: AbortSignal.timeout(5000)});
      await confirmedReceipt(response, body);
      reported++;
    }
    log(`Layer Cache recorded ${reported} completed Gradle ${reported === 1 ? 'invocation' : 'invocations'}. Native keys and task execution were unchanged.\n`);
    return {reported};
  } catch {
    // This is optional evidence. Never print report contents, paths, server
    // bodies or exception messages, and never retry an uncertain commit here.
    log('::warning::Layer Cache Gradle metering was not confirmed; native outcomes and savings may remain unavailable.\n');
    return {reported: 0, unavailable: true};
  }
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {await completeGradle();} finally {cleanupGradle();}
}
