import { build } from 'esbuild';
import { fileURLToPath } from 'node:url';

// The same entry is used by direct TypeScript actions and both bundled actions.
export async function buildExecutionWorker(outfile = fileURLToPath(new URL('./execution-worker.cjs', import.meta.url))) {
  await build({ entryPoints: [fileURLToPath(new URL('./execution-worker.ts', import.meta.url))],
    outfile, format: 'cjs', bundle: true, platform: 'node', target: 'node24', legalComments: 'eof',
    absWorkingDir: fileURLToPath(new URL('../..', import.meta.url)) });
}

if (process.argv[1] === fileURLToPath(import.meta.url)) await buildExecutionWorker();
