import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, realpathSync, statSync, utimesSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { assertAuthority, configuration } from './run.mjs';

const maxBytes = 64 * 1024 * 1024;
const fixtureFiles = [
  'CacheProbe.xcodeproj/project.pbxproj',
  'CacheProbe.xcodeproj/project.xcworkspace/contents.xcworkspacedata',
  'CacheProbe.xcodeproj/xcshareddata/xcschemes/CacheProbe.xcscheme',
  'Source/main.swift',
];
const sha256 = value => createHash('sha256').update(value).digest('hex');
const check = (condition, code) => { if (!condition) throw new Error(code); };
const ms = started => Math.round(performance.now() - started);

function command(program, args, cwd, code) {
  try {
    // Never pass the scoped cache credential or OIDC request data to build tools.
    const { HOME, PATH, TMPDIR, DEVELOPER_DIR } = process.env;
    return execFileSync(program, args, { cwd, env: { HOME, PATH, TMPDIR, DEVELOPER_DIR },
      encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 60_000, maxBuffer: 4 << 20 });
  } catch { throw new Error(code); }
}

function toolchain(cfg, env) {
  check(process.platform === 'darwin' && process.arch === 'arm64', 'wrong-xcode-runtime');
  const run = (program, args) => command(program, args, env.GITHUB_WORKSPACE, 'toolchain-probe-failed').trim();
  const fixture = join(env.GITHUB_WORKSPACE, 'qa/r2-native-oidc/xcode-fixture');
  const inputs = fixtureFiles.map(name => [name, sha256(readFileSync(join(fixture, name)))]);
  const xcode = run('xcodebuild', ['-version']);
  const sdk = run('xcrun', ['--sdk', 'macosx', '--show-sdk-build-version']);
  const swift = run('xcrun', ['--sdk', 'macosx', 'swiftc', '--version']);
  check(/^Xcode \d[\d.]*\nBuild version [A-Za-z0-9]+$/.test(xcode) && /^[A-Za-z0-9]+$/.test(sdk) && swift,
    'invalid-xcode-toolchain');
  const layout = join(realpathSync(dirname(cfg.workspace)), '.layercache-r2-qa');
  const compatibility = `xcode-derived-r2-v1-${sha256(JSON.stringify({ inputs, xcode, sdk, swift, layout,
    architecture: 'arm64', configuration: 'Release', sdkName: 'macosx' }))}`;
  return { fixture, xcode, sdk, compatibility };
}

export function prepareXcode(cfg, env) {
  const git = command('git', ['rev-parse', 'HEAD'], env.GITHUB_WORKSPACE, 'checkout-source-probe-failed').trim();
  check(git === env.GITHUB_SHA, 'checkout-source-mismatch');
  check(!existsSync(cfg.workspace) && !existsSync(cfg.output), 'owned-scratch-already-exists');
  const { fixture, compatibility } = toolchain(cfg, env);
  mkdirSync(cfg.workspace, { mode: 0o700 });
  for (const name of fixtureFiles) {
    const target = join(cfg.workspace, name);
    mkdirSync(dirname(target), { recursive: true, mode: 0o700 });
    writeFileSync(target, readFileSync(join(fixture, name)), { mode: 0o600, flag: 'wx' });
    // Match the mtime of unchanged inputs across independent GitHub checkouts.
    utimesSync(target, 1700000000, 1700000000);
  }
  writeFileSync(env.GITHUB_OUTPUT, `compatibility=${compatibility}\n`, { flag: 'a', mode: 0o600 });
}

export async function readVerifiedArchive(response) {
  const digest = response.headers.get('x-layercache-digest') || '';
  const length = Number(response.headers.get('content-length'));
  if (response.status !== 200 || !/^sha256:[a-f0-9]{64}$/.test(digest) ||
      !Number.isSafeInteger(length) || length < 1 || length > maxBytes || !response.body) {
    await response.body?.cancel();
    throw new Error('invalid-xcode-archive-response');
  }
  const result = Buffer.alloc(length);
  let offset = 0;
  for await (const chunk of response.body) {
    check(offset + chunk.length <= length, 'xcode-archive-length-mismatch');
    result.set(chunk, offset);
    offset += chunk.length;
  }
  check(offset === length, 'xcode-archive-length-mismatch');
  check(`sha256:${sha256(result)}` === digest, 'xcode-archive-digest-mismatch');
  return result;
}

function measures(cfg) {
  const buildDir = join(cfg.workspace, 'DerivedData');
  const binary = join(buildDir, 'Build/Products/Release/CacheProbe');
  const outputReceipt = join(buildDir, 'qualification.json');
  const build = (expected) => {
    const started = performance.now();
    const log = command('xcodebuild', ['-project', 'CacheProbe.xcodeproj', '-scheme', 'CacheProbe',
      '-configuration', 'Release', '-destination', 'platform=macOS,arch=arm64', '-jobs', '2',
      '-derivedDataPath', buildDir, 'COMPILATION_CACHE_ENABLE_CACHING=NO', 'build'], cfg.workspace, 'xcode-build-failed');
    const buildMs = ms(started);
    const output = command(binary, [], cfg.workspace, 'fixture-executable-failed').trim();
    check(output === expected, 'fixture-executable-output-mismatch');
    const compileEntries = (log.match(/^SwiftCompile /gm) || []).length;
    return { buildMs, compileEntries };
  };
  const binaryIdentity = () => ({ sha256: sha256(readFileSync(binary)), mtimeNs: String(statSync(binary, { bigint: true }).mtimeNs) });
  return { buildDir, binaryIdentity, outputReceipt, build };
}

export async function qualifyXcode(cfg, env, request = fetch) {
  check(env.TURBO_API === cfg.origin && env.TURBO_TEAM === cfg.project && env.TURBO_TOKEN,
    'pinned-action-environment-required');
  const { xcode, sdk, compatibility } = toolchain(cfg, env);
  assertAuthority(env.TURBO_TOKEN, cfg, env, compatibility);
  const key = sha256(`xcode-derived-r2-v1\0${cfg.profile}`);
  const archiveURL = new URL(`/v8/artifacts/${key}`, cfg.origin);
  const send = (method, body) => request(archiveURL, { method, body, redirect: 'error',
    headers: { Authorization: `Bearer ${env.TURBO_TOKEN}` }, signal: AbortSignal.timeout(45_000) });
  const archive = join(cfg.workspace, 'fixture.tgz');
  const { buildDir, binaryIdentity, outputReceipt, build } = measures(cfg);
  check(!existsSync(buildDir) && !existsSync(archive), 'xcode-fixture-not-empty');
  const evidence = { schemaVersion: 'r2-xcode-archive-v1', passed: false, mode: cfg.mode,
    sourceRevision: env.GITHUB_SHA, xcode, sdk, architecture: 'arm64', compatibility };
  const started = performance.now();
  try {
    if (cfg.mode === 'producer') {
      const cold = await send('GET');
      await cold.body?.cancel();
      check(cold.status === 404, 'xcode-archive-key-not-cold');
      evidence.cold = build('42');
      check(evidence.cold.compileEntries > 0, 'xcode-cold-compile-missing');
      evidence.binary = binaryIdentity();
      writeFileSync(outputReceipt, JSON.stringify(evidence.binary), { mode: 0o600, flag: 'wx' });
      const packStart = performance.now();
      command('/usr/bin/tar', ['--format', 'pax', '-czf', archive, 'DerivedData'], cfg.workspace, 'xcode-pack-failed');
      evidence.packMs = ms(packStart);
      const bytes = statSync(archive).size;
      check(bytes > 0 && bytes <= maxBytes, 'xcode-archive-over-budget');
      const packed = readFileSync(archive);
      evidence.archive = { bytes, sha256: sha256(packed) };
      const transferStart = performance.now();
      const published = await send('PUT', packed);
      await published.body?.cancel();
      check(published.status === 200, 'hosted-xcode-publication-failed');
      evidence.uploadMs = ms(transferStart);
    } else {
      const transferStart = performance.now();
      const packed = await readVerifiedArchive(await send('GET'));
      evidence.downloadMs = ms(transferStart);
      evidence.archive = { bytes: packed.length, sha256: sha256(packed) };
      writeFileSync(archive, packed, { mode: 0o600, flag: 'wx' });
      const restoreStart = performance.now();
      command('/usr/bin/tar', ['-xzf', archive, '--', 'DerivedData'], cfg.workspace, 'xcode-extraction-failed');
      evidence.restoreMs = ms(restoreStart);
      const saved = JSON.parse(readFileSync(outputReceipt, 'utf8'));
      evidence.binary = binaryIdentity();
      check(saved.sha256 === evidence.binary.sha256 && saved.mtimeNs === evidence.binary.mtimeNs,
        'xcode-restored-executable-differs');
      evidence.warm = build('42');
      check(evidence.warm.compileEntries === 0 && binaryIdentity().sha256 === evidence.binary.sha256,
        'xcode-restore-did-not-skip-compile');
      writeFileSync(join(cfg.workspace, 'Source/main.swift'), 'print(43)\n');
      evidence.changed = build('43');
      check(evidence.changed.compileEntries > 0, 'xcode-input-edit-not-recompiled');
      const denied = await send('PUT', 'read-only control');
      await denied.body?.cancel();
      check(denied.status === 401, 'read-only-put-was-not-denied');
      evidence.deniedReadOnlyPUTStatus = denied.status;
    }
    evidence.passed = true;
  } finally {
    evidence.commandMs = ms(started);
    writeFileSync(cfg.output, JSON.stringify(evidence, null, 2) + '\n', { mode: 0o600, flag: 'wx' });
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const cfg = configuration(process.env);
    if (process.argv[2] === 'prepare') prepareXcode(cfg, process.env);
    else if (process.argv[2] === 'run') await qualifyXcode(cfg, process.env);
    else throw new Error('unknown-qualification-command');
    console.log('Xcode archive qualification step passed. Only the allowlisted summary may be published.');
  } catch (error) {
    const reason = /^[a-z][a-z0-9-]{1,100}$/.test(error.message || '') ? error.message : 'qualification-failed';
    console.error(`Xcode archive qualification failed: ${reason}`);
    process.exitCode = 1;
  }
}
