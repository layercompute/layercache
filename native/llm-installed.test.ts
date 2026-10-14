import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtemp, mkdir, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { generateText, wrapLanguageModel } from 'ai';
import { MockLanguageModelV4 } from 'ai/test';
import { setup } from '../action/setup/main.mjs';
import { cleanup } from '../action/setup/post.mjs';
import { createLLMReuseMiddleware, LLMReplayMissError } from './llm.ts';
import type { LLMReuseOptions } from './llm.ts';

// Existing native-client CI supplies its already-compiled Go executable. This
// runs the actual authenticated artifact endpoint/store without paid inference.
test('actual Go artifact service preserves a concurrent LLM winner and restores on an empty SDK consumer', { skip: !process.env.LAYERCACHE_SETUP_BINARY }, async () => {
  const root = await mkdtemp(join(tmpdir(), 'layercache-llm-installed-'));
  const workspace = join(root, 'workspace'); await mkdir(workspace);
  execFileSync('git', ['init', '--quiet', '-b', 'main', workspace]);
  execFileSync('git', ['-C', workspace, 'remote', 'add', 'origin', 'https://github.com/acme/widget.git']);
  execFileSync('git', ['-C', workspace, '-c', 'user.name=QA', '-c', 'user.email=qa@example.invalid', 'commit', '--quiet', '--allow-empty', '-m', 'fixture']);
  await writeFile(join(root, 'event.json'), JSON.stringify({ repository: { default_branch: 'main' } }));
  let installation: Awaited<ReturnType<typeof setup>> | undefined;
  try {
    installation = await setup({ ...process.env, INPUT_BINARY: resolve(process.env.LAYERCACHE_SETUP_BINARY!), 'INPUT_MAX-SIZE': '10485760',
      INPUT_COMPATIBILITY: 'llm-installed-v1', RUNNER_TEMP: root, GITHUB_WORKSPACE: workspace, GITHUB_REPOSITORY: 'acme/widget', GITHUB_REF: 'refs/heads/main',
      GITHUB_SHA: execFileSync('git', ['-C', workspace, 'rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(),
      GITHUB_EVENT_PATH: join(root, 'event.json'), GITHUB_STATE: join(root, 'state'), GITHUB_ENV: join(root, 'env'), GITHUB_PATH: join(root, 'path'), GITHUB_OUTPUT: join(root, 'output'),
    }, { log: () => {} });
    const options: LLMReuseOptions = { project: 'github.com/acme/widget', compatibility: 'llm-installed-v1', scope: 'team',
      mode: 'read-through', profile: { endpointIdentity: 'fixture', accountIdentity: 'owner', providerRevision: 'fixture-v1', defaultsRevision: 'defaults-v1', modelRevision: 'model-v1' },
      context: { app: 'frozen-app', screenshot: new Uint8Array([1, 2]) }, corpus: { id: 'installed', generation: 'first' }, sampleSlot: '0', ttlMs: 60_000,
      cache: { endpoint: installation.endpoint, token: installation.route.environment.TURBO_TOKEN, maxBytes: 1024 ** 2 } };
    let calls = 0; let release!: () => void; const waiting = new Promise<void>(resolve => { release = resolve; });
    const model = (name: string, generate: () => Promise<string>, overrides: Partial<LLMReuseOptions> = {}) => wrapLanguageModel({
      model: new MockLanguageModelV4({ doGenerate: async () => { calls++; return { content: [{ type: 'text', text: await generate() }], finishReason: { unified: 'stop', raw: 'stop' },
        usage: { inputTokens: { total: 2, noCache: 2, cacheRead: 0, cacheWrite: 0 }, outputTokens: { total: 1, text: 1, reasoning: 0 } }, warnings: [] }; } }),
      middleware: createLLMReuseMiddleware({ ...options, ...overrides, cache: { ...options.cache, cacheDir: join(root, name) } }),
    });
    const losing = model('contender', async () => { await waiting; return 'losing fresh response'; });
    const contender = generateText({ model: losing, prompt: 'inspect the frozen screen', maxRetries: 0 });
    while (calls === 0) await new Promise(resolve => setImmediate(resolve));
    await generateText({ model: model('producer', async () => 'canonical response'), prompt: 'inspect the frozen screen', maxRetries: 0 });
    release();
    const winner = await contender;
    assert.equal(winner.text, 'canonical response'); assert.equal(winner.usage.totalTokens, 3);
    const consumer = await generateText({ model: model('empty-consumer', async () => { throw new Error('provider must not execute'); }, { mode: 'replay' }), prompt: 'inspect the frozen screen', maxRetries: 0 });
    assert.equal(consumer.text, 'canonical response'); assert.equal(consumer.usage.totalTokens, 0); assert.equal(calls, 2);
    await assert.rejects(generateText({ model: model('wrong-profile', async () => 'must not run', { mode: 'replay', compatibility: 'wrong-profile' }), prompt: 'inspect the frozen screen', maxRetries: 0 }), LLMReplayMissError);
    await assert.rejects(generateText({ model: model('wrong-project', async () => 'must not run', { mode: 'replay', project: 'github.com/acme/other' }), prompt: 'inspect the frozen screen', maxRetries: 0 }), LLMReplayMissError);
    assert.equal(calls, 2);
  } finally {
    if (installation) cleanup({ RUNNER_TEMP: root, STATE_root: installation.root, STATE_binary: installation.binary, STATE_config: installation.config });
    await rm(root, { recursive: true, force: true });
  }
});
