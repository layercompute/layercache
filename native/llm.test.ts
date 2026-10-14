import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { createHash } from 'node:crypto';
import { mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { generateText, jsonSchema, Output, stepCountIs, ToolLoopAgent, tool, wrapLanguageModel } from 'ai';
import { MockLanguageModelV4 } from 'ai/test';
import type { LanguageModelV4CallOptions, LanguageModelV4GenerateResult } from '@ai-sdk/provider';
import { createLLMReuseMiddleware, LLMReplayMissError } from './llm.ts';
import type { LLMReuseEvent, LLMReuseOptions } from './llm.ts';
import { canonicalLLMValue, decodeLLMValue } from './llm-codec.ts';
import { NativeCache, NativeCacheConflictError, artifactKey } from './client.ts';

const usage = { inputTokens: { total: 8, noCache: 8, cacheRead: 0, cacheWrite: 0 }, outputTokens: { total: 3, text: 2, reasoning: 1 } };
const result = (text = 'recorded answer'): LanguageModelV4GenerateResult => ({ content: [{ type: 'text', text }], finishReason: { unified: 'stop', raw: 'stop' }, usage, warnings: [] });
const params: LanguageModelV4CallOptions = { prompt: [{ role: 'user', content: [{ type: 'text', text: 'inspect this screen' }] }] };
async function fixture(t: { after: (callback: () => Promise<void>) => void }) {
  const root = await mkdtemp(join(tmpdir(), 'layercache-llm-test-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const options: LLMReuseOptions = { project: 'github.com/example/phone', compatibility: 'llm-fixture-profile-v1', scope: 'team', mode: 'read-through',
    profile: { endpointIdentity: 'fixture-endpoint-v1', accountIdentity: 'fixture-owner', providerRevision: 'fixture-provider-v1', defaultsRevision: 'defaults-v1', modelRevision: 'model-v1' },
    context: { app: 'app-sha', harness: 'harness-v1', tools: 'tool-implementation-v1', screenshot: new Uint8Array([1, 2, 3]) },
    corpus: { id: 'phone-planner', generation: 'first' }, sampleSlot: '0', ttlMs: 60_000, cache: { cacheDir: join(root, 'cache'), maxBytes: 8 * 1024 ** 2 } };
  return { root, options };
}
function wrapped(options: LLMReuseOptions, generate: (options: LanguageModelV4CallOptions) => Promise<LanguageModelV4GenerateResult> = async () => result()) {
  const model = wrapLanguageModel({ model: new MockLanguageModelV4({ doGenerate: generate }), middleware: createLLMReuseMiddleware(options) });
  return { ...model, doGenerate: async (params: LanguageModelV4CallOptions) => await model.doGenerate(params) };
}

test('actual AI SDK generateText records and replays with zero fresh tokens and no extra provider calls', async t => {
  const { root, options } = await fixture(t);
  let calls = 0;
  const events: LLMReuseEvent[] = [];
  const model = wrapped({ ...options, onEvent: event => { events.push(event); } }, async () => {
    calls++;
    return { ...result(), content: [{ type: 'reasoning', text: 'thought' }, { type: 'text', text: 'answer' }],
      response: { id: 'provider-response', timestamp: new Date('2026-01-01T00:00:00Z'), modelId: 'fixture-model', headers: { authorization: 'secret-response-header' }, body: { token: 'secret-debug-response' } },
      request: { body: { apiKey: 'secret-debug-request' } }, providerMetadata: { fixture: { trace: 'semantic' } } };
  });
  const first = await generateText({ model, prompt: 'inspect', maxRetries: 0 });
  const replay = await generateText({ model, prompt: 'inspect', maxRetries: 0 });
  assert.equal(calls, 1); assert.equal(first.text, 'answer'); assert.equal(replay.text, 'answer');
  assert.equal(replay.usage.totalTokens, 0); assert.equal(first.usage.totalTokens, 11);
  assert.equal(events[1]?.currentProviderCalls, 0); assert.deepEqual(events[1]?.historicalUsage, usage);
  assert.equal(replay.response.timestamp.toISOString(), '2026-01-01T00:00:00.000Z');
  const key = events[0]!.key!;
  await new NativeCache(options.cache).restore({ project: options.project, compatibility: options.compatibility, key }, join(root, 'inspect'));
  const bytes = await readFile(join(root, 'inspect', 'response.json'), 'utf8');
  assert.ok(!bytes.includes('secret-')); assert.ok(!bytes.includes('inspect this screen'));
  assert.equal(replay.providerMetadata?.layercache?.outcome, 'hit');
});

test('actual ToolLoopAgent replays planner calls while executing tools against the current observation', async t => {
  const { options } = await fixture(t);
  let providerCalls = 0; let observations = 0;
  const model = wrapped(options, async call => {
    providerCalls++;
    return call.prompt.at(-1)?.role === 'tool' ? result('accepted current screen') : {
      ...result(), content: [{ type: 'tool-call', toolCallId: 'observe-fixed', toolName: 'observe', input: '{}' }], finishReason: { unified: 'tool-calls', raw: 'tool_calls' },
    };
  });
  const agent = new ToolLoopAgent({ model, maxRetries: 0, stopWhen: stepCountIs(2), tools: { observe: tool({
    description: 'Read the live screen', inputSchema: jsonSchema<Record<string, never>>({ type: 'object', properties: {}, additionalProperties: false }),
    execute: async () => { observations++; return 'identical current screen'; },
  }) } });
  const first = await agent.generate({ prompt: 'inspect' });
  const replay = await agent.generate({ prompt: 'inspect' });
  assert.equal(first.text, 'accepted current screen'); assert.equal(replay.text, first.text);
  assert.equal(providerCalls, 2); assert.equal(observations, 2); assert.equal(replay.totalUsage.totalTokens, 0);
  // A judge constructed with the raw provider remains fresh, independently of planner reuse.
  let judgeCalls = 0;
  const judge = new MockLanguageModelV4({ doGenerate: async () => { judgeCalls++; return result('current assertion'); } });
  await generateText({ model: judge, prompt: 'judge current screen' });
  await generateText({ model: judge, prompt: 'judge current screen' });
  assert.equal(judgeCalls, 2);
});

test('each context, profile, model, project, corpus and sample component isolates an immutable recording', async t => {
  const { options } = await fixture(t);
  let calls = 0;
  const generate = async () => { calls++; return result(); };
  await wrapped(options, generate).doGenerate(params);
  const changes: Partial<LLMReuseOptions>[] = [
    { project: 'github.com/example/other' }, { compatibility: 'changed-profile' }, { scope: 'personal' }, { sampleSlot: '1' },
    { corpus: { ...options.corpus, id: 'other' } }, { corpus: { ...options.corpus, generation: 'second' } },
    { context: { ...options.context as object, screenshot: new Uint8Array([1, 2, 4]) } },
    ...Object.keys(options.profile).map(field => ({ profile: { ...options.profile, [field]: 'changed' } })),
  ];
  for (const change of changes) await wrapped({ ...options, ...change }, generate).doGenerate(params);
  for (const [provider, modelId] of [['other-provider', 'mock-model-id'], ['mock-provider', 'other-model']]) {
    await wrapLanguageModel({ model: new MockLanguageModelV4({ provider, modelId, doGenerate: generate }), middleware: createLLMReuseMiddleware(options) }).doGenerate(params);
  }
  assert.equal(calls, 1 + changes.length + 2);
});

test('all normalized request fields, ordered history, schema, tools, bytes and tool observations participate', async t => {
  const { options } = await fixture(t);
  let calls = 0; const model = wrapped(options, async () => { calls++; return result(); });
  await model.doGenerate(params);
  const changes: LanguageModelV4CallOptions[] = [
    { ...params, temperature: 0 }, { ...params, maxOutputTokens: 10 }, { ...params, seed: 1 }, { ...params, topP: 0.5 }, { ...params, topK: 2 },
    { ...params, presencePenalty: 1 }, { ...params, frequencyPenalty: 1 }, { ...params, stopSequences: ['STOP'] }, { ...params, reasoning: 'high' },
    { ...params, headers: { 'x-model-route': 'other' } }, { ...params, providerOptions: { fixture: { detail: true } } }, { ...params, includeRawChunks: false },
    { ...params, responseFormat: { type: 'json', name: 'screen', schema: { type: 'object', properties: { visible: { type: 'boolean' } } } } },
    { ...params, toolChoice: { type: 'none' } }, { ...params, tools: [{ type: 'function', name: 'observe', description: 'live', inputSchema: { type: 'object' } }] },
    { prompt: [{ role: 'system', content: 'exact instruction' }, ...params.prompt] },
    { prompt: [...params.prompt, { role: 'tool', content: [{ type: 'tool-result', toolCallId: 'a', toolName: 'observe', output: { type: 'json', value: { screen: 'changed' } } }] }] },
    { prompt: [{ role: 'user', content: [{ type: 'file', mediaType: 'image/png', data: { type: 'data', data: new Uint8Array([1, 2]) } }] }] },
    { prompt: [{ role: 'user', content: [{ type: 'file', mediaType: 'image/png', data: { type: 'data', data: new Uint8Array([1, 3]) } }] }] },
  ];
  for (const change of changes) await model.doGenerate(change);
  assert.equal(calls, 1 + changes.length);
  const reordered = { ...params, headers: { z: 'last', a: 'first' } };
  await model.doGenerate(reordered); await model.doGenerate({ ...params, headers: { a: 'first', z: 'last' } });
  assert.equal(calls, changes.length + 2);
});

test('binary codec round-trips typed bytes, dates, undefined, negative zero and JSON tag-looking objects', () => {
  const value = { bytes: new Uint8Array([0, 255]), buffer: new Uint8Array([1, 2]).buffer, date: new Date(0), optional: undefined, zero: -0, data: { type: 'bytes', value: 'different' } };
  const decoded = decodeLLMValue(canonicalLLMValue(value, 10_000), 10_000);
  assert.deepEqual(decoded, value);
  assert.notEqual(canonicalLLMValue(new Uint8Array([1]), 1000), canonicalLLMValue('AQ==', 1000));
  assert.throws(() => decodeLLMValue('["bytes","!?invalid"]', 1000));
});

test('fresh bypasses cache reads and writes; replay misses never invoke the provider; record is immutable', async t => {
  const { options } = await fixture(t); let calls = 0;
  const generate = async () => result(`response-${++calls}`);
  await assert.rejects(wrapped({ ...options, mode: 'replay' }, generate).doGenerate(params), LLMReplayMissError);
  assert.equal(calls, 0);
  assert.equal((await wrapped(options, generate).doGenerate(params)).content[0]?.type, 'text');
  const fresh = await wrapped({ ...options, mode: 'fresh' }, generate).doGenerate(params);
  assert.equal(fresh.content[0]?.type === 'text' && fresh.content[0].text, 'response-2');
  const replay = await wrapped({ ...options, mode: 'replay' }, generate).doGenerate(params);
  assert.equal(replay.content[0]?.type === 'text' && replay.content[0].text, 'response-1');
  await assert.rejects(wrapped({ ...options, mode: 'record' }, generate).doGenerate(params), NativeCacheConflictError);
  assert.equal(calls, 3);
  const winner = await wrapped(options, generate).doGenerate(params);
  assert.equal(winner.content[0]?.type === 'text' && winner.content[0].text, 'response-1');
});

test('absolute expiry is not renewed by hits and requires a new immutable generation to refresh', async t => {
  const { options } = await fixture(t);
  let calls = 0; const clock = Date.now; let now = clock(); Date.now = () => now;
  t.after(async () => { Date.now = clock; });
  const model = wrapped({ ...options, ttlMs: 1000 }, async () => { calls++; return result(); });
  await model.doGenerate(params); now += 900; await model.doGenerate(params); now += 101;
  await assert.rejects(wrapped({ ...options, ttlMs: 1000, mode: 'replay' }).doGenerate(params), LLMReplayMissError);
  assert.equal(calls, 1);
  const unrecorded = await model.doGenerate(params);
  assert.equal(unrecorded.providerMetadata?.layercache?.outcome, 'unrecorded');
  assert.equal(calls, 2);
  const refreshed = await wrapped({ ...options, ttlMs: 1000, corpus: { ...options.corpus, generation: 'rotated' } }, async () => { calls++; return result(); }).doGenerate(params);
  assert.equal(refreshed.providerMetadata?.layercache?.outcome, 'recorded');
});

test('unknown options, URL/file references and credential-bearing parameters bypass safely', async t => {
  const { options } = await fixture(t); let calls = 0;
  const events: LLMReuseEvent[] = []; const model = wrapped({ ...options, onEvent: event => { events.push(event); } }, async () => { calls++; return result(); });
  const unknown = { ...params, futureOption: 'changed' };
  const url: LanguageModelV4CallOptions = { prompt: [{ role: 'user', content: [{ type: 'file', mediaType: 'image/png', data: { type: 'url', url: new URL('https://example.com/mutable.png') } }] }] };
  for (const input of [unknown, url, { ...params, headers: { authorization: 'private-secret' } }, { ...params, tools: [{ type: 'provider' as const, id: 'fixture.search' as const, name: 'search', args: {} }] }]) { await model.doGenerate(input); await model.doGenerate(input); }
  assert.equal(calls, 8); assert.ok(events.every(event => event.outcome === 'bypass'));
  assert.equal((await readdir(options.cache!.cacheDir!).catch(() => [])).length, 0);
  await assert.rejects(wrapped({ ...options, mode: 'replay' }).doGenerate(url), LLMReplayMissError);
});

test('provider failures, cancellation, incomplete output and oversized output never become recordings', async t => {
  const { options } = await fixture(t); let calls = 0;
  const failure = wrapped(options, async () => { calls++; throw new Error('provider unavailable'); });
  await assert.rejects(failure.doGenerate(params), /provider unavailable/); await assert.rejects(failure.doGenerate(params), /provider unavailable/);
  const controller = new AbortController();
  await assert.rejects(wrapped(options, async () => { calls++; controller.abort(); return result(); }).doGenerate({ ...params, abortSignal: controller.signal }));
  for (const output of [{ ...result(), finishReason: { unified: 'length' as const, raw: 'length' } }, result('x'.repeat(2000))]) {
    const model = wrapped({ ...options, maxEntryBytes: 1000 }, async () => { calls++; return output; });
    await model.doGenerate(params); await model.doGenerate(params);
  }
  assert.equal(calls, 7);
  await assert.rejects(wrapped({ ...options, mode: 'replay' }).doGenerate(params), LLMReplayMissError);
});

test('streaming always delegates without recording, and event callback failures do not change provider outcomes', async t => {
  const { options } = await fixture(t); let streams = 0;
  const raw = new MockLanguageModelV4({ doStream: async () => { streams++; return { stream: new ReadableStream({ start(controller) { controller.close(); } }) }; } });
  const model = wrapLanguageModel({ model: raw, middleware: createLLMReuseMiddleware({ ...options, mode: 'read-through', onEvent: () => { throw new Error('observer failure'); } }) });
  await model.doStream(params); await model.doStream(params); assert.equal(streams, 2);
  const replay = wrapLanguageModel({ model: raw, middleware: createLLMReuseMiddleware({ ...options, mode: 'replay' }) });
  await assert.rejects(async () => await replay.doStream(params), /streaming-unsupported/); assert.equal(streams, 2);
  const fresh = wrapped({ ...options, onEvent: async () => { throw new Error('async observer failure'); } });
  await fresh.doGenerate(params); await fresh.doGenerate(params);
});

async function privateServer(t: { after: (callback: () => Promise<void>) => void }) {
  const objects = new Map<string, Buffer>();
  const server = createServer(async (request, response) => {
    if (request.headers.authorization !== 'Bearer owner-token') { response.writeHead(403).end(); return; }
    const key = request.url!;
    if (request.method === 'PUT') {
      const chunks = []; for await (const chunk of request) chunks.push(chunk);
      const bytes = Buffer.concat(chunks); const existing = objects.get(key);
      if (existing && !existing.equals(bytes)) { response.writeHead(409).end(); return; }
      objects.set(key, bytes); response.writeHead(200).end(); return;
    }
    const bytes = objects.get(key);
    if (!bytes) { response.writeHead(404).end(); return; }
    response.writeHead(200, { 'content-length': bytes.length, 'x-layercache-digest': `sha256:${createHash('sha256').update(bytes).digest('hex')}` }).end(bytes);
  });
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve())));
  const address = server.address(); assert.ok(address && typeof address !== 'string');
  return { endpoint: `http://127.0.0.1:${address.port}`, objects };
}

test('private transport restores on an empty consumer and concurrent read-through returns the committed winner', async t => {
  const { root, options } = await fixture(t); const { endpoint, objects } = await privateServer(t);
  const remote = { endpoint, token: 'owner-token' };
  let calls = 0;
  let release!: () => void; const waiting = new Promise<void>(resolve => { release = resolve; });
  const producer = wrapped({ ...options, cache: { ...options.cache, ...remote, cacheDir: join(root, 'producer') } }, async () => { calls++; return result('winner'); });
  const contenderEvents: LLMReuseEvent[] = [];
  const contender = wrapped({ ...options, cache: { ...options.cache, ...remote, cacheDir: join(root, 'contender') }, onEvent: event => { contenderEvents.push(event); } }, async () => { calls++; await waiting; return result('loser'); });
  const loserRequest = contender.doGenerate(params);
  // Wait until the contender has reached its provider before committing the winner.
  while (calls === 0) await new Promise(resolve => setImmediate(resolve));
  await producer.doGenerate(params); release();
  const canonical = await loserRequest;
  assert.equal(canonical.content[0]?.type === 'text' && canonical.content[0].text, 'winner');
  assert.equal(canonical.usage.inputTokens.total, 8); // the losing fresh request actually consumed tokens
  assert.equal(contenderEvents.at(-1)?.currentProviderCalls, 1); assert.equal(contenderEvents.at(-1)?.outcome, 'hit');
  const consumer = wrapped({ ...options, mode: 'replay', cache: { ...options.cache, ...remote, cacheDir: join(root, 'consumer') } }, async () => { throw new Error('must never call provider'); });
  const restored = await consumer.doGenerate(params);
  assert.equal(restored.content[0]?.type === 'text' && restored.content[0].text, 'winner');
  assert.equal(restored.providerMetadata?.layercache?.source, 'team'); assert.equal(objects.size, 1);
  const again = await contender.doGenerate(params); assert.equal(again.content[0]?.type === 'text' && again.content[0].text, 'winner');
  assert.equal(calls, 2);
  await assert.rejects(wrapped({ ...options, mode: 'replay', project: 'other-project', cache: { ...options.cache, ...remote, cacheDir: join(root, 'other') } }).doGenerate(params), LLMReplayMissError);
  await assert.rejects(wrapped({ ...options, mode: 'replay', cache: { ...options.cache, endpoint, token: 'wrong-token', cacheDir: join(root, 'denied') } }).doGenerate(params), LLMReplayMissError);
});

test('corrupt archives and codec envelopes cannot replay', async t => {
  const { root, options } = await fixture(t); const events: LLMReuseEvent[] = [];
  await wrapped({ ...options, onEvent: event => { events.push(event); } }).doGenerate(params);
  const identity = { project: options.project, compatibility: options.compatibility, key: events[0]!.key! };
  const cache = new NativeCache(options.cache);
  await writeFile(join(cache.root, `${artifactKey(identity)}.tgz`), 'corrupt');
  await assert.rejects(wrapped({ ...options, mode: 'replay' }).doGenerate(params), LLMReplayMissError);
  const file = join(root, 'response.json');
  await writeFile(file, JSON.stringify({ version: 1, key: identity.key, recordedAt: Date.now(), expiresAt: Date.now() + options.ttlMs, result: '["unknown-codec"]' }));
  await cache.save(identity, file);
  await assert.rejects(wrapped({ ...options, mode: 'replay' }).doGenerate(params), LLMReplayMissError);
});

test('private semantic JSON is retained, while small storage budgets and strict record failures stay explicit', async t => {
  const { options } = await fixture(t);
  const call: LanguageModelV4CallOptions = { ...params, tools: [{ type: 'function' as const, name: 'domain', inputSchema: { type: 'object', properties: { password: { type: 'string' }, secret: { type: 'string' } } } }] };
  let calls = 0;
  const model = wrapped({ ...options, context: { type: 'url', password: 'private-domain-value' } }, async () => { calls++; return result(); });
  await model.doGenerate(call); await model.doGenerate(call); assert.equal(calls, 1);
  const bounded = wrapped({ ...options, cache: { ...options.cache, cacheDir: join(options.cache!.cacheDir!, 'small'), maxBytes: 1000 } });
  const output = await bounded.doGenerate(params); assert.equal(output.providerMetadata?.layercache?.outcome, 'unrecorded');
  await assert.rejects(wrapped({ ...options, mode: 'record', maxEntryBytes: 1000 }, async () => result('x'.repeat(2000))).doGenerate(params), /could not be committed/);
  let unsupportedCalls = 0;
  await assert.rejects(wrapped({ ...options, mode: 'record' }, async () => { unsupportedCalls++; return result(); }).doGenerate({ ...params, headers: { authorization: 'secret' } }), LLMReplayMissError);
  assert.equal(unsupportedCalls, 0);
});

test('expiry does not renew a retained sample, but actual eviction permits a new read-through recording', async t => {
  const { root, options } = await fixture(t); const clock = Date.now; let now = clock(); Date.now = () => now;
  t.after(async () => { Date.now = clock; });
  const evictable = { ...options, ttlMs: 1000, cache: { ...options.cache, maxAgeMs: 1000 } };
  let calls = 0;
  const model = wrapped(evictable, async () => result(`sample-${++calls}`));
  const first = await model.doGenerate(params); now += 2500;
  const expired = await model.doGenerate(params);
  assert.equal(expired.providerMetadata?.layercache?.outcome, 'unrecorded');
  const unrelated = join(root, 'unrelated.txt'); await writeFile(unrelated, 'maintenance trigger');
  await new NativeCache(evictable.cache).save({ project: options.project, compatibility: options.compatibility, key: 'other-artifact' }, unrelated);
  const renewed = await model.doGenerate(params);
  assert.equal(renewed.content[0]?.type === 'text' && renewed.content[0].text, 'sample-3');
  assert.equal(renewed.providerMetadata?.layercache?.outcome, 'recorded');
  assert.ok(Number(renewed.providerMetadata?.layercache?.recordedAt) > Number(first.providerMetadata?.layercache?.recordedAt));
  const replay = await wrapped({ ...evictable, mode: 'replay' }).doGenerate(params);
  assert.equal(replay.content[0]?.type === 'text' && replay.content[0].text, 'sample-3'); assert.equal(calls, 3);
});

test('actual SDK invalid tool JSON, unknown names and schema violations never become hits', async t => {
  const { options } = await fixture(t);
  for (const { toolName, input } of [{ toolName: 'observe', input: 'not-json' }, { toolName: 'undeclared', input: '{}' }, { toolName: 'observe', input: '{"ok":"wrong-type"}' }]) {
    let calls = 0;
    const events: LLMReuseEvent[] = [];
    const model = wrapped({ ...options, corpus: { ...options.corpus, generation: input }, onEvent: event => { events.push(event); } }, async () => {
      calls++; return { ...result(), content: [{ type: 'tool-call', toolCallId: 'test-call', toolName: toolName!, input: input! }], finishReason: { unified: 'tool-calls', raw: 'tool_calls' } };
    });
    const tools = { observe: tool({ inputSchema: jsonSchema<{ok:boolean}>({ type: 'object', properties: { ok: { type: 'boolean' } }, required: ['ok'], additionalProperties: false }, {
      validate: value => typeof value === 'object' && value !== null && 'ok' in value && typeof value.ok === 'boolean' ? { success: true, value: { ok: value.ok } } : { success: false, error: new Error('expected boolean ok') },
    }), execute: async () => 'current' }) };
    for (let attempt = 0; attempt < 2; attempt++) {
      const generated = await generateText({ model, prompt: 'observe', tools, maxRetries: 0 });
      assert.equal(generated.toolCalls[0]?.invalid, true);
    }
    assert.equal(calls, 2); assert.ok(events.every(event => event.outcome === 'unrecorded'));
  }
});

test('actual SDK malformed and schema-invalid structured output never becomes a recorded response', async t => {
  const { options } = await fixture(t);
  for (const text of ['not-json', '{"ok":"wrong-type"}']) {
    let calls = 0; const events: LLMReuseEvent[] = [];
    const model = wrapped({ ...options, corpus: { ...options.corpus, generation: text }, onEvent: event => { events.push(event); } }, async () => { calls++; return result(text); });
    const schema = jsonSchema<{ok:boolean}>({ type: 'object', properties: { ok: { type: 'boolean' } }, required: ['ok'], additionalProperties: false }, {
      validate: value => typeof value === 'object' && value !== null && 'ok' in value && typeof value.ok === 'boolean' ? { success: true, value: { ok: value.ok } } : { success: false, error: new Error('expected boolean ok') },
    });
    for (let attempt = 0; attempt < 2; attempt++) await assert.rejects(generateText({ model, prompt: 'object', maxRetries: 0, output: Output.object({ schema }) }), error => error instanceof Error && error.name === 'AI_NoObjectGeneratedError');
    assert.equal(calls, 2); assert.ok(events.every(event => event.outcome === 'unrecorded'));
  }
});

test('qualified normalized structured schemas replay, unsupported schemas decline, and hidden custom validators stay fresh by policy', async t => {
  const { options } = await fixture(t); let calls = 0;
  const schema = { type: 'object' as const, properties: { ok: { type: 'boolean' as const } }, required: ['ok'], additionalProperties: false };
  const model = wrapped(options, async () => { calls++; return result('{"ok":true}'); });
  for (let attempt = 0; attempt < 2; attempt++) {
    const generated = await generateText({ model, prompt: 'qualified', maxRetries: 0, output: Output.object({ schema: jsonSchema<{ok:boolean}>(schema) }) });
    assert.deepEqual(generated.output, { ok: true });
  }
  assert.equal(calls, 1);
  const events: LLMReuseEvent[] = [];
  const unsupported = wrapped({ ...options, onEvent: event => { events.push(event); } }, async () => { calls++; return result('{"ok":true}'); });
  for (let attempt = 0; attempt < 2; attempt++) {
    await generateText({ model: unsupported, prompt: 'unsupported', maxRetries: 0, output: Output.object({ schema: jsonSchema({ ...schema, $schema: 'https://example.invalid/custom-schema' }) }) });
  }
  assert.equal(calls, 3); assert.ok(events.every(event => event.outcome === 'unrecorded' && event.reason === 'unsupported-generated-json-schema'));
  // An arbitrary refinement is absent from normalized JSON Schema. A caller
  // requiring current custom validation selects fresh instead of recording it.
  const hidden = wrapped({ ...options, mode: 'fresh' }, async () => { calls++; return result('{"ok":true}'); });
  for (let attempt = 0; attempt < 2; attempt++) await assert.rejects(generateText({ model: hidden, prompt: 'custom', maxRetries: 0, output: Output.object({ schema: jsonSchema(schema, { validate: () => ({ success: false, error: new Error('live custom condition') }) }) }) }), error => error instanceof Error && error.name === 'AI_NoObjectGeneratedError');
  assert.equal(calls, 5);
});

test('async schemas and SDK-forbidden JSON properties are never admitted', async t => {
  const { options } = await fixture(t);
  const asyncSchema = { $async: true, type: 'object' as const, properties: { ok: { type: 'boolean' as const } }, required: ['ok'] };
  for (const { id, text, schema } of [
    { id: 'async', text: '{"ok":"wrong"}', schema: asyncSchema },
    { id: 'prototype', text: '{"__proto__":{"changed":true}}', schema: { type: 'object' as const } },
  ]) {
    let calls = 0; const events: LLMReuseEvent[] = [];
    const model = wrapped({ ...options, corpus: { ...options.corpus, generation: id }, onEvent: event => { events.push(event); } }, async () => { calls++; return result(text); });
    for (let attempt = 0; attempt < 2; attempt++) {
      try { await generateText({ model, prompt: 'object', maxRetries: 0, output: Output.object({ schema: jsonSchema({ ...schema }) }) }); }
      catch (error) { assert.ok(error instanceof Error && error.name === 'AI_NoObjectGeneratedError'); }
    }
    assert.equal(calls, 2); assert.ok(events.every(event => event.outcome === 'unrecorded'));
  }
});

test('valid-digest restored tool and structured results must pass current semantic admission before a hit', async t => {
  const { root, options } = await fixture(t);
  for (const format of ['tool', 'structured']) {
    const events: LLMReuseEvent[] = [];
    const scoped = { ...options, corpus: { ...options.corpus, generation: format } };
    const tools = { observe: tool({ inputSchema: jsonSchema({ type: 'object' }), execute: async () => 'current' }) };
    const request = format === 'tool' ? { prompt: 'poisoned', tools } : { prompt: 'poisoned', output: Output.object({ schema: jsonSchema({ type: 'object' }) }) };
    const replay = wrapped({ ...scoped, mode: 'replay', onEvent: event => { events.push(event); } }, async () => { throw new Error('replay must not execute provider'); });
    await assert.rejects(generateText({ model: replay, maxRetries: 0, ...request }), LLMReplayMissError);
    const identity = { project: options.project, compatibility: options.compatibility, key: events.at(-1)!.key! };
    const bad: LanguageModelV4GenerateResult = format === 'tool' ? { ...result(), content: [{ type: 'tool-call', toolCallId: 'bad', toolName: 'observe', input: 'not-json' }], finishReason: { unified: 'tool-calls', raw: 'tool_calls' } } : result('not-json');
    const recordedAt = Date.now(); const file = join(root, 'response.json');
    await writeFile(file, JSON.stringify({ version: 1, key: identity.key, recordedAt, expiresAt: recordedAt + options.ttlMs, result: canonicalLLMValue(bad, 10000) }));
    await new NativeCache(options.cache).save(identity, file); // valid tar, sha256 and codec, bad generation semantics
    await assert.rejects(generateText({ model: replay, maxRetries: 0, ...request }), LLMReplayMissError);
    assert.ok(events.every(event => event.outcome !== 'hit'));
    let calls = 0; const current: LLMReuseEvent[] = [];
    const fallback = wrapped({ ...scoped, onEvent: event => { current.push(event); } }, async () => { calls++; return result('{}'); });
    await generateText({ model: fallback, maxRetries: 0, ...request });
    assert.equal(calls, 1); assert.ok(current.every(event => event.outcome !== 'hit'));
  }
});
