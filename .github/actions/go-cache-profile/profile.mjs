import { appendFileSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

export function writeProfile(env, cwd = process.cwd()) {
  const required = ['RUNNER_OS', 'RUNNER_ARCH', 'ImageOS', 'ImageVersion', 'GITHUB_JOB'];
  if (required.some(name => !env[name])) return false;
  const settings = ['GOFLAGS', 'GOEXPERIMENT', 'GODEBUG', 'GOTOOLCHAIN', 'CGO_ENABLED',
    'CC', 'CXX', 'CGO_CFLAGS', 'CGO_CPPFLAGS', 'CGO_CXXFLAGS', 'CGO_FFLAGS', 'CGO_LDFLAGS',
    'GOAMD64', 'GOARM64'];
  const profile = Object.fromEntries([...required, ...settings].map(name => [name, env[name] ?? '']));
  try {
    writeFileSync(resolve(cwd, '.github/.go-cache-context'), JSON.stringify(profile), { mode: 0o600, flag: 'wx' });
    return true;
  } catch {
    return false;
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const enabled = writeProfile(process.env);
  if (process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT, `enabled=${enabled}\n`);
  console.log(enabled ? 'Native Go compilation cache has a complete runner profile.'
    : 'Native Go cache disabled: runner identity or owned profile file unavailable.');
}
