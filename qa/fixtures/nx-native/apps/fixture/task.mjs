import assert from 'node:assert/strict';
import { appendFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';

const task = process.argv[2];
assert.ok(task === 'build' || task === 'test', 'expected build or test');
assert.ok(process.env.NX_FIXTURE_COUNTER_DIR, 'external execution counter directory is required');
const source = readFileSync('src/message.txt', 'utf8');
const dependency = JSON.parse(readFileSync('../../dependency.json', 'utf8')).value;
const environment = process.env.NX_FIXTURE_ENV ?? '';
const body = Buffer.concat([
  Buffer.from([0, 1, 127, 128, 254, 255]),
  Buffer.from(JSON.stringify({ source, dependency, environment }) + '\n'),
]);
mkdirSync(process.env.NX_FIXTURE_COUNTER_DIR, { recursive: true });
appendFileSync(resolve(process.env.NX_FIXTURE_COUNTER_DIR, 'executions.jsonl'), JSON.stringify({ task }) + '\n');
console.log(`NATIVE_NX_STDOUT task=${task} source=${source.trim()} dependency=${dependency} environment=${environment}`);
console.error(`NATIVE_NX_STDERR task=${task}`);
if (task === 'build') {
  mkdirSync('dist', { recursive: true });
  writeFileSync('dist/result.bin', body);
} else {
  assert.deepEqual(readFileSync('dist/result.bin'), body, 'build output matches declared test inputs');
  if (process.env.NX_FIXTURE_FAIL === '1') {
    console.error('NATIVE_NX_TEST_FAILURE deliberate fixture failure');
    process.exit(7);
  }
  mkdirSync('test-results', { recursive: true });
  writeFileSync('test-results/verdict.json', JSON.stringify({ passed: true, source, dependency, environment }) + '\n');
}
