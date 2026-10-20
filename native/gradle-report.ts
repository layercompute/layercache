import { constants } from 'node:fs';
import { open } from 'node:fs/promises';
import { readGradleReports } from '../action/gradle/reports.mjs';

// Offline projection uses the action's exact native task/key mapper. It neither
// infers the importing machine's resources nor reads cache credentials.
export async function exportGradleReport(args: string[]): Promise<void> {
  const allowed = ['--project', '--compatibility', '--reports-dir', '--info-log-file', '--output'];
  const values = new Map<string, string>();
  for (let index = 0; index < args.length; index += 2) {
    const flag = args[index], value = args[index + 1];
    if (!flag || !allowed.includes(flag) || !value || values.has(flag)) throw new Error('Invalid Gradle report arguments');
    values.set(flag, value);
  }
  const project = values.get('--project'), compatibility = values.get('--compatibility');
  if (!project || project.length > 256 || /[\p{White_Space}\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/u.test(project) ||
      !compatibility || !/^[a-z0-9][a-z0-9_.:+@-]{0,255}$/.test(compatibility) ||
      !values.has('--reports-dir') || !values.has('--output')) throw new Error('Invalid Gradle report arguments');
  let reports;
  try {
    reports = readGradleReports(values.get('--reports-dir')!, {
      project, compatibility, infoLogPath: values.get('--info-log-file'),
    });
  } catch {throw new Error('Gradle invocation reports unavailable');}
  if (reports.length !== 1) throw new Error('Gradle report export requires exactly one completed invocation');
  // Keep the invocation identity and final bytes; exporting it again must not
  // create a new invocation or combine separate Gradle processes.
  const report = reports[0]!.body;
  let output;
  try {
    output = await open(values.get('--output')!, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
    await output.writeFile(JSON.stringify(report) + '\n'); await output.sync();
  } catch {throw new Error('Gradle report output unavailable');}
  finally {await output?.close();}
}
