import type { LanguageModelMiddleware } from 'ai';
type GenerateHook = NonNullable<LanguageModelMiddleware['wrapGenerate']>;
type LanguageModelV4GenerateResult = Awaited<ReturnType<GenerateHook>>;
type LanguageModelV4Usage = LanguageModelV4GenerateResult['usage'];
import type { Options, Result } from './client.js';
export { NativeCacheConflictError, NativeCachePublicationError } from './client.js';
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
    corpus: {
        id: string;
        generation: string;
    };
    sampleSlot: string;
    mode: LLMReuseMode;
    ttlMs: number;
    cache?: Options;
    maxEntryBytes?: number;
    maxInputBytes?: number;
    onEvent?: (event: LLMReuseEvent) => void | Promise<void>;
}
export declare class LLMReplayMissError extends Error {
    constructor(reason: string);
}
/** Completed AI SDK v4 generations only. Put this middleware last, nearest the
 * provider, after request transformations. Provider defaults belong in profile. */
export declare function createLLMReuseMiddleware(options: LLMReuseOptions): LanguageModelMiddleware;
