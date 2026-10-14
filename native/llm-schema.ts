import { Ajv } from 'ajv';
import { parse } from 'secure-json-parse';
import type { LanguageModelMiddleware } from 'ai';
import { UnsupportedLLMValue } from './llm-codec.ts';

type GenerateHook = NonNullable<LanguageModelMiddleware['wrapGenerate']>;
type CallOptions = Parameters<GenerateHook>[0]['params'];
type GenerateResult = Awaited<ReturnType<GenerateHook>>;

// The middleware sees normalized Draft 7 schemas, not the caller's custom/Zod
// validators, repairs or refinements. Reuse a pinned OSS validator for this
// visible contract. Per-call compilation keeps the validator cache bounded.
export function validateLLMGeneration(result: GenerateResult, params: CallOptions): void {
  const validator = new Ajv({ strict: true, allErrors: false, validateFormats: true });
  function validateJSON(text: string, schema: unknown, emptyObject = false): void {
    let value: unknown;
    try { value = emptyObject && text.trim() === '' ? {} : parse(text); }
    catch { throw new UnsupportedLLMValue('invalid-generated-json'); }
    if (schema === undefined) return;
    try {
      const validate = validator.compile(schema as object);
      if ('$async' in validate && validate.$async) throw new UnsupportedLLMValue('unsupported-async-json-schema');
      if (!validate(value)) throw new UnsupportedLLMValue('generated-json-schema-mismatch');
    } catch (error) {
      if (error instanceof UnsupportedLLMValue) throw error;
      throw new UnsupportedLLMValue('unsupported-generated-json-schema');
    }
  }
  const names = new Map<string, Extract<NonNullable<CallOptions['tools']>[number], { type: 'function' }>>();
  for (const tool of params.tools ?? []) {
    if (tool.type !== 'function' || names.has(tool.name)) throw new UnsupportedLLMValue('unsupported-or-duplicate-tool');
    names.set(tool.name, tool);
  }
  for (const part of result.content) {
    if (part.type !== 'tool-call') continue;
    const tool = names.get(part.toolName);
    if (!tool) throw new UnsupportedLLMValue('undeclared-tool-call');
    validateJSON(part.input, tool.inputSchema, true);
  }
  // Intermediate planner tool calls may have no final structured output yet.
  if (result.finishReason.unified === 'stop' && params.responseFormat?.type === 'json') {
    const text = result.content.filter(part => part.type === 'text').map(part => part.text).join('');
    validateJSON(text, params.responseFormat.schema);
  }
}
