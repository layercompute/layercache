// Wire format and failure tracking for moving hosted object bytes directly with
// object storage. client.ts deliberately re-exports nothing from here, so the
// breaker reset stays out of the published package.

export const TRANSFER_MEDIA_TYPE = 'application/vnd.layercache.transfer.v1+json';

const MAX_TRANSFER_BODY_BYTES = 64 << 10;
const BREAKER_THRESHOLD = 3;
const BREAKER_COOLDOWN_MS = 10 * 60_000;
// One path segment. Printable ASCII, and never a dot segment that URL parsing
// would collapse into a different route.
const TRANSFER_ID = /^(?!\.{1,2}$)[!-~]{1,128}$/;
const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/i;

export interface ReadDescriptor { url: URL; digest: string; size: number; lease: string }
export interface UploadGrant { url: URL; headers: Record<string, string>; upload: string }

// Per process, shared by every NativeCache: a broken object storage path is a
// property of the host, not of one client.
let failures = 0;
let openUntil = 0;
export const directBreaker = {
  allow(): boolean { return Date.now() >= openUntil; },
  fail(): void {
    failures += 1;
    if (failures >= BREAKER_THRESHOLD) { failures = 0; openUntil = Date.now() + BREAKER_COOLDOWN_MS; }
  },
  succeed(): void { failures = 0; },
  reset(): void { failures = 0; openUntil = 0; },
};

export function isDescriptorResponse(response: Response): boolean {
  if (response.status !== 200) return false;
  const [mediaType] = (response.headers.get('content-type') ?? '').split(';', 1);
  return mediaType?.trim().toLowerCase() === TRANSFER_MEDIA_TYPE;
}

function objectUrl(raw: unknown): URL | null {
  if (typeof raw !== 'string') return null;
  let url: URL;
  try { url = new URL(raw); } catch { return null; }
  if (url.username || url.password || url.hash) return null;
  const loopback = url.hostname === 'localhost' || url.hostname === '[::1]' || /^127\.\d+\.\d+\.\d+$/.test(url.hostname);
  return url.protocol === 'https:' || (url.protocol === 'http:' && loopback) ? url : null;
}

// Returns null for an oversized or unparseable body. A read failure rejects.
async function readCapped(body: ReadableStream<Uint8Array> | null): Promise<unknown> {
  if (!body) return null;
  const chunks: Uint8Array[] = [];
  let total = 0;
  for await (const chunk of body) {
    total += chunk.length;
    if (total > MAX_TRANSFER_BODY_BYTES) return null;
    chunks.push(chunk);
  }
  try { return JSON.parse(Buffer.concat(chunks).toString('utf8')); } catch { return null; }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

// Returns null for an invalid descriptor. A read failure rejects.
export async function readDescriptor(body: ReadableStream<Uint8Array> | null, maxBytes: number): Promise<ReadDescriptor | null> {
  const value = await readCapped(body);
  if (!isRecord(value)) return null;
  const { transfer, method, digest, size, expiresAt, lease } = value;
  if (transfer !== 'direct-v1' || method !== 'GET' || typeof digest !== 'string' || !/^sha256:[a-f0-9]{64}$/.test(digest) ||
      typeof size !== 'number' || !Number.isSafeInteger(size) || size <= 0 || size > maxBytes ||
      typeof lease !== 'string' || !TRANSFER_ID.test(lease) ||
      typeof expiresAt !== 'string' || !RFC3339.test(expiresAt) || !(Date.parse(expiresAt) > Date.now())) return null;
  const url = objectUrl(value.url);
  return url && { url, digest, size, lease };
}

// Returns null for a malformed answer. A read failure rejects. The signed
// Content-Length is checked and removed because fetch derives it from the body.
export async function readUploadGrant(body: ReadableStream<Uint8Array> | null, size: number): Promise<UploadGrant | null> {
  const value = await readCapped(body);
  if (!isRecord(value) || typeof value.upload !== 'string' || !TRANSFER_ID.test(value.upload)) return null;
  const url = objectUrl(value.url);
  const declared = value.headers ?? {};
  if (!url || !isRecord(declared)) return null;
  const headers: Record<string, string> = {};
  for (const [name, header] of Object.entries(declared)) {
    if (typeof header !== 'string') return null;
    if (name.toLowerCase() !== 'content-length') headers[name] = header;
    else if (header !== String(size)) return null;
  }
  return { url, headers, upload: value.upload };
}
