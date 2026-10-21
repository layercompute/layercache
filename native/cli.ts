import { NativeCache, NativeCacheConflictError, NativeCachePublicationError, environmentOptions } from './client.ts';
import type { Identity, Options } from './client.ts';
import { sourceKey } from './source.ts';
import { resolve } from 'node:path';
import { detectSimulatorTarget, parseSimulatorTarget, planExpoSimulator } from './expo-identity.ts';
import { authenticateSnapshot, SnapshotAuthenticationError } from './snapshot-auth.ts';
import { snapshotArchiveErrorMessage } from './snapshot-error.ts';
import { exportXcodeReport } from './xcode-report.ts';
import { exportGradleReport } from './gradle-report.ts';
import { measureOperationCLI } from './operation.ts';

// Deliberately uses the same key and transport as CI. No checkout path or runner
// identity is included, so compatible worktrees can share outputs.
async function main(): Promise<void> {
  const [operation, ...args] = process.argv.slice(2);
  if (operation?.startsWith('measure-')) {await measureOperationCLI(operation, args); return;}
  if (operation === 'xcode-report') {await exportXcodeReport(args); return;}
  if (operation === 'gradle-report') {await exportGradleReport(args); return;}
  if (operation === 'key' && args.length === 0) { console.log(await sourceKey(process.cwd())); return; }
  if (operation === 'expo-target' && args.length === 0) { console.log(JSON.stringify(await detectSimulatorTarget(process.cwd()))); return; }
  const values: Record<string, string> = {};
  for (let index = 0; index < args.length; index += 2) {
    const flag = args[index];
    const value = args[index + 1];
    const flags = operation === 'expo-key' ? ['--project', '--app', '--root', '--scheme', '--target'] : ['--project', '--compatibility', '--key', '--path', '--cache-dir', '--max-bytes', '--timeout-seconds', '--credential-file', '--endpoint'];
    if (!flag || !flags.includes(flag) || !value || flag in values) throw new Error('Invalid arguments');
    values[flag] = value;
  }
  if (operation === 'expo-key') {
    const projectRoot = resolve(values['--root'] ?? '.');
    const target = values['--target'] ? parseSimulatorTarget(values['--target']) : await detectSimulatorTarget(projectRoot);
    console.log(JSON.stringify(await planExpoSimulator({ projectRoot, project: values['--project'] ?? '', app: values['--app'] ?? '', scheme: values['--scheme'], target })));
    return;
  }
  const timeout = values['--timeout-seconds'];
  if (timeout !== undefined && (!/^[1-9][0-9]{0,2}$/.test(timeout) || Number(timeout) > 900)) throw new Error('--timeout-seconds must be an integer from 1 to 900');
  const timeoutOptions = timeout === undefined ? {} : { timeoutMs: Number(timeout) * 1000 };
  const identity: Identity = { project: values['--project'] ?? '', compatibility: values['--compatibility'] ?? '', key: values['--key'] ?? '' };
  if (values['--credential-file'] || values['--endpoint']) {
    if (!values['--credential-file'] || !values['--endpoint'] || !values['--path'] || !['save', 'restore'].includes(operation ?? '')) {
      throw new SnapshotAuthenticationError('Scoped snapshot save/restore requires --credential-file, --endpoint and --path');
    }
    try {
      const options = await authenticateSnapshot({ endpoint: values['--endpoint'], credentialFile: values['--credential-file'], identity,
        mode: operation === 'save' ? 'write' : 'read', ...(values['--cache-dir'] ? { cacheDir: values['--cache-dir'] } : {}) });
      const cache = new NativeCache({ ...options, ...timeoutOptions, ...(values['--max-bytes'] ? { maxBytes: Number(values['--max-bytes']) } : {}) });
      console.log(JSON.stringify(operation === 'save' ? await cache.saveImmutable(identity, values['--path']) : await cache.restore(identity, values['--path'])));
    } catch (error) {
      if (error instanceof SnapshotAuthenticationError) throw error;
      if (error instanceof NativeCacheConflictError) throw new Error('Snapshot generation already has different immutable content');
      if (error instanceof NativeCachePublicationError) throw new Error('Snapshot Team publication was not confirmed');
      throw new Error(snapshotArchiveErrorMessage(error));
    }
    return;
  }
  const options: Options = { endpoint: process.env.LAYER_CACHE_URL, token: process.env.LAYER_CACHE_TOKEN,
    cacheDir: values['--cache-dir'], maxBytes: values['--max-bytes'] ? Number(values['--max-bytes']) : undefined, ...timeoutOptions };
  const cache = new NativeCache(environmentOptions(options));
  if (operation === 'restore') console.log(JSON.stringify(await cache.restore(identity, values['--path'])));
  else if (operation === 'save' && values['--path']) console.log(JSON.stringify(await cache.save(identity, values['--path'])));
  else throw new Error('Use restore or save --project ID --compatibility TOOLCHAIN --key KEY --path OUTPUT');
}
main().catch(error => { console.error(error.message); process.exitCode = 1; });
