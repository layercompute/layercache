import { build } from 'esbuild';
import { execFileSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { readFile, readdir, writeFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { buildExecutionWorker } from '../action/setup/build-execution-worker.mjs';

await buildExecutionWorker();
await buildExecutionWorker('action/native/dist/execution-worker.cjs');

for (const [entry, outfile, format] of [
  ['native/expo.ts', 'native/dist/expo.cjs', 'cjs'],
  ['native/client.ts', 'native/dist/client.js', 'esm'],
  ['native/llm.ts', 'native/dist/llm.js', 'esm'],
  ['native/cli.ts', 'native/dist/cli.js', 'esm'],
  ['action/native/main.ts', 'action/native/dist/main.cjs', 'cjs'],
] as const) {
  await build({ entryPoints: [entry], outfile, format, bundle: true, platform: 'node', target: 'node24',
    external: ['ai', '@ai-sdk/provider'], legalComments: 'eof', banner: entry === 'native/cli.ts' ? { js: '#!/usr/bin/env node' } : undefined });
}
const require = createRequire(import.meta.url);
const dependency = createRequire(require.resolve('tar/package.json'));
const notices: string[] = [];
for (const name of ['tar', '@isaacs/fs-minipass', 'chownr', 'minipass', 'minizlib', 'yallist']) {
  const directory = dirname(dependency.resolve(`${name}/package.json`));
  const license = (await readdir(directory)).find(name => /^license/i.test(name));
  if (!license) throw new Error(`Missing bundled dependency license: ${name}`);
  notices.push(`${name}\n${await readFile(join(directory, license), 'utf8')}`);
}
await writeFile('action/native/dist/THIRD_PARTY_LICENSES.txt', notices.join('\n\n'));
const llmDependency = createRequire(require.resolve('ajv/package.json'));
for (const name of ['ajv', 'fast-deep-equal', 'fast-uri', 'json-schema-traverse', 'require-from-string', 'secure-json-parse']) {
  const directory = dirname((name === 'secure-json-parse' ? require : llmDependency).resolve(`${name}/package.json`));
  const license = (await readdir(directory)).find(name => /^license/i.test(name));
  if (!license) throw new Error(`Missing bundled dependency license: ${name}`);
  notices.push(`${name}\n${await readFile(join(directory, license), 'utf8')}`);
}
await writeFile('native/dist/THIRD_PARTY_LICENSES.txt', notices.join('\n\n'));

execFileSync(process.execPath, ['node_modules/typescript/bin/tsc', '-p', 'native/tsconfig.types.json'], { stdio: 'inherit' });
for (const file of await readdir('native/dist/types')) {
  if (file.endsWith('.d.ts')) {
    const path = join('native/dist/types', file);
    await writeFile(path, (await readFile(path, 'utf8')).replaceAll('.ts\'', '.js\''));
  }
}
