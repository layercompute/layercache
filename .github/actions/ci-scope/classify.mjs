import { execFileSync } from 'node:child_process';
import { appendFileSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const commitID = /^[a-f0-9]{40}$/;
const full = reason => ({ mode: 'full', reason });

function prosePath(path) {
  return path === 'README.md' || path === 'TODOs.md' ||
    path.startsWith('docs/') && path.endsWith('.md') &&
    path.split('/').every(part => part !== '' && part !== '.' && part !== '..');
}

function proseDiff(raw) {
  const fields = new TextDecoder('utf-8', { fatal: true }).decode(raw).split('\0');
  if (fields.pop() !== '' || fields.length === 0 || fields.length % 2 !== 0) return false;
  for (let index = 0; index < fields.length; index += 2) {
    const header = /^:(\d{6}) (\d{6}) [a-f0-9]{40} [a-f0-9]{40} ([AMD])$/.exec(fields[index]);
    if (!header || !prosePath(fields[index + 1])) return false;
    const [, before, after, status] = header;
    // Executable Markdown and symlink changes require full checks too.
    if (before !== (status === 'A' ? '000000' : '100644') ||
        after !== (status === 'D' ? '000000' : '100644')) return false;
  }
  return true;
}

export function classify({ eventName, eventPath, sha, cwd = process.cwd() }) {
  if (eventName !== 'pull_request') return full('main, manual and other events run all checks');
  try {
    const event = JSON.parse(readFileSync(eventPath, 'utf8'));
    const base = event.pull_request?.base?.sha;
    const head = event.pull_request?.head?.sha;
    if (![base, head, sha].every(value => typeof value === 'string' && commitID.test(value))) {
      return full('missing or invalid commit identity');
    }
    const git = args => execFileSync('git', args, {
      cwd, timeout: 5000, maxBuffer: 4 << 20, stdio: ['ignore', 'pipe', 'pipe'],
    });
    if (git(['rev-parse', 'HEAD']).toString().trim() !== sha) return full('checkout identity differs');
    const parents = git(['show', '-s', '--format=%P', sha]).toString().trim().split(' ');
    if (parents.length !== 2 || parents[0] !== base || parents[1] !== head) {
      return full('checkout is not this PR merge');
    }
    for (const commit of [base, head]) git(['cat-file', '-e', `${commit}^{commit}`]);
    const diff = git(['diff', '--no-ext-diff', '--no-textconv', '--no-renames',
      '--raw', '--no-abbrev', '-z', base, head, '--']);
    return proseDiff(diff) ? { mode: 'docs', reason: 'only allowed regular prose files changed' }
      : full('executable, unknown or empty change set');
  } catch {
    return full('comparison unavailable or malformed');
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const result = classify({ eventName: process.env.GITHUB_EVENT_NAME,
    eventPath: process.env.GITHUB_EVENT_PATH, sha: process.env.GITHUB_SHA });
  if (process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT, `mode=${result.mode}\n`);
  const message = `CI scope: ${result.mode}. ${result.reason}.`;
  console.log(message);
  if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, `${message}\n`);
}
