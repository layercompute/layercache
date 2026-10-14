import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import test from 'node:test';
import { trackChild, stopChildren } from './processes.mjs';

test('cleanup waits for forced exit and still stops every owned child', async () => {
  const children = [];
  try {
    for (const ignoreTerm of [true, false]) {
      const child = trackChild(spawn(process.execPath, ['-e', `${ignoreTerm ? "process.on('SIGTERM', () => {});" : ''}
        process.stdout.write('ready\\n'); setInterval(() => {}, 1000);`], {
        detached: process.platform !== 'win32', stdio: ['ignore', 'pipe', 'ignore'],
      }));
      children.push(child);
      await once(child.stdout, 'data');
    }
    await assert.rejects(stopChildren(children, { graceMs: 50, killMs: 1000 }), error => {
      assert.ok(error instanceof AggregateError);
      assert.equal(error.errors.length, 1);
      assert.match(error.errors[0].message, /forced shutdown/);
      return true;
    });
    assert.equal(children[0].signalCode, 'SIGKILL');
    assert.equal(children[1].signalCode, 'SIGTERM');
    for (const child of children) {
      assert.throws(() => process.kill(child.pid, 0), { code: 'ESRCH' });
    }
  } finally {
    for (const child of children) {
      if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
    }
  }
});

for (const signal of ['SIGINT', 'SIGTERM']) {
  test(`${signal} cancellation stops all owned children within the cleanup budget`, { timeout: 5000 }, async () => {
    const module = new URL('./processes.mjs', import.meta.url).href;
    const program = `
      import { spawn } from 'node:child_process';
      import { once } from 'node:events';
      import { cleanupOnSignals, trackChild } from ${JSON.stringify(module)};
      const children = [];
      const cancellation = new AbortController();
      const finish = cleanupOnSignals(children, cancellation, { graceMs: 50, killMs: 1000 });
      cancellation.signal.addEventListener('abort', async () => {
        try { await finish(); process.exitCode = 99; }
        catch (error) { process.exitCode = error instanceof AggregateError && error.errors.length === 2 ? 0 : 98; }
      });
      for (let index = 0; index < 2; index++) {
        const child = trackChild(spawn(process.execPath, ['-e', "process.on('SIGTERM', () => {}); process.stdout.write('ready\\\\n'); setInterval(() => {}, 1000);"], {
          detached: true, stdio: ['ignore', 'pipe', 'ignore']
        }));
        children.push(child);
        await once(child.stdout, 'data');
      }
      console.log(JSON.stringify(children.map(child => child.pid)));
    `;
    const wrapper = spawn(process.execPath, ['--input-type=module', '-e', program], { stdio: ['ignore', 'pipe', 'pipe'] });
    let pids = [];
    let stderr = '';
    wrapper.stderr.on('data', chunk => { stderr += chunk; });
    try {
      const [ready] = await once(wrapper.stdout, 'data');
      pids = JSON.parse(ready.toString());
      const closed = once(wrapper, 'close');
      wrapper.kill(signal);
      const [code] = await closed;
      assert.equal(code, 0, stderr);
      for (const pid of pids) assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' });
    } finally {
      if (wrapper.exitCode === null && wrapper.signalCode === null) wrapper.kill('SIGKILL');
      for (const pid of pids) {
        try { process.kill(-pid, 'SIGKILL'); } catch (error) { if (error.code !== 'ESRCH') throw error; }
      }
    }
  });
}
