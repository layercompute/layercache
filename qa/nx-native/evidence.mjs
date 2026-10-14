import { closeSync, openSync, writeFileSync } from 'node:fs';
import { isAbsolute } from 'node:path';

// Export only this fixture's structured proof, never its credential-bearing
// workspace, configurations, arbitrary properties or raw native/daemon logs.
export function writeEvidence(output, evidence) {
  if (!isAbsolute(output)) throw new Error('NX_QA_OUTPUT must be an absolute path to a new JSON file');
  const exported = { schemaVersion: 'nx-native-v1' };
  for (const key of ['passed', 'nxVersion', 'installedNativePackages', 'nodeVersion', 'profile', 'hashes',
    'localArchives', 'teamArchives', 'outputDigests', 'localTerminalOutputDigests', 'teamTerminalOutputDigests',
    'stages', 'nativeFilesystemCacheBoundedByLayerCache', 'localNxTeamReadthroughAndPublicationImplemented']) {
    if (evidence[key] !== undefined) exported[key] = evidence[key];
  }
  const file = openSync(output, 'wx', 0o600);
  try {
    writeFileSync(file, JSON.stringify(exported, null, 2) + '\n');
  } finally {
    closeSync(file);
  }
}
