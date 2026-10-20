import { collectExecutionSample } from './execution-collect.ts';

// This process receives only the two selected paths, never authentication or
// inherited job environment. stdout contains a bounded hardware-only snapshot.
async function main(): Promise<void> {
  let source = '';
  for await (const chunk of process.stdin) {
    source += String(chunk);
    if (Buffer.byteLength(source) > 16 * 1024) throw new Error('Input limit');
  }
  const input = JSON.parse(source) as Record<string, unknown>;
  const env: NodeJS.ProcessEnv = {};
  for (const key of ['GITHUB_WORKSPACE', 'RUNNER_TEMP']) {
    const value = input[key];
    if (typeof value === 'string' && value.length <= 4096) env[key] = value;
  }
  const output = JSON.stringify(await collectExecutionSample(env));
  if (Buffer.byteLength(output) > 16 * 1024) throw new Error('Output limit');
  process.stdout.write(output, () => process.exit(0));
}

main().catch(() => process.exit(1));
