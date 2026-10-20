import assert from 'node:assert/strict';
import test from 'node:test';
import { snapshotArchiveErrorMessage } from './snapshot-error.ts';

test('known client failures distinguish metadata, integrity, archive policy and admission', () => {
  const cases = [
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
  ];
  for (const [message, code] of cases) assert.equal(snapshotArchiveErrorMessage(new Error(message)), `Snapshot archive operation failed: ${code}`, message);
});

test('allowlisted filesystem, compression and archive codes discard secret-bearing fields', () => {
  const cases = [
    ['EEXIST', 'filesystem-exists'], ['ENOENT', 'filesystem-missing'],
    ['EACCES', 'filesystem-permission'], ['EPERM', 'filesystem-permission'],
    ['ENOSPC', 'filesystem-full'], ['EDQUOT', 'filesystem-full'],
    ['EROFS', 'filesystem-read-only'], ['ENOTDIR', 'filesystem-path-invalid'],
    ['EISDIR', 'filesystem-path-invalid'], ['ELOOP', 'filesystem-path-invalid'],
    ['EBUSY', 'filesystem-busy'], ['Z_DATA_ERROR', 'compression-invalid'],
    ['Z_BUF_ERROR', 'compression-invalid'], ['TAR_BAD_ARCHIVE', 'archive-invalid'],
    ['TAR_ABORT', 'archive-invalid'], ['TAR_ENTRY_ERROR', 'archive-extraction-failed'],
    ['TAR_ENTRY_UNSUPPORTED', 'archive-entry-unsupported'],
    ['ETIMEDOUT', 'timeout'], ['ABORT_ERR', 'canceled'],
  ];
  for (const [code, classification] of cases) {
    const error = Object.assign(new Error('private message Bearer secret-token', { cause: new Error('private nested cause') }), {
      code, path: '/private/path', response: { body: 'private response body' },
    });
    assert.equal(snapshotArchiveErrorMessage(error), `Snapshot archive operation failed: ${classification}`, code);
  }
  assert.equal(snapshotArchiveErrorMessage(new DOMException('private timeout details', 'TimeoutError')), 'Snapshot archive operation failed: timeout');
  assert.equal(snapshotArchiveErrorMessage(new DOMException('private abort details', 'AbortError')), 'Snapshot archive operation failed: canceled');
});

test('unknown errors and nested known causes never reveal or borrow private diagnostics', () => {
  const errors = [
    new Error('Bearer private-token /private/path response-body'),
    Object.assign(new Error('private message'), { code: 'PRIVATE_ERROR_CODE', name: 'TimeoutError' }),
    new Error('Invalid artifact digest or size: private suffix'),
    new Error('private wrapper', { cause: Object.assign(new Error('private cause'), { code: 'EACCES' }) }),
    Object.defineProperty(new Error('private message'), 'code', { get() { throw new Error('private getter details'); } }),
    { message: 'Invalid artifact digest or size', code: 'EACCES', path: '/private/path' },
    'private thrown value', null, undefined,
  ];
  for (const error of errors) assert.equal(snapshotArchiveErrorMessage(error), 'Snapshot archive operation failed');
});
