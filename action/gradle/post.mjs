import { existsSync, realpathSync, rmSync } from 'node:fs';
import { basename, dirname } from 'node:path';
import { pathToFileURL } from 'node:url';

export function cleanupGradle(env = process.env) {
  const root = env.STATE_gradle_root;
  if (!root || !existsSync(root)) return;
  if (!env.RUNNER_TEMP || dirname(realpathSync(root)) !== realpathSync(env.RUNNER_TEMP) ||
      !basename(root).startsWith('layercache-gradle-job-')) throw new Error('Refusing Gradle cleanup outside its owned job directory');
  rmSync(root, { recursive: true });
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) cleanupGradle();
