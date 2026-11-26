import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import test from 'node:test';
import { readVerifiedArchive } from './xcode.mjs';

test('the Xcode reader verifies complete bounded bytes against the hosted digest', async () => {
  const body = Buffer.from('opaque Xcode archive fixture');
  const digest = createHash('sha256').update(body).digest('hex');
  const response = (headers = {}, status = 200) => new Response(body, { status, headers: {
    'content-length': String(body.length), 'x-layercache-digest': `sha256:${digest}`, ...headers,
  } });
  assert.deepEqual(await readVerifiedArchive(response()), body);
  await assert.rejects(readVerifiedArchive(response({}, 404)), /invalid-xcode-archive-response/);
  await assert.rejects(readVerifiedArchive(response({ 'content-length': String(65 * 1024 * 1024) })),
    /invalid-xcode-archive-response/);
  for (const claimed of [body.length - 1, body.length + 1]) {
    await assert.rejects(readVerifiedArchive(response({ 'content-length': String(claimed) })),
      /xcode-archive-length-mismatch/);
  }
  await assert.rejects(readVerifiedArchive(response({ 'x-layercache-digest': `sha256:${'0'.repeat(64)}` })),
    /xcode-archive-digest-mismatch/);
});
