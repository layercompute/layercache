import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { setTimeout as delay } from 'node:timers/promises';

const binary = process.env.LAYERCACHE_BIN;
assert.ok(binary && path.isAbsolute(binary), 'Set an absolute LAYERCACHE_BIN path');

type ProcessRecord = { command: string; pid: number; parent: number; child?: number };
function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    if (process.platform === 'linux') {
      const stat = fs.readFileSync(`/proc/${pid}/stat`, 'utf8');
      // An orphan that exited is stopped even before init reaps its PID.
      if (['Z', 'X'].includes(stat.slice(stat.lastIndexOf(')') + 2).split(' ')[0])) return false;
    }
    return true;
  } catch { return false; }
}

for (const signal of ['SIGINT', 'SIGTERM'] as const) {
  test(`Turbo acceptance cleans up real services after ${signal}`, { timeout: 30_000 }, async () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'layercache-turbo-interruption-'));
    const recordFile = path.join(root, 'processes.jsonl');
    const wrapper = path.join(root, 'layercache');
    const heldTurbo = path.join(root, 'turbo');
    // Real CLI operations launch both cache services. Hold the build command
    // open so the signal arrives while the harness is waiting for a child.
    fs.writeFileSync(wrapper, `#!/usr/bin/env node
const { spawn } = require('node:child_process');
const { appendFileSync } = require('node:fs');
const args = process.argv.slice(2);
const record = { command: args[0], pid: process.pid, parent: process.ppid };
const child = spawn(process.env.QA_REAL_CLI, args, { stdio: 'inherit' });
appendFileSync(process.env.QA_PROCESS_RECORD, JSON.stringify({ ...record, child: child.pid }) + '\\n');
process.on('SIGINT', () => child.kill('SIGINT'));
process.on('SIGTERM', () => child.kill('SIGTERM'));
child.once('error', () => process.exit(1));
child.once('exit', code => process.exit(code ?? 1));
`, { mode: 0o700 });
    fs.writeFileSync(heldTurbo, `#!/usr/bin/env node
const { appendFileSync } = require('node:fs');
appendFileSync(process.env.QA_PROCESS_RECORD, JSON.stringify({ command: 'turbo-build', pid: process.pid, parent: process.ppid }) + '\\n');
process.on('SIGTERM', () => setTimeout(() => process.exit(0), 200));
setInterval(() => {}, 1000);
`, { mode: 0o700 });
    let log = '';
    const harness = spawn(process.execPath, [path.resolve('qa/cloud/turbo-restore.ts')], {
      detached: true, stdio: ['ignore', 'pipe', 'pipe'],
      env: { ...process.env, LAYERCACHE_BIN: wrapper, TURBO_BIN: heldTurbo, QA_REAL_CLI: binary,
        QA_PROCESS_RECORD: recordFile, QA_OUTPUT: path.join(root, 'evidence') },
    });
    harness.stdout.on('data', chunk => { log += chunk; });
    harness.stderr.on('data', chunk => { log += chunk; });
    const exited = once(harness, 'exit');
    let records: ProcessRecord[] = [];
    try {
      for (let i = 0; i < 200; i++) {
        records = fs.existsSync(recordFile) ? fs.readFileSync(recordFile, 'utf8').trim().split('\n').map(line => JSON.parse(line) as ProcessRecord) : [];
        if (records.some(record => record.command === 'turbo-build')) break;
        assert.equal(harness.exitCode, null, `harness exited before the build: ${log}`);
        await delay(50);
      }
      const cloud = records.find(record => record.command === 'serve-cloud');
      const daemon = records.find(record => record.command === 'serve');
      const build = records.find(record => record.command === 'run');
      const turbo = records.find(record => record.command === 'turbo-build');
      assert.ok(cloud?.child && daemon?.child && build?.child && turbo, `real services/build were not started: ${log}`);
      const owned = [cloud.parent, cloud.pid, cloud.child, daemon.pid, daemon.child, build.pid, build.child, turbo.pid];
      assert.ok(owned.every(alive), 'service processes exited before interruption');
      harness.kill(signal);
      const [code, exitSignal] = await Promise.race([
        exited,
        delay(15_000, undefined, { ref: false }).then(() => { throw new Error(`harness ignored ${signal}: ${log}`); }),
      ]);
      for (let i = 0; i < 20 && owned.some(alive); i++) await delay(10);
      assert.deepEqual(owned.filter(alive), [], 'harness exited before its fixture, cloud, daemon, and build stopped');
      if (code === null) assert.equal(exitSignal, signal);
      else assert.notEqual(code, 0, 'interrupted harness exited successfully');
      assert.equal(fs.existsSync(path.join(root, 'evidence/turbo-restore.json')), false, 'interrupted build reported success');
    } finally {
      // Each test owns a separate process group, including any descendants
      // orphaned by a regression in the harness under test.
      for (const pid of [harness.pid, ...records.filter(record => record.command === 'run').map(record => record.pid)]) {
        if (pid) { try { process.kill(-pid, 'SIGKILL'); } catch { /* Already stopped. */ } }
      }
      await exited;
      fs.rmSync(root, { recursive: true, force: true });
    }
  });
}
