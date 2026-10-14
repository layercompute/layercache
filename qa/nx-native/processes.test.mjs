import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { trackChild, stopChild, stopChildren } from './processes.mjs';

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

test('cleanup escalates a surviving descendant after its process-group leader closes', {
  skip: process.platform !== 'linux', timeout: 15000,
}, async () => {
  const module = new URL('./processes.mjs', import.meta.url).href;
  const descendant = "process.on('SIGTERM', () => {}); process.send('ready'); setInterval(() => {}, 1000);";
  const leader = `
    const { spawn } = require('node:child_process');
    const child = spawn(process.execPath, ['-e', ${JSON.stringify(descendant)}], {
      stdio: ['ignore', 'ignore', 'ignore', 'ipc']
    });
    child.once('message', () => { console.log(child.pid); child.disconnect(); });
    setInterval(() => {}, 1000);
  `;
  const controller = `
    import assert from 'node:assert/strict';
    import { spawn } from 'node:child_process';
    import { once } from 'node:events';
    import { setTimeout as delay } from 'node:timers/promises';
    import { stopChild, trackChild } from ${JSON.stringify(module)};
    const child = trackChild(spawn(process.execPath, ['-e', ${JSON.stringify(leader)}], {
      detached: true, stdio: ['ignore', 'pipe', 'ignore']
    }));
    try {
      const [ready] = await once(child.stdout, 'data');
      const descendantPID = Number(ready.toString().trim());
      await assert.rejects(stopChild(child, { graceMs: 50, killMs: 1000 }), /forced shutdown/);
      assert.equal(child.signalCode, 'SIGTERM');
      assert.throws(() => process.kill(descendantPID, 0), { code: 'ESRCH' });
      assert.throws(() => process.kill(-child.pid, 0), { code: 'ESRCH' });
    } finally {
      try { process.kill(-child.pid, 'SIGKILL'); } catch (error) { if (error.code !== 'ESRCH') throw error; }
      const deadline = Date.now() + 1000;
      while (Date.now() < deadline) {
        try { process.kill(-child.pid, 0); }
        catch (error) { if (error.code === 'ESRCH') break; throw error; }
        await delay(10);
      }
    }
  `;
  // The outer fixture adopts and reaps the orphan after its leader exits.
  // A container's PID 1 need not reap it, which would leave killpg(0) true for
  // a zombie and make a correct cleanup appear to time out.
  const fixture = trackChild(spawn('python3', [fileURLToPath(new URL('./reap-descendants.py', import.meta.url)),
    process.execPath, '--input-type=module', '-e', controller], { detached: true, stdio: ['ignore', 'ignore', 'pipe'] }));
  let stderr = '';
  fixture.stderr.on('data', chunk => { stderr += chunk; });
  try {
    const [code] = await once(fixture, 'close');
    assert.equal(code, 0, stderr);
  } finally {
    await stopChild(fixture, { graceMs: 3000, killMs: 1000 });
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
