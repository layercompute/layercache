// Tagged canonical values preserve binary, undefined and Date values without
// colliding with user JSON objects. Only plain data is supported.
export class UnsupportedLLMValue extends Error {}
export function canonicalLLMValue(value: unknown, maxBytes: number): string {
  let bytes = 0;
  const seen = new Set<object>();
  const budget = (size: number) => { bytes += size; if (bytes > maxBytes) throw new UnsupportedLLMValue('size-limit'); };
  function encode(value: unknown, depth: number): unknown {
    budget(16);
    if (depth > 64) throw new UnsupportedLLMValue('depth-limit');
    if (value === undefined) return ['undefined'];
    if (value === null) return ['null'];
    if (typeof value === 'boolean') return ['boolean', value];
    if (typeof value === 'number') {
      if (!Number.isFinite(value)) throw new UnsupportedLLMValue('non-finite-number');
      return ['number', Object.is(value, -0) ? '-0' : value];
    }
    if (typeof value === 'string') {
      if (/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(value)) throw new UnsupportedLLMValue('invalid-unicode');
      budget(Buffer.byteLength(value)); return ['string', value];
    }
    if (typeof value !== 'object' || value instanceof URL) throw new UnsupportedLLMValue('unsupported-value');
    if (value instanceof Uint8Array) { budget(value.byteLength * 2); return ['bytes', Buffer.from(value.buffer, value.byteOffset, value.byteLength).toString('base64')]; }
    if (value instanceof ArrayBuffer) { budget(value.byteLength * 2); return ['array-buffer', Buffer.from(value).toString('base64')]; }
    if (value instanceof Date) {
      if (!Number.isFinite(value.getTime())) throw new UnsupportedLLMValue('invalid-date');
      return ['date', value.toISOString()];
    }
    if (seen.has(value)) throw new UnsupportedLLMValue('cyclic-value');
    seen.add(value);
    try {
      if (Array.isArray(value)) {
        if (Object.keys(value).length !== value.length) throw new UnsupportedLLMValue('sparse-or-decorated-array');
        return ['array', value.map(item => encode(item, depth + 1))];
      }
      if (![Object.prototype, null].includes(Object.getPrototypeOf(value))) throw new UnsupportedLLMValue('unsupported-object');
      if (Object.getOwnPropertySymbols(value).length) throw new UnsupportedLLMValue('symbol-property');
      const descriptors = Object.getOwnPropertyDescriptors(value);
      if (Object.values(descriptors).some(property => !('value' in property) || !property.enumerable)) throw new UnsupportedLLMValue('non-data-property');
      return ['object', Object.keys(descriptors).sort().map(key => {
        budget(Buffer.byteLength(key)); return [key, encode(descriptors[key]!.value, depth + 1)];
      })];
    } finally { seen.delete(value); }
  }
  const text = JSON.stringify(encode(value, 0));
  if (Buffer.byteLength(text) > maxBytes) throw new UnsupportedLLMValue('size-limit');
  return text;
}
export function decodeLLMValue(text: string, maxBytes: number): unknown {
  if (Buffer.byteLength(text) > maxBytes) throw new UnsupportedLLMValue('size-limit');
  function decode(node: unknown, depth: number): unknown {
    if (depth > 64 || !Array.isArray(node) || typeof node[0] !== 'string') throw new UnsupportedLLMValue('invalid-codec');
    const [tag, value] = node;
    if (tag === 'null' || tag === 'undefined') {
      if (node.length !== 1) throw new UnsupportedLLMValue('invalid-codec');
      return tag === 'null' ? null : undefined;
    }
    if (node.length !== 2) throw new UnsupportedLLMValue('invalid-codec');
    if (tag === 'string' && typeof value === 'string') return value;
    if (tag === 'boolean' && typeof value === 'boolean') return value;
    if (tag === 'number' && (typeof value === 'number' || value === '-0')) return value === '-0' ? -0 : value;
    if (tag === 'date' && typeof value === 'string') return new Date(value);
    if ((tag === 'bytes' || tag === 'array-buffer') && typeof value === 'string') {
      const bytes = Buffer.from(value, 'base64');
      if (bytes.toString('base64') !== value) throw new UnsupportedLLMValue('invalid-binary');
      return tag === 'bytes' ? new Uint8Array(bytes) : new Uint8Array(bytes).buffer;
    }
    if (tag === 'array' && Array.isArray(value)) return value.map(item => decode(item, depth + 1));
    if (tag === 'object' && Array.isArray(value)) {
      const object: Record<string, unknown> = {};
      for (const pair of value) {
        if (!Array.isArray(pair) || pair.length !== 2 || typeof pair[0] !== 'string' || Object.hasOwn(object, pair[0])) throw new UnsupportedLLMValue('invalid-object');
        Object.defineProperty(object, pair[0], { value: decode(pair[1], depth + 1), enumerable: true, writable: true, configurable: true });
      }
      return object;
    }
    throw new UnsupportedLLMValue('invalid-codec');
  }
  const value = decode(JSON.parse(text), 0);
  if (canonicalLLMValue(value, maxBytes) !== text) throw new UnsupportedLLMValue('noncanonical-codec');
  return value;
}
