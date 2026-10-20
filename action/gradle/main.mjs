import { chmodSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, isAbsolute, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { endpointURL, exchangeCapability, fileCommand, fileCommands } from '../setup/auth.ts';

const actionRoot = dirname(fileURLToPath(import.meta.url));
const defaultLog = value => { process.stdout.write(value); };
const emptyConnection = { LAYER_CACHE_GRADLE_URL: '', LAYER_CACHE_GRADLE_PUSH: '0', LAYER_CACHE_GRADLE_TOKEN: '',
  LAYER_CACHE_GRADLE_REPORT_DIR: '', LAYER_CACHE_GRADLE_REPORT_PROJECT: '', LAYER_CACHE_GRADLE_REPORT_COMPATIBILITY: '' };
const emptyOutputs = { configured: 'false', 'init-script': '', 'expires-at': '' };
const trustedEvents = new Set(['push', 'workflow_dispatch', 'schedule']);
const singleLine = value => typeof value === 'string' && value.length > 0 && !/[\x00-\x20\x7f]/.test(value);

function connectionInputs(env) {
  const input = name => env[`INPUT_${name.toUpperCase()}`] || '';
  const endpoint = input('team-url');
  let url;
  try { url = endpointURL(endpoint); }
  catch { throw new Error('team-url must be an HTTPS or loopback HTTP URL without credentials, query or fragment'); }
  if (!singleLine(endpoint) || url.port === '0') throw new Error('team-url must be a single valid endpoint');
  const project = input('project');
  if (!singleLine(project) || project.length > 256) throw new Error('project must be an explicit identity without whitespace or control characters');
  const compatibility = input('compatibility');
  if (!/^[a-z0-9][a-z0-9_.:+@-]{0,255}$/.test(compatibility)) throw new Error('compatibility must be an explicit canonical full-context profile');
  const push = input('push') || 'false';
  if (!['true', 'false'].includes(push)) throw new Error('push must be true or false');
  const minutes = Number(input('ttl-minutes') || '60');
  if (!Number.isInteger(minutes) || minutes < 1 || minutes > 60) throw new Error('ttl-minutes must be an integer from 1 to 60');
  const infoLog = input('native-info-log');
  if (infoLog && (!isAbsolute(infoLog) || infoLog.length > 4096 || /[\x00-\x1f\x7f]/.test(infoLog))) throw new Error('native-info-log must be an explicit absolute single-line path');
  if (!/^[a-zA-Z0-9_.-]+\/[a-zA-Z0-9_.-]+$/.test(env.GITHUB_REPOSITORY || '')) throw new Error('GitHub repository identity is required');
  for (const key of ['RUNNER_TEMP', 'GITHUB_ENV', 'GITHUB_OUTPUT', 'GITHUB_STATE']) {
    if (!env[key] || /[\x00-\x1f\x7f]/.test(env[key]) || !isAbsolute(env[key])) throw new Error(`${key} must be an absolute single-line runner path`);
  }
  return { endpoint, url, project, compatibility, push: push === 'true', minutes, infoLog };
}

export function capabilityScope(result, inputs, env) {
  if (result.teamToken.length > 32 * 1024 || !/^lc2\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+$/.test(result.teamToken)) throw new Error('Invalid Gradle capability');
  const parts = result.teamToken.split('.');
  const claims = JSON.parse(Buffer.from(parts[1], 'base64url').toString('utf8'));
  // The capability arrived directly from the authenticated endpoint. This
  // inspects its scope, not its signature; the gateway independently verifies
  // every native request and current membership.
  if (claims.project !== inputs.project || claims.integration !== 'gradle' ||
      claims.compatibility !== inputs.compatibility ||
      claims.repository !== env.GITHUB_REPOSITORY.toLowerCase() ||
      !Array.isArray(claims.capabilities) ||
      !claims.capabilities.some(value => ['read', 'write', 'admin'].includes(value)) ||
      Date.parse(claims.expiresAt) !== Date.parse(result.expiresAt)) throw new Error('Wrong Gradle capability scope');
  return inputs.push && trustedEvents.has(env.GITHUB_EVENT_NAME) &&
    claims.ref === env.GITHUB_REF && singleLine(claims.defaultRef) && claims.ref === claims.defaultRef &&
    claims.capabilities.some(value => value === 'write' || value === 'admin');
}

export async function setupGradle(env = process.env, { fetcher = fetch, log = defaultLog } = {}) {
  Object.assign(env, emptyConnection);
  fileCommands(env.GITHUB_ENV, emptyConnection);
  fileCommands(env.GITHUB_OUTPUT, emptyOutputs);
  const inputs = connectionInputs(env);
  let result;
  let writable;
  try {
    result = await exchangeCapability({ endpoint: inputs.endpoint, project: inputs.project,
      compatibility: inputs.compatibility, minutes: inputs.minutes, env, integration: 'gradle', fetcher, log });
    writable = capabilityScope(result, inputs, env);
  } catch {
    log('::warning::Layer Cache Gradle authentication unavailable; continuing without its HTTP cache. Check id-token permission and the private project.\n');
    return { configured: false };
  }
  const root = mkdtempSync(join(realpathSync(env.RUNNER_TEMP), 'layercache-gradle-job-'));
  chmodSync(root, 0o700);
  const initScript = join(root, 'init.gradle');
  try {
    writeFileSync(initScript, readFileSync(join(actionRoot, '../../native/gradle/init.gradle')), { mode: 0o600, flag: 'wx' });
    fileCommand(env.GITHUB_STATE, 'gradle_root', root);
    const url = inputs.url;
    url.pathname = `${url.pathname.replace(/\/+$/, '').replace(/\/v1$/, '')}/v1/gradle-cache/`;
    const environment = { LAYER_CACHE_GRADLE_URL: url.href, LAYER_CACHE_GRADLE_PUSH: writable ? '1' : '0', LAYER_CACHE_GRADLE_TOKEN: result.teamToken };
    const metering = result.nativeReports?.version === 1 && result.nativeReports.path === '/v1/reports/native';
    let reportDirectory;
    if (metering) {
      reportDirectory = join(root, 'reports');
      mkdirSync(reportDirectory, {mode: 0o700});
      Object.assign(environment, {LAYER_CACHE_GRADLE_REPORT_DIR: reportDirectory,
        LAYER_CACHE_GRADLE_REPORT_PROJECT: inputs.project, LAYER_CACHE_GRADLE_REPORT_COMPATIBILITY: inputs.compatibility});
      fileCommands(env.GITHUB_STATE, {gradle_metering: '1', gradle_endpoint: inputs.endpoint, gradle_project: inputs.project,
        gradle_compatibility: inputs.compatibility, gradle_info_log: inputs.infoLog});
    }
    fileCommands(env.GITHUB_ENV, environment);
    try {
      fileCommands(env.GITHUB_OUTPUT, { configured: 'true', 'init-script': initScript, 'expires-at': result.expiresAt });
    } catch (error) {
      fileCommands(env.GITHUB_ENV, emptyConnection);
      throw error;
    }
    Object.assign(env, environment);
    log(`Layer Cache Gradle connected with ${writable ? 'publication enabled' : 'read-only publication policy'}. Credentials expire at ${result.expiresAt}.\n`);
    return { configured: true, root, initScript, expiresAt: result.expiresAt, writable, ...(reportDirectory ? {reportDirectory} : {}) };
  } catch (error) {
    rmSync(root, { recursive: true, force: true });
    throw error;
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  setupGradle().catch(() => {
    process.stdout.write('::error::Layer Cache Gradle setup failed. Check explicit inputs and runner file permissions.\n');
    process.exitCode = 1;
  });
}
