export declare class UnsupportedLLMValue extends Error {
}
export declare function canonicalLLMValue(value: unknown, maxBytes: number): string;
export declare function decodeLLMValue(text: string, maxBytes: number): unknown;
