// Scoped snapshot diagnostics never forward raw messages, paths, response bodies
// or nested causes. Exact client literals and known library codes are the only
// inputs to these fixed classifications; unknown errors stay generic.
const clientFailures = new Map<string, string>([
  ['Invalid artifact digest or size', 'metadata-invalid'],
  ['Artifact exceeds declared size', 'integrity-failed'],
  ['Artifact integrity check failed', 'integrity-failed'],
  ['Duplicate or case-colliding archive path', 'archive-path-collision'],
  ['Unsafe archive path', 'archive-path-unsafe'],
  ['Unsupported archive entry', 'archive-entry-unsupported'],
  ['Special permission bits are not cacheable', 'archive-permissions-unsupported'],
  ['Empty archive link', 'archive-link-unsafe'],
  ['Unsafe archive link', 'archive-link-unsafe'],
  ['Archive writes through a symlink', 'archive-link-unsafe'],
  ['Unsafe archive link chain', 'archive-link-unsafe'],
  ['Cyclic archive link', 'archive-link-cycle'],
  ['Empty artifact archive', 'archive-empty'],
  ['Invalid archive', 'archive-invalid'],
  ['Too many archive entries', 'archive-entry-limit'],
  ['Artifact exceeds local cache budget', 'cache-budget-exceeded'],
  ['Expanded artifact exceeds local cache budget', 'cache-budget-exceeded'],
  ['Insufficient local cache budget', 'cache-budget-exceeded'],
  ['Insufficient local cache budget: live Expo consumers are pinned', 'cache-budget-exceeded'],
  ['Native cache is busy; inspect its .lock directory', 'cache-busy'],
  ['Cannot replace native artifact backing live Expo consumers; use a complete build key', 'cache-busy'],
  ['Build must be a directory or regular file', 'input-unsupported'],
  ['Unsupported build file', 'input-unsupported'],
  ['Too many build files', 'input-unsupported'],
  ['maxBytes must be positive integer bytes', 'configuration-invalid'],
  ['maxAgeMs must be positive integer milliseconds', 'configuration-invalid'],
]);

const libraryFailures = new Map<string, string>([
  ['EEXIST', 'filesystem-exists'],
  ['ENOENT', 'filesystem-missing'],
  ['EACCES', 'filesystem-permission'],
  ['EPERM', 'filesystem-permission'],
  ['ENOSPC', 'filesystem-full'],
  ['EDQUOT', 'filesystem-full'],
  ['EROFS', 'filesystem-read-only'],
  ['ENOTDIR', 'filesystem-path-invalid'],
  ['EISDIR', 'filesystem-path-invalid'],
  ['ELOOP', 'filesystem-path-invalid'],
  ['EBUSY', 'filesystem-busy'],
  ['Z_DATA_ERROR', 'compression-invalid'],
  ['Z_BUF_ERROR', 'compression-invalid'],
  ['TAR_BAD_ARCHIVE', 'archive-invalid'],
  ['TAR_ABORT', 'archive-invalid'],
  ['TAR_ENTRY_ERROR', 'archive-extraction-failed'],
  ['TAR_ENTRY_UNSUPPORTED', 'archive-entry-unsupported'],
  ['ETIMEDOUT', 'timeout'],
  ['ABORT_ERR', 'canceled'],
]);

export function snapshotArchiveErrorMessage(error: unknown): string {
  let classification: string | undefined;
  try {
    if (error instanceof Error) {
      classification = clientFailures.get(error.message);
      if (!classification && 'code' in error && typeof error.code === 'string') classification = libraryFailures.get(error.code);
      if (!classification && error instanceof DOMException) {
        if (error.name === 'TimeoutError') classification = 'timeout';
        else if (error.name === 'AbortError') classification = 'canceled';
      }
    }
  } catch { classification = undefined; }
  return classification ? `Snapshot archive operation failed: ${classification}` : 'Snapshot archive operation failed';
}
