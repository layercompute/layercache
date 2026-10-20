import { createHash } from 'node:crypto';
import { constants } from 'node:fs';
import type { Stats } from 'node:fs';
import { open } from 'node:fs/promises';
import type { FileHandle } from 'node:fs/promises';
import { homedir } from 'node:os';
import { join, resolve } from 'node:path';
import { artifactKey } from './client.ts';
import type { Identity, Options } from './client.ts';

const maximumCredentialBytes = 16 * 1024;
const maximumResponseBytes = 16 * 1024;
const exchangeTimeoutMs = 10000;

export class SnapshotAuthenticationError extends Error {}

function endpointOrigin(value: string): string {
  try {
    const endpoint = new URL(value);
    if (endpoint.username || endpoint.password || endpoint.pathname !== '/' || endpoint.search || endpoint.hash ||
        (endpoint.protocol !== 'https:' && !(endpoint.protocol === 'http:' && ['127.0.0.1', '[::1]', 'localhost'].includes(endpoint.hostname)))) throw new Error();
    return endpoint.origin;
  } catch { throw new SnapshotAuthenticationError('Snapshot endpoint must be an HTTPS origin or HTTP loopback'); }
}

async function readCredential(path: string): Promise<string> {
  let file: FileHandle | undefined;
  try {
    const owner = process.geteuid?.();
    if (owner === undefined) throw new Error();
    file = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
    const valid = (info: Stats) => info.isFile() && info.uid === owner &&
      (info.mode & 0o7777) === 0o600 && info.nlink === 1 && info.size > 0 && info.size <= maximumCredentialBytes;
    if (!valid(await file.stat())) throw new Error();
    const bytes = Buffer.alloc(maximumCredentialBytes + 1);
    let length = 0;
    while (length < bytes.length) {
      const read = await file.read(bytes, length, bytes.length - length, null);
      if (!read.bytesRead) break;
      length += read.bytesRead;
    }
    if (length > maximumCredentialBytes || !valid(await file.stat())) throw new Error();
    const token = bytes.toString('utf8', 0, length).trim();
    if (!/^lcs1\.[a-z0-9][a-z0-9-]{0,62}\.[a-f0-9]{64}$/.test(token)) throw new Error();
    return token;
  } catch { throw new SnapshotAuthenticationError('Snapshot credential file must be an owned single-link regular 0600 file containing one valid credential'); }
  finally { await file?.close().catch(() => {}); }
}

async function responseJSON(response: Response): Promise<unknown> {
  if (!response.body) throw new Error();
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let length = 0;
  try {
    while (true) {
      const next = await reader.read();
      if (next.done) break;
      length += next.value.length;
      if (length > maximumResponseBytes) throw new Error();
      chunks.push(next.value);
    }
    return JSON.parse(Buffer.concat(chunks).toString('utf8'));
  } finally { await reader.cancel().catch(() => {}); }
}

// This grants one archive identity for one invocation. It never reads legacy
// token environment or accepts local cache bytes before a fresh exchange.
export async function authenticateSnapshot(input: {
  endpoint: string; credentialFile: string; identity: Identity; mode: 'read' | 'write'; cacheDir?: string;
}): Promise<Options> {
  const endpoint = endpointOrigin(input.endpoint);
  let nativeKey: string;
  try {
    nativeKey = artifactKey(input.identity);
    if (!/^[\x21-\x7e]{1,8192}$/.test(input.identity.key)) throw new Error();
  } catch { throw new SnapshotAuthenticationError('Snapshot requires an explicit valid project, compatibility and printable generation key'); }
  const bootstrap = await readCredential(input.credentialFile);
  let response: Response;
  try {
    response = await fetch(new URL('/v1/auth/snapshot/exchange', endpoint), {
      method: 'POST', redirect: 'error', signal: AbortSignal.timeout(exchangeTimeoutMs),
      headers: { Authorization: `Bearer ${bootstrap}`, 'Content-Type': 'application/json', 'X-LayerCache-Project': input.identity.project },
      body: JSON.stringify({ project: input.identity.project, compatibility: input.identity.compatibility, key: input.identity.key, mode: input.mode }),
    });
  } catch { throw new SnapshotAuthenticationError('Snapshot credential exchange unavailable'); }
  if (!response.ok) {
    await response.body?.cancel().catch(() => {});
    throw new SnapshotAuthenticationError(`Snapshot credential exchange returned HTTP ${response.status}`);
  }
  try {
    const result = await responseJSON(response) as Record<string, unknown>;
    const expires = typeof result.expiresAt === 'string' ? Date.parse(result.expiresAt) : NaN;
    const now = Date.now();
    if (result.version !== 1 || result.credentialKind !== 'shared-snapshot-bootstrap' || result.project !== input.identity.project ||
        result.compatibility !== input.identity.compatibility || result.nativeKey !== nativeKey || result.mode !== input.mode ||
        !Array.isArray(result.capabilities) || result.capabilities.length !== 1 || result.capabilities[0] !== input.mode ||
        typeof result.token !== 'string' || !/^lc2\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/.test(result.token) || result.token.length > maximumResponseBytes ||
        !Number.isFinite(expires) || expires <= now || expires > now + 15 * 60 * 1000 + 5000) throw new Error();
    const root = resolve(input.cacheDir ?? process.env.LAYER_CACHE_NATIVE_DIR ?? join(homedir(), '.cache', 'layercache', 'native-v1'));
    return { endpoint, token: result.token, cacheDir: join(root, 'snapshot-v1', createHash('sha256').update(endpoint).digest('hex')) };
  } catch { throw new SnapshotAuthenticationError('Snapshot exchange returned invalid or mismatched scoped credentials'); }
}
