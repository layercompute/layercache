import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { chmodSync, mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { classify } from './classify.mjs';

function fixture(t, change) {
  const cwd = mkdtempSync(join(tmpdir(), 'layercache-ci-scope-'));
  t.after(() => rmSync(cwd, { recursive: true, force: true }));
  const git = (...args) => execFileSync('git', ['-c', 'user.name=CI fixture',
    '-c', 'user.email=ci@example.invalid', '-c', 'commit.gpgsign=false', ...args],
  { cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();
  const write = (path, value) => {
    mkdirSync(dirname(join(cwd, path)), { recursive: true });
    writeFileSync(join(cwd, path), value);
  };
  git('init', '-q', '-b', 'main');
  write('main.go', 'package main\n');
  write('README.md', 'Initial prose\n');
  write('docs/old.md', 'Old prose\n');
  git('add', '.'); git('commit', '-qm', 'base');
  const base = git('rev-parse', 'HEAD');
  git('checkout', '-qb', 'contribution');
  change({ cwd, git, write });
  git('add', '-A'); git('commit', '--allow-empty', '-qm', 'change');
  const head = git('rev-parse', 'HEAD');
  git('checkout', '-q', 'main'); git('merge', '--no-ff', '-qm', 'PR merge', 'contribution');
  const sha = git('rev-parse', 'HEAD');
  const eventPath = join(cwd, 'event.json');
  writeFileSync(eventPath, JSON.stringify({ pull_request: {
    base: { sha: base }, head: { sha: head, repo: { fork: true } },
  } }));
  return { cwd, git, eventPath, eventName: 'pull_request', sha };
}

test('fork-shaped PR with prose additions, modification and deletion selects docs', t => {
  const input = fixture(t, ({ cwd, write }) => {
    write('README.md', 'Changed prose\n');
    write('TODOs.md', 'Next steps\n');
    write('docs/nested/new.md', 'New prose\n');
    rmSync(join(cwd, 'docs/old.md'));
  });
  assert.equal(classify(input).mode, 'docs');
});

for (const path of ['main.go', '.github/workflows/ci.yml', 'go.sum', 'docs/evidence.json',
  'AGENTS.md', 'CONTEXT.md', 'native/README.md', 'unknown.md']) {
  test(`${path} with prose retains full checks`, t => {
    const input = fixture(t, ({ write }) => { write('README.md', 'Prose\n'); write(path, 'Changed\n'); });
    assert.equal(classify(input).mode, 'full');
  });
}

test('moving executable input into Markdown retains full checks', t => {
  const input = fixture(t, ({ git }) => git('mv', 'main.go', 'docs/source.md'));
  assert.equal(classify(input).mode, 'full');
});

test('executable Markdown retains full checks', t => {
  const input = fixture(t, ({ cwd }) => chmodSync(join(cwd, 'README.md'), 0o755));
  assert.equal(classify(input).mode, 'full');
});

test('Markdown symlinks retain full checks', t => {
  const input = fixture(t, ({ cwd }) => symlinkSync('../main.go', join(cwd, 'docs/source.md')));
  assert.equal(classify(input).mode, 'full');
});

test('missing history, malformed events and mismatched checkout retain full checks', t => {
  const input = fixture(t, ({ write }) => write('README.md', 'Prose\n'));
  assert.equal(classify({ ...input, sha: '0'.repeat(40) }).mode, 'full');
  writeFileSync(input.eventPath, JSON.stringify({ pull_request: {
    base: { sha: '0'.repeat(40) }, head: { sha: input.sha },
  } }));
  assert.equal(classify(input).mode, 'full');
  writeFileSync(input.eventPath, '{');
  assert.equal(classify(input).mode, 'full');
});

test('empty PR and non-PR events retain full checks', t => {
  const input = fixture(t, () => {});
  assert.equal(classify(input).mode, 'full');
  for (const eventName of ['push', 'workflow_dispatch', 'schedule', undefined]) {
    assert.equal(classify({ ...input, eventName }).mode, 'full');
  }
});

test('a genuinely shallow checkout retains full checks', t => {
  const input = fixture(t, ({ write }) => write('README.md', 'Prose\n'));
  const shallow = join(input.cwd, 'shallow');
  execFileSync('git', ['clone', '-q', '--depth=1', `file://${input.cwd}`, shallow],
    { stdio: ['ignore', 'pipe', 'pipe'] });
  assert.equal(classify({ ...input, cwd: shallow }).mode, 'full');
});
