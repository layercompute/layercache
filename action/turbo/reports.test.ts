import assert from 'node:assert/strict';
import {
  mkdtemp,
  mkdir,
  writeFile,
  readFile,
  rm,
  symlink,
  truncate,
  open,
} from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import test from 'node:test';
import { setupTurbo } from './main.ts';
import { reportTurbo } from './post.ts';
import { startReport, completedSummaries } from './reports.ts';

test('action reports only new summaries with fresh OIDC, without environment secrets', async (t) => {
  const workspace = await mkdtemp(join(tmpdir(), 'turbo-report-'));
  t.after(() => rm(workspace, { recursive: true, force: true }));
  const runs = join(workspace, '.turbo/runs');
  await mkdir(runs, { recursive: true });
  await writeFile(join(runs, 'old.json'), '{}');
  const env: NodeJS.ProcessEnv = {
    'INPUT_TEAM-URL': 'https://cache.example',
    INPUT_COMPATIBILITY: 'linux-node24',
    GITHUB_REPOSITORY: 'acme/widget',
    GITHUB_WORKSPACE: workspace,
    GITHUB_ENV: 'env',
    GITHUB_OUTPUT: 'output',
    GITHUB_STATE: 'state',
    GITHUB_STEP_SUMMARY: join(workspace, 'step-summary.md'),
    ACTIONS_ID_TOKEN_REQUEST_URL: 'https://oidc.example/token',
    ACTIONS_ID_TOKEN_REQUEST_TOKEN: 'request-token',
  };
  const states: Record<string, string> = {};
  const exports: Record<string, string> = {};
  await setupTurbo(env, {
    fetcher: async (url) =>
      Response.json(
        String(url).includes('oidc.example')
          ? { value: 'setup-identity' }
          : {
              teamToken: 'setup-token',
              expiresAt: new Date(Date.now() + 1800_000).toISOString(),
            },
      ),
    write: (path, name, value) => {
      if (path === 'state') states[name] = value;
      if (path === 'env') exports[name] = value;
    },
    log: () => {},
  });
  assert.equal(exports.TURBO_RUN_SUMMARY, 'true');
  const now = Date.now();
  await writeFile(
    join(runs, 'new.json'),
    JSON.stringify({
      id: 'new',
      version: '1',
      execution: { startTime: now, endTime: now + 1 },
      environmentVariables: { secret: 'do-not-send' },
      tasks: [
        {
          taskId: 'app#build',
          hash: 'hash',
          dependencies: [],
          command: 'secret-command',
          environmentVariables: { secret: 'do-not-send' },
          execution: { startTime: now, endTime: now + 1, exitCode: 0 },
          cache: { status: 'MISS' },
          resolvedTaskDefinition: { cache: true },
        },
      ],
    }),
  );
  let submitted: unknown;
  const logs: string[] = [];
  await reportTurbo(
    { ...env, STATE_layercache_turbo: states.layercache_turbo },
    {
      fetcher: async (url, init) => {
        if (String(url).includes('oidc.example'))
          return Response.json({ value: 'fresh-identity' });
        if (String(url).includes('/exchange'))
          return Response.json({
            teamToken: 'fresh-token',
            expiresAt: new Date(Date.now() + 1800_000).toISOString(),
          });
        assert.equal(
          new Headers(init?.headers).get('authorization'),
          'Bearer fresh-token',
        );
        submitted = JSON.parse(String(init?.body));
        return Response.json({
          runId: 'signed-run',
          tasks: 1,
          report: {
            eligible: 1,
            hits: 0,
            misses: 1,
            hitRate: 0,
            netEstimatedBuildTimeSaved: { milliseconds: 0, known: 1, total: 1 },
          },
        });
      },
      log: (value) => logs.push(value),
    },
  );
  assert.ok(submitted);
  assert.equal((submitted as { summaries: unknown[] }).summaries.length, 1);
  assert.ok(!JSON.stringify(submitted).includes('do-not-send'));
  assert.ok(!JSON.stringify(submitted).includes('secret-command'));
  assert.ok(logs.some((value) => value.includes('1 eligible')));
  assert.match(
    await readFile(env.GITHUB_STEP_SUMMARY!, 'utf8'),
    /0 hits \/ 1 eligible tasks/,
  );
});

test('missing summaries make no credential request and report failure remains a warning', async (t) => {
  const workspace = await mkdtemp(join(tmpdir(), 'turbo-report-'));
  t.after(() => rm(workspace, { recursive: true, force: true }));
  const state = await startReport(workspace, '.', {
    endpoint: 'https://cache.example',
    project: 'github.com/acme/widget',
    compatibility: 'linux-node24',
  });
  const logs: string[] = [];
  await reportTurbo(
    { STATE_layercache_turbo: JSON.stringify(state) },
    {
      fetcher: () => assert.fail('no request without summaries'),
      log: (value) => logs.push(value),
    },
  );
  assert.ok(logs.some((value) => value.includes('no new Turbo summaries')));
  await reportTurbo(
    { STATE_layercache_turbo: 'not-json-secret' },
    {
      fetcher: () => assert.fail('no request on invalid state'),
      log: (value) => logs.push(value),
    },
  );
  assert.ok(logs.some((value) => value.startsWith('::warning::')));
  assert.ok(!logs.join('').includes('not-json-secret'));
});

test('summary collector rejects symlink files and workspace escapes', async (t) => {
  const workspace = await mkdtemp(join(tmpdir(), 'turbo-report-'));
  t.after(() => rm(workspace, { recursive: true, force: true }));
  const state = await startReport(workspace, '.', {
    endpoint: 'https://cache.example',
    project: 'github.com/acme/widget',
    compatibility: 'linux-node24',
  });
  const runs = join(workspace, '.turbo/runs');
  await mkdir(runs, { recursive: true });
  await writeFile(join(workspace, 'secret.json'), '{}');
  await symlink(join(workspace, 'secret.json'), join(runs, 'new.json'));
  await assert.rejects(completedSummaries(state), /Unsafe summary/);
  await assert.rejects(
    startReport(workspace, '..', {
      endpoint: 'https://cache.example',
      project: 'github.com/acme/widget',
      compatibility: 'linux-node24',
    }),
    /escapes workspace/,
  );
});

async function summaryFixture(t: { after: (fn: () => Promise<void>) => void }) {
  const workspace = await mkdtemp(join(tmpdir(), 'turbo-report-'));
  t.after(() => rm(workspace, { recursive: true, force: true }));
  const state = await startReport(workspace, '.', {
    endpoint: 'https://cache.example',
    project: 'github.com/acme/widget',
    compatibility: 'linux-node24',
  });
  const runs = join(workspace, '.turbo/runs');
  await mkdir(runs, { recursive: true });
  const now = Date.now();
  const task = (id = 'app#build') => ({
    taskId: id,
    hash: 'hash',
    dependencies: [],
    execution: { startTime: now, endTime: now + 1, exitCode: 0 },
    cache: { status: 'MISS' },
    resolvedTaskDefinition: { cache: true },
  });
  const summary = (id = 'summary') => ({
    id,
    version: '1',
    execution: { startTime: now, endTime: now + 1 },
    tasks: [task()],
  });
  return { state, runs, task, summary };
}

test('large native input maps are bounded locally and excluded from reported summaries', async (t) => {
  const { state, runs, summary } = await summaryFixture(t);
  // Real task graphs repeat source input maps across tasks. A representative
  // native document exceeds the old 2 MiB limit without a large report payload.
  const unused = 'private-input-and-environment-'.repeat(120_000);
  const document = summary();
  await writeFile(
    join(runs, 'large.json'),
    JSON.stringify({
      ...document,
      environmentVariables: unused,
      tasks: document.tasks.map((task) => ({
        ...task,
        inputs: { 'private/path.ts': unused },
        environmentVariables: unused,
      })),
    }),
  );
  const summaries = await completedSummaries(state);
  assert.equal(summaries.length, 1);
  assert.ok(JSON.stringify(summaries).length < 1024);
  assert.ok(!JSON.stringify(summaries).includes('private-input'));
});

test('raw file and cumulative raw summary limits reject sparse oversized files before parsing', async (t) => {
  const { state, runs } = await summaryFixture(t);
  const first = join(runs, 'one.json');
  await writeFile(first, '{}');
  await truncate(first, 64 * 1024 ** 2 + 1);
  await assert.rejects(completedSummaries(state), /summary.*limit/i);
  await truncate(first, 33 * 1024 ** 2);
  const second = join(runs, 'two.json');
  await writeFile(second, '{}');
  await truncate(second, 33 * 1024 ** 2);
  await assert.rejects(completedSummaries(state), /batch.*limit/i);
});

test('summary growth after stat is rejected by the actual read budget', async (t) => {
  const { state, runs, summary } = await summaryFixture(t);
  const path = join(runs, 'growing.json');
  await writeFile(path, JSON.stringify(summary()));
  await assert.rejects(
    completedSummaries(state, {
      openSummary: async (name, flags) => {
        const file = await open(name, flags);
        const stat = file.stat.bind(file);
        Object.defineProperty(file, 'stat', {
          value: async () => {
            const metadata = await stat();
            await truncate(path, 64 * 1024 ** 2 + 1);
            return metadata;
          },
        });
        return file;
      },
    }),
    /summary.*limit/i,
  );
});

test('sanitized request byte and cumulative task limits remain independent of raw metadata', async (t) => {
  const { state, runs, summary, task } = await summaryFixture(t);
  for (const name of ['one', 'two']) {
    const document = summary(name);
    document.tasks[0]!.hash = 'x'.repeat(4 * 1024 ** 2);
    await writeFile(join(runs, `${name}.json`), JSON.stringify(document));
  }
  await assert.rejects(completedSummaries(state), /request.*limit/i);
  for (const name of ['one', 'two']) {
    await writeFile(
      join(runs, `${name}.json`),
      JSON.stringify({
        ...summary(name),
        tasks: Array.from({ length: 2049 }, (_, index) => task(`app#task-${name}-${index}`)),
      }),
    );
  }
  await assert.rejects(completedSummaries(state), /task.*limit/i);
});

test('summary count stays bounded before collection', async (t) => {
  const { state, runs, summary } = await summaryFixture(t);
  await Promise.all(
    Array.from({ length: 33 }, (_, index) =>
      writeFile(join(runs, `${index}.json`), JSON.stringify(summary(`${index}`))),
    ),
  );
  await assert.rejects(completedSummaries(state), /Too many new summaries/);
});

test('the 8 MiB request limit includes its JSON envelope and UTF-8 bytes', async (t) => {
  const { state, runs, summary } = await summaryFixture(t);
  const document = summary();
  const path = join(runs, 'boundary.json');
  await writeFile(path, JSON.stringify(document));
  const initial = await completedSummaries(state);
  const padding =
    8 * 1024 ** 2 - Buffer.byteLength(JSON.stringify({ summaries: initial }));
  document.tasks[0]!.hash +=
    'é'.repeat(Math.floor(padding / 2)) + (padding % 2 ? 'x' : '');
  await writeFile(path, JSON.stringify(document));
  const accepted = await completedSummaries(state);
  assert.equal(
    Buffer.byteLength(JSON.stringify({ summaries: accepted })),
    8 * 1024 ** 2,
  );
  document.tasks[0]!.hash += 'x';
  await writeFile(path, JSON.stringify(document));
  await assert.rejects(completedSummaries(state), /request.*limit/i);
});

test('report failures expose fixed stages and numeric HTTP status without private diagnostics', async (t) => {
  for (const stage of ['state', 'collect', 'auth', 'submit', 'response']) {
    await t.test(stage, async (t) => {
      const { state, runs, summary } = await summaryFixture(t);
      await writeFile(join(runs, 'new.json'), JSON.stringify(summary()));
      if (stage === 'collect') {
        await rm(join(runs, 'new.json'));
        await symlink('/private/path-with-token', join(runs, 'new.json'));
      }
      const logs: string[] = [];
      await reportTurbo(
        {
          STATE_layercache_turbo:
            stage === 'state' ? 'invalid-private-state-token' : JSON.stringify(state),
          ACTIONS_ID_TOKEN_REQUEST_URL: 'https://oidc.example/token',
          ACTIONS_ID_TOKEN_REQUEST_TOKEN: 'private-request-token',
        },
        {
          fetcher: async (url) => {
            if (stage === 'auth')
              throw new Error('private-error-with-path-and-token');
            if (String(url).includes('oidc.example'))
              return Response.json({ value: 'fixture-identity' });
            if (String(url).includes('/exchange'))
              return Response.json({
                teamToken: 'fixture-capability',
                expiresAt: new Date(Date.now() + 300_000).toISOString(),
              });
            return new Response('private-server-body-token', {
              status: stage === 'submit' ? 403 : 200,
              headers: { 'X-Private-Header': 'private-response-header-token' },
            });
          },
          log: (value) => logs.push(value),
        },
      );
      const warning = logs.find((value) => value.startsWith('::warning::'));
      assert.ok(warning?.includes(`stage=${stage}`));
      assert.equal(warning?.includes('HTTP 403'), stage === 'submit');
      assert.equal(warning?.includes('HTTP 200'), stage === 'response');
      assert.ok(!logs.join('').includes('private-'));
      assert.match(warning!, /Build result is unchanged/);
    });
  }
});
