import { createHash } from 'node:crypto';
import { mkdtemp, readFile, readdir, rm, stat, writeFile } from 'node:fs/promises';
import { homedir, tmpdir } from 'node:os';
import { join } from 'node:path';
import type { LanguageModelMiddleware } from 'ai';
type GenerateHook = NonNullable<LanguageModelMiddleware['wrapGenerate']>;
type LanguageModelV4CallOptions = Parameters<GenerateHook>[0]['params'];
type LanguageModelV4GenerateResult = Awaited<ReturnType<GenerateHook>>;
type LanguageModelV4Usage = LanguageModelV4GenerateResult['usage'];
import { NativeCache, NativeCacheConflictError, environmentOptions } from './client.ts';
import type { Identity, Options, Result } from './client.ts';
import { canonicalLLMValue, decodeLLMValue, UnsupportedLLMValue } from './llm-codec.ts';
import { validateLLMGeneration } from './llm-schema.ts';

export type LLMReuseMode = 'record' | 'replay' | 'read-through' | 'fresh';
export interface LLMReuseProfile {
  endpointIdentity: string;
  accountIdentity: string;
  providerRevision: string;
  defaultsRevision: string;
  modelRevision: string;
}
export interface LLMReuseEvent {
  mode: LLMReuseMode;
  outcome: 'hit' | 'recorded' | 'fresh' | 'bypass' | 'miss' | 'conflict' | 'unrecorded';
  key?: string;
  source?: Result['source'];
  reason?: string;
  recordedAt?: number;
  expiresAt?: number;
  currentProviderCalls: number;
  currentUsage?: LanguageModelV4Usage;
  historicalUsage?: LanguageModelV4Usage;
}
export interface LLMReuseOptions {
  project: string;
  compatibility: string;
  scope: 'personal' | 'team';
  profile: LLMReuseProfile;
  context: unknown;
  corpus: { id: string; generation: string };
  sampleSlot: string;
  mode: LLMReuseMode;
  ttlMs: number;
  cache?: Options;
  maxEntryBytes?: number;
  maxInputBytes?: number;
  onEvent?: (event: LLMReuseEvent) => void | Promise<void>;
}
interface Envelope {
  version: 1;
  key: string;
  recordedAt: number;
  expiresAt: number;
  result: string;
}
export class LLMReplayMissError extends Error {
  constructor(reason: string) { super(`LLM replay unavailable: ${reason}`); this.name = 'LLMReplayMissError'; }
}
const requestFields = new Set(['prompt', 'maxOutputTokens', 'temperature', 'stopSequences', 'topP', 'topK', 'presencePenalty', 'frequencyPenalty', 'responseFormat', 'seed', 'tools', 'toolChoice', 'includeRawChunks', 'abortSignal', 'headers', 'reasoning', 'providerOptions']);
const resultFields = new Set(['content', 'finishReason', 'usage', 'providerMetadata', 'request', 'response', 'warnings']);
const zeroUsage = (): LanguageModelV4Usage => ({ inputTokens: { total: 0, noCache: 0, cacheRead: 0, cacheWrite: 0 }, outputTokens: { total: 0, text: 0, reasoning: 0 } });
const sha = (value: string) => createHash('sha256').update(value).digest('hex');
function required(value: unknown, name: string): asserts value is string {
  if (typeof value !== 'string' || !value.trim() || value.length > 2048) throw new Error(`${name} must be an explicit nonempty identity`);
}
function positive(value: number, name: string) {
  if (!Number.isSafeInteger(value) || value <= 0) throw new Error(`${name} must be a positive integer`);
}
function supportedResult(value: unknown): LanguageModelV4GenerateResult {
  if (!value || typeof value !== 'object') throw new UnsupportedLLMValue('invalid-result');
  if (![Object.prototype, null].includes(Object.getPrototypeOf(value))) throw new UnsupportedLLMValue('unsupported-result-object');
  const result = value as LanguageModelV4GenerateResult;
  if (Object.keys(result).some(key => !resultFields.has(key)) || !Array.isArray(result.content) || !Array.isArray(result.warnings) ||
      (result.finishReason?.raw !== undefined && typeof result.finishReason.raw !== 'string') || !['stop', 'tool-calls'].includes(result.finishReason?.unified) || !result.usage?.inputTokens || !result.usage.outputTokens) throw new UnsupportedLLMValue('incomplete-or-unsupported-result');
  for (const counts of [result.usage.inputTokens, result.usage.outputTokens]) {
    if (Object.values(counts).some(count => count !== undefined && (!Number.isSafeInteger(count) || count < 0))) throw new UnsupportedLLMValue('invalid-usage');
  }
  const types = new Set(['text', 'reasoning', 'file', 'reasoning-file', 'tool-call', 'tool-result', 'tool-approval-request', 'custom', 'source']);
  for (const part of result.content) {
    if (!part || !types.has(part.type) || ('preliminary' in part && part.preliminary) || ('isError' in part && part.isError) || ('providerExecuted' in part && part.providerExecuted)) throw new UnsupportedLLMValue('incomplete-content');
    if ((part.type === 'text' || part.type === 'reasoning') && typeof part.text !== 'string') throw new UnsupportedLLMValue('invalid-text');
    if (part.type === 'tool-call' && (typeof part.toolCallId !== 'string' || typeof part.toolName !== 'string' || typeof part.input !== 'string')) throw new UnsupportedLLMValue('invalid-tool-call');
    if (part.type === 'tool-result' && (typeof part.toolCallId !== 'string' || typeof part.toolName !== 'string' || part.result == null)) throw new UnsupportedLLMValue('invalid-tool-result');
    if (part.type === 'tool-approval-request' && (typeof part.approvalId !== 'string' || typeof part.toolCallId !== 'string')) throw new UnsupportedLLMValue('invalid-tool-approval');
    if (part.type === 'custom' && (typeof part.kind !== 'string' || !part.kind.includes('.'))) throw new UnsupportedLLMValue('invalid-custom-content');
    if (part.type === 'source' && (typeof part.id !== 'string' || !['url', 'document'].includes(part.sourceType))) throw new UnsupportedLLMValue('invalid-source');
    if (part.type === 'file' || part.type === 'reasoning-file') {
      if (part.data?.type !== 'data' || typeof part.mediaType !== 'string' || !(typeof part.data.data === 'string' || part.data.data instanceof Uint8Array)) throw new UnsupportedLLMValue('mutable-or-invalid-output-file');
    }
  }
  return result;
}
function credentialFields(value: unknown): void {
  if (!value || typeof value !== 'object' || value instanceof Uint8Array) return;
  for (const [key, item] of Object.entries(value)) {
    if (/^(authorization|proxy-authorization|x-api-key|api[-_]?key|cookie|set-cookie|accessToken|refreshToken)$/i.test(key)) throw new UnsupportedLLMValue('credential-field');
    credentialFields(item);
  }
}
function validateRequestData(params: LanguageModelV4CallOptions): void {
  credentialFields(params.headers); credentialFields(params.providerOptions);
  if (params.tools?.some(tool => tool.type === 'provider')) throw new UnsupportedLLMValue('provider-executed-tools-unsupported');
  function visit(value: unknown): void {
    if (!value || typeof value !== 'object' || value instanceof Uint8Array) return;
    const part = value as { type?: unknown; data?: { type?: unknown }; providerOptions?: unknown };
    if ((part.type === 'file' || part.type === 'reasoning-file') && ['url', 'reference'].includes(String(part.data?.type))) throw new UnsupportedLLMValue('mutable-media-reference');
    credentialFields(part.providerOptions);
    for (const item of Object.values(value)) visit(item);
  }
  visit(params.prompt);
}
function storedResult(result: LanguageModelV4GenerateResult): LanguageModelV4GenerateResult {
  // Transport debug bodies and headers may include credentials. Preserve only
  // the standard response identity and the provider's semantic result metadata.
  credentialFields(result.providerMetadata);
  const response = result.response && { id: result.response.id, timestamp: result.response.timestamp, modelId: result.response.modelId };
  return { content: result.content, finishReason: result.finishReason, usage: result.usage, warnings: result.warnings,
    ...(result.providerMetadata ? { providerMetadata: result.providerMetadata } : {}), ...(response ? { response } : {}) };
}
function metadata(result: LanguageModelV4GenerateResult, event: LLMReuseEvent): LanguageModelV4GenerateResult {
  const details = JSON.parse(JSON.stringify(event));
  return { ...result, providerMetadata: { ...result.providerMetadata, layercache: details } };
}

/** Completed AI SDK v4 generations only. Put this middleware last, nearest the
 * provider, after request transformations. Provider defaults belong in profile. */
export function createLLMReuseMiddleware(options: LLMReuseOptions): LanguageModelMiddleware {
  options = { ...options, profile: { ...options.profile }, corpus: { ...options.corpus }, cache: options.cache && { ...options.cache } };
  required(options.project, 'project'); required(options.compatibility, 'compatibility'); required(options.sampleSlot, 'sampleSlot');
  required(options.corpus?.id, 'corpus.id'); required(options.corpus?.generation, 'corpus.generation');
  for (const field of ['endpointIdentity', 'accountIdentity', 'providerRevision', 'defaultsRevision', 'modelRevision'] as const) required(options.profile?.[field], `profile.${field}`);
  if (!['personal', 'team'].includes(options.scope) || !['record', 'replay', 'read-through', 'fresh'].includes(options.mode)) throw new Error('Explicit private scope and reuse mode required');
  if (options.context === undefined) throw new Error('Explicit full input-bearing context required');
  positive(options.ttlMs, 'ttlMs');
  const maxEntryBytes = options.maxEntryBytes ?? 4 * 1024 ** 2;
  const maxInputBytes = options.maxInputBytes ?? 8 * 1024 ** 2;
  positive(maxEntryBytes, 'maxEntryBytes'); positive(maxInputBytes, 'maxInputBytes');
  // Snapshot identities so callers cannot change the declared profile while a
  // provider request is in flight. Credentials remain only in NativeCache options.
  const declared = decodeLLMValue(canonicalLLMValue({ profile: options.profile, context: options.context, corpus: options.corpus, sampleSlot: options.sampleSlot, scope: options.scope }, maxInputBytes), maxInputBytes);
  const storage = options.cache?.token ? options.cache : environmentOptions(options.cache);
  const cache = new NativeCache({ ...storage, cacheDir: options.cache?.cacheDir ?? process.env.LAYER_CACHE_LLM_DIR ?? join(homedir(), '.cache', 'layercache', 'llm-v1'), maxBytes: options.cache?.maxBytes ?? 64 * 1024 ** 2 });
  const emit = (event: LLMReuseEvent) => {
    try { const pending = options.onEvent?.(structuredClone(event)); if (pending) void Promise.resolve(pending).catch(() => {}); } catch { /* Observability cannot change a provider outcome. */ }
  };
  async function read(identity: Identity, signal?: AbortSignal): Promise<{ envelope: Envelope; result: LanguageModelV4GenerateResult; source: Result['source'] } | undefined> {
    signal?.throwIfAborted();
    const directory = await mkdtemp(join(tmpdir(), 'layercache-llm-read-'));
    try {
      const destination = join(directory, 'recording');
      const restored = await cache.restore(identity, destination, signal);
      signal?.throwIfAborted();
      if (!restored.hit) return undefined;
      if ((await readdir(destination)).join() !== 'response.json') throw new Error('invalid-envelope');
      const path = join(destination, 'response.json');
      if ((await stat(path)).size > maxEntryBytes) throw new Error('size-limit');
      const envelope: Envelope = JSON.parse(await readFile(path, 'utf8'));
      if (Object.keys(envelope).sort().join() !== 'expiresAt,key,recordedAt,result,version' || envelope.version !== 1 || envelope.key !== identity.key ||
          !Number.isSafeInteger(envelope.recordedAt) || !Number.isSafeInteger(envelope.expiresAt) || envelope.recordedAt > Date.now() ||
          envelope.expiresAt <= envelope.recordedAt || envelope.expiresAt - envelope.recordedAt !== options.ttlMs || Date.now() >= envelope.expiresAt || typeof envelope.result !== 'string') return undefined;
      const result = supportedResult(decodeLLMValue(envelope.result, maxEntryBytes));
      return { envelope, result, source: restored.source };
    } finally { await rm(directory, { recursive: true, force: true }); }
  }
  return {
    specificationVersion: 'v4',
    async wrapStream({ doStream }) {
      if (options.mode === 'replay' || options.mode === 'record') throw new LLMReplayMissError('streaming-unsupported');
      emit({ mode: options.mode, outcome: 'bypass', reason: 'streaming', currentProviderCalls: 1 });
      return doStream();
    },
    async wrapGenerate({ params, model, doGenerate }) {
      params.abortSignal?.throwIfAborted();
      if (options.mode === 'fresh') {
        const result = await doGenerate(); params.abortSignal?.throwIfAborted();
        const event: LLMReuseEvent = { mode: options.mode, outcome: 'fresh', currentProviderCalls: 1, currentUsage: result.usage };
        emit(event); return metadata(result, event);
      }
      let identity: Identity;
      try {
        if (![Object.prototype, null].includes(Object.getPrototypeOf(params)) || Object.getOwnPropertySymbols(params).length || Object.values(Object.getOwnPropertyDescriptors(params)).some(property => !('value' in property) || !property.enumerable)) throw new UnsupportedLLMValue('unsupported-request-object');
        if (Object.keys(params).some(key => !requestFields.has(key))) throw new UnsupportedLLMValue('unknown-request-option');
        validateRequestData(params);
        const { abortSignal: _signal, ...request } = params;
        identity = { project: options.project, compatibility: options.compatibility, key: sha(canonicalLLMValue({ version: 3, validation: 'ajv-8.17.1-draft7-sync-secure-json-4.1.0-v1', operation: 'generate', provider: model.provider, modelId: model.modelId, declared, compatibility: options.compatibility, request, ttlMs: options.ttlMs }, maxInputBytes)) };
      } catch (error) {
        const reason = error instanceof UnsupportedLLMValue ? error.message : 'unsupported-request';
        if (options.mode === 'replay' || options.mode === 'record') {
          emit({ mode: options.mode, outcome: 'bypass', reason, currentProviderCalls: 0 });
          throw new LLMReplayMissError(reason);
        }
        const result = await doGenerate(); params.abortSignal?.throwIfAborted();
        const event: LLMReuseEvent = { mode: options.mode, outcome: 'bypass', reason, currentProviderCalls: 1, currentUsage: result.usage };
        emit(event); return metadata(result, event);
      }
      const replay = (found: NonNullable<Awaited<ReturnType<typeof read>>>, currentUsage?: LanguageModelV4Usage) => {
        validateLLMGeneration(found.result, params);
        const event: LLMReuseEvent = { mode: options.mode, outcome: 'hit', key: identity.key, source: found.source,
          recordedAt: found.envelope.recordedAt, expiresAt: found.envelope.expiresAt, currentProviderCalls: currentUsage ? 1 : 0,
          currentUsage: currentUsage ?? zeroUsage(), historicalUsage: found.result.usage };
        emit(event); return metadata({ ...found.result, usage: currentUsage ?? zeroUsage() }, event);
      };
      if (options.mode === 'replay' || options.mode === 'read-through') {
        try { const found = await read(identity, params.abortSignal); if (found) return replay(found); }
        catch (error) { params.abortSignal?.throwIfAborted(); if (options.mode === 'replay') throw new LLMReplayMissError('invalid-or-unavailable-recording'); }
        if (options.mode === 'replay') {
          emit({ mode: options.mode, outcome: 'miss', key: identity.key, currentProviderCalls: 0 });
          throw new LLMReplayMissError('missing-or-expired-recording');
        }
      }
      const result = await doGenerate();
      params.abortSignal?.throwIfAborted();
      const recordedAt = Date.now();
      const expiresAt = recordedAt + options.ttlMs;
      let directory: string | undefined;
      try {
        if (!Number.isSafeInteger(expiresAt)) throw new UnsupportedLLMValue('invalid-expiry');
        const supported = supportedResult(result);
        // Bound the output before running syntax/schema admission.
        const encoded = canonicalLLMValue(storedResult(supported), maxEntryBytes);
        validateLLMGeneration(supported, params);
        const envelope: Envelope = { version: 1, key: identity.key, recordedAt, expiresAt, result: encoded };
        const bytes = JSON.stringify(envelope);
        if (Buffer.byteLength(bytes) > maxEntryBytes) throw new UnsupportedLLMValue('size-limit');
        directory = await mkdtemp(join(tmpdir(), 'layercache-llm-write-'));
        const path = join(directory, 'response.json');
        await writeFile(path, bytes, { mode: 0o600 });
        params.abortSignal?.throwIfAborted();
        const saved = await cache.saveImmutable(identity, path, params.abortSignal);
        params.abortSignal?.throwIfAborted();
        const event: LLMReuseEvent = { mode: options.mode, outcome: 'recorded', key: identity.key, source: saved.source,
          recordedAt, expiresAt, currentProviderCalls: 1, currentUsage: result.usage };
        emit(event); return metadata(result, event);
      } catch (error) {
        params.abortSignal?.throwIfAborted();
        if (error instanceof NativeCacheConflictError) {
          emit({ mode: options.mode, outcome: 'conflict', key: identity.key, currentProviderCalls: 1, currentUsage: result.usage });
          if (options.mode === 'record') throw error;
          try { const winner = await read(identity, params.abortSignal); if (winner) return replay(winner, result.usage); } catch { params.abortSignal?.throwIfAborted(); }
          const event: LLMReuseEvent = { mode: options.mode, outcome: 'unrecorded', reason: 'conflicting-winner-expired-or-unavailable', key: identity.key, currentProviderCalls: 1, currentUsage: result.usage };
          emit(event); return metadata(result, event);
        }
        const event: LLMReuseEvent = { mode: options.mode, outcome: 'unrecorded', key: identity.key,
          reason: error instanceof UnsupportedLLMValue ? error.message : 'cache-write-failed', currentProviderCalls: 1, currentUsage: result.usage };
        emit(event);
        if (options.mode === 'record') throw new Error('LLM recording could not be committed');
        return metadata(result, event);
      } finally { if (directory) await rm(directory, { recursive: true, force: true }); }
    },
  };
}
