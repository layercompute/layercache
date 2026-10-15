import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { writeProfile } from './profile.mjs';

test('runner image, job and build setting changes produce different native cache identities', t => {
  const cwd = mkdtempSync(join(tmpdir(), 'layercache-go-profile-'));
  t.after(() => rmSync(cwd, { recursive: true, force: true }));
  mkdirSync(join(cwd, '.github'));
  const env = { RUNNER_OS: 'Linux', RUNNER_ARCH: 'X64', ImageOS: 'ubuntu24',
    ImageVersion: '20261015.1', GITHUB_JOB: 'linux-go' };
  const profile = join(cwd, '.github/.go-cache-context');
  assert.equal(writeProfile(env, cwd), true);
  const baseline = readFileSync(profile, 'utf8');
  for (const change of [{ ImageVersion: '20261016.1' }, { GITHUB_JOB: 'another-job' },
    { CGO_ENABLED: '0' }, { GOFLAGS: '-race' }, { CGO_CFLAGS: '-DCHANGED' }]) {
    rmSync(profile);
    assert.equal(writeProfile({ ...env, ...change }, cwd), true);
    assert.notEqual(readFileSync(profile, 'utf8'), baseline);
  }
  writeFileSync(profile, 'pre-existing');
  assert.equal(writeProfile(env, cwd), false);
  assert.equal(readFileSync(profile, 'utf8'), 'pre-existing');
  rmSync(profile);
  for (const key of ['RUNNER_OS', 'RUNNER_ARCH', 'ImageOS', 'ImageVersion', 'GITHUB_JOB']) {
    assert.equal(writeProfile({ ...env, [key]: '' }, cwd), false);
  }
});
