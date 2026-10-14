import type { LanguageModelMiddleware } from 'ai';
type GenerateHook = NonNullable<LanguageModelMiddleware['wrapGenerate']>;
type CallOptions = Parameters<GenerateHook>[0]['params'];
type GenerateResult = Awaited<ReturnType<GenerateHook>>;
export declare function validateLLMGeneration(result: GenerateResult, params: CallOptions): void;
export {};
