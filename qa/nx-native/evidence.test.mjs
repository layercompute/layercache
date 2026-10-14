import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { writeEvidence } from './evidence.mjs';

test('export contains sanitized evidence and cannot overwrite a destination', () => {
  const root = mkdtempSync(join(tmpdir(), 'nx-evidence-test-'));
  try {
    const output = join(root, 'results.json');
    writeEvidence(output, { passed: true, nxVersion: '23.3.0', stages: [],
      root: '/private/credentials', token: 'secret-token', config: { secret: 'secret-config' }, logs: 'raw logs' });
    assert.equal(statSync(output).mode & 0o777, 0o600);
    const exported = JSON.parse(readFileSync(output, 'utf8'));
    assert.deepEqual(exported, { schemaVersion: 'nx-native-v1', passed: true, nxVersion: '23.3.0', stages: [] });
    assert.throws(() => writeEvidence(output, { passed: true }), { code: 'EEXIST' });
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
