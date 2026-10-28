import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync, execFileSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync, existsSync, symlinkSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { tmpdir } from 'node:os';
import { createHash } from 'node:crypto';

test('installer verifies immutable version, attestations, checksum, and package before replacing a binary', () => {
  const root = mkdtempSync(join(tmpdir(), 'layercache-installer-qa-'));
  try {
    const bin = join(root, 'mock-bin');
    const release = join(root, 'release');
    const target = join(root, 'target');
    for (const directory of [bin, release, target]) mkdirSync(directory);
    const platform = `${process.platform}-${process.arch === 'x64' ? 'amd64' : process.arch}`;
    const artifact = `layercache-${platform}`;
    const archive = `${artifact}.tar.gz`;
    writeFileSync(join(release, artifact), '#!/bin/sh\ntest "$1" = help\n', { mode: 0o755 });
    execFileSync('tar', ['-czf', join(release, archive), '-C', release, artifact]);
    const digest = createHash('sha256').update(readFileSync(join(release, archive))).digest('hex');
    writeFileSync(join(release, `${archive}.sha256`), `${digest}  ${archive}\n`);
    const log = join(root, 'calls');
    writeFileSync(join(bin, 'gh'), `#!/usr/bin/env node
const fs = require('node:fs');
const path = require('node:path');
const args = process.argv.slice(2);
fs.appendFileSync(process.env.TEST_CALLS, JSON.stringify(args)+'\\n');
if (args[0] === 'api') {
  console.log(JSON.stringify(args[1].includes('releases/tags') ? {tag_name:'v0.1.0',draft:false,immutable:process.env.TEST_MODE !== 'mutable'} : {object:{type:'commit',sha:'a'.repeat(40)}}));
} else if (args[0] === 'release' && args[1] === 'download') {
  const destination = args[args.indexOf('--dir')+1];
  for(let i=0;i<args.length;i++) if(args[i] === '--pattern') fs.copyFileSync(path.join(process.env.TEST_RELEASE,args[i+1]),path.join(destination,args[i+1]));
} else if (args[0] === 'attestation' && args[1] === 'verify') {
  if (process.env.TEST_MODE === 'unattested') process.exit(1);
} else process.exit(1);
`, { mode: 0o755 });
    const run = (mode, version = 'v0.1.0') => spawnSync('bash', [fileURLToPath(new URL('install.sh', import.meta.url)), '--repository', 'acme/layercache', '--version', version, '--prefix', target], {
      encoding: 'utf8', env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, TEST_RELEASE: release, TEST_CALLS: log, TEST_MODE: mode },
    });
    assert.equal(run('normal').status, 0);
    assert.equal(existsSync(join(target, 'layercache')), true);
    const calls = readFileSync(log, 'utf8').trim().split('\n').map(JSON.parse);
    const verification = calls.filter(args => args[0] === 'attestation');
    assert.equal(verification.length, 2);
    for (const args of verification) {
      assert.ok(args.includes('--deny-self-hosted-runners'));
      assert.equal(args[args.indexOf('--source-ref') + 1], 'refs/tags/v0.1.0');
      assert.equal(args[args.indexOf('--source-digest') + 1], 'a'.repeat(40));
    }
    const original = readFileSync(join(target, 'layercache'), 'utf8');
    for (const mode of ['mutable', 'unattested']) {
      assert.notEqual(run(mode).status, 0);
      assert.equal(readFileSync(join(target, 'layercache'), 'utf8'), original);
    }
    assert.notEqual(run('normal', 'latest').status, 0);
    writeFileSync(join(release, `${archive}.sha256`), `${'0'.repeat(64)}  ${archive}\n`);
    assert.notEqual(run('normal').status, 0);
    writeFileSync(join(release, `${archive}.sha256`), `${digest}  ${archive}\n`);
    rmSync(join(target, 'layercache'));
    symlinkSync(join(release, artifact), join(target, 'layercache'));
    assert.notEqual(run('normal').status, 0);
  } finally { rmSync(root, { recursive: true }); }
});

test('transferred beta provenance retains only the exact historical source and signer', () => {
  const root = mkdtempSync(join(tmpdir(), 'layercache-provenance-qa-'));
  try {
    const log = join(root, 'calls');
    const subject = join(root, 'artifact');
    writeFileSync(subject, 'fixture bytes');
    writeFileSync(join(root, 'gh'), `#!/usr/bin/env node
require('node:fs').appendFileSync(process.env.TEST_CALLS, JSON.stringify(process.argv.slice(2))+'\\n');
if (process.env.TEST_FAIL === 'true') process.exit(1);
`, { mode: 0o755 });
    const source = 'cb254e9af694f6fe25f5a0ad416456174bda3e44';
    const helper = fileURLToPath(new URL('verify-release-provenance.sh', import.meta.url));
    const run = (repository, version, commit, fail = false) => {
      writeFileSync(log, '');
      const result = spawnSync('bash', [helper, repository, version, commit, subject], {
        encoding: 'utf8', env: { ...process.env, PATH: `${root}:${process.env.PATH}`, TEST_CALLS: log, TEST_FAIL: String(fail) },
      });
      const calls = readFileSync(log, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse);
      return { ...result, calls };
    };
    const historical = run('layercompute/layercache', 'v0.1.0-beta.9', source);
    assert.equal(historical.status, 0, historical.stderr);
    assert.equal(historical.calls.length, 1);
    const args = historical.calls[0];
    for (const [flag, expected] of [
      ['--repo', 'ziahamza/layercache'],
      ['--signer-workflow', 'ziahamza/layercache/.github/workflows/release.yml'],
      ['--source-ref', 'refs/tags/v0.1.0-beta.9'],
      ['--source-digest', source],
    ]) assert.equal(args[args.indexOf(flag) + 1], expected);
    assert.ok(args.includes('--deny-self-hosted-runners'));
    const bundles = readFileSync(args[args.indexOf('--bundle') + 1], 'utf8').trim().split('\n').map(JSON.parse);
    assert.equal(bundles.length, 3);
    for (const bundle of bundles) {
      const statement = JSON.parse(Buffer.from(bundle.dsseEnvelope.payload, 'base64'));
      assert.equal(statement.predicateType, 'https://slsa.dev/provenance/v1');
      assert.equal(statement.subject.length, 3);
      assert.ok(bundle.verificationMaterial.certificate.rawBytes);
      assert.ok(bundle.verificationMaterial.tlogEntries.length);
    }
    const movedTag = run('layercompute/layercache', 'v0.1.0-beta.9', 'a'.repeat(40));
    assert.notEqual(movedTag.status, 0);
    assert.deepEqual(movedTag.calls, []);
    for (const [repository, version] of [
      ['layercompute/layercache', 'v0.1.0-beta.10'],
      ['another/layercache', 'v0.1.0-beta.9'],
      ['ziahamza/layercache', 'v0.1.0-beta.9'],
    ]) {
      const current = run(repository, version, source);
      assert.equal(current.status, 0, current.stderr);
      assert.equal(current.calls.length, 1);
      assert.equal(current.calls[0][current.calls[0].indexOf('--repo') + 1], repository);
      assert.equal(current.calls[0].includes('--bundle'), false);
    }
    const failed = run('layercompute/layercache', 'v0.1.0-beta.10', source, true);
    assert.notEqual(failed.status, 0);
    assert.equal(failed.calls.length, 1);
    assert.equal(failed.calls[0].includes('--bundle'), false);
  } finally { rmSync(root, { recursive: true, force: true }); }
});
