const maximumBytes = 64 * 1024 * 1024;
const maximumMatches = 8192;
const maximumPaths = 4096;
const maximumPathCharacters = 1024;
const qualifiedVersions = new Set(['9.3.1', '9.4.1']);
const taskKeyLine = /^Build cache key for task '(:[^'\r\n]*)' is ([a-f0-9]{32})$/;
const disabledLine = /^Caching disabled for task '(:[^'\r\n]*)' because:$/;
const pathControls = /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/u;

function unavailable(reason) {
  return { keys: new Map(), ineligiblePaths: new Set(), ambiguousPaths: new Set(), duplicateKeys: new Set(), reason };
}

// Qualified Gradle versions log the same exact template in
// AbstractResolveCachingStateStep.logCacheKey and logDisabledReasons:
// https://github.com/gradle/gradle/blob/v9.3.1/platforms/core-execution/execution/src/main/java/org/gradle/internal/execution/steps/AbstractResolveCachingStateStep.java#L113
// https://github.com/gradle/gradle/blob/v9.4.1/platforms/core-execution/execution/src/main/java/org/gradle/internal/execution/steps/AbstractResolveCachingStateStep.java#L109
// TaskExecution.getDisplayName uses task.toString() for the task label.
// Exact text is a local correlation hint, not authenticated Gradle provenance.
// The caller must separately establish that the log belongs to its completed
// invocation. A parser cannot detect a file truncated exactly at a line boundary.
export function parseGradleInfo(text, { gradleVersion = '9.3.1' } = {}) {
  if (!qualifiedVersions.has(gradleVersion)) return unavailable('unsupported-gradle-version');
  if (typeof text !== 'string') return unavailable('invalid-info-log');
  if (text.length > maximumBytes || Buffer.byteLength(text, 'utf8') > maximumBytes) {
    return unavailable('info-log-too-large');
  }
  if (text.length === 0) return unavailable('missing-key-lines');
  if (!text.endsWith('\n')) return unavailable('truncated-info-log');

  const pathKeys = new Map();
  const ineligiblePaths = new Set();
  const keyPaths = new Map();
  let matches = 0;
  let offset = 0;
  while (offset < text.length) {
    const end = text.indexOf('\n', offset);
    const line = text.slice(offset, end > offset && text[end - 1] === '\r' ? end - 1 : end);
    offset = end + 1;
    // Do not split the entire log into lines: a bounded byte string can still
    // contain millions of irrelevant short lines. Transform keys are unrelated.
    const keyCandidate = line.includes('Build cache key for task');
    const disabledCandidate = line.includes('Caching disabled for task');
    if (!keyCandidate && !disabledCandidate) continue;
    const keyMatch = taskKeyLine.exec(line), disabledMatch = disabledLine.exec(line);
    const match = keyMatch ?? disabledMatch;
    if (!match) return unavailable('unrecognized-key-line');
    const [, path, key] = match;
    if (path.length > maximumPathCharacters || pathControls.test(path)) {
      return unavailable('invalid-task-path');
    }
    matches++;
    if (matches > maximumMatches) return unavailable('too-many-key-lines');
    if (!pathKeys.has(path)) {
      if (pathKeys.size === maximumPaths) return unavailable('too-many-task-paths');
      pathKeys.set(path, new Set());
    }
    if (disabledMatch) {ineligiblePaths.add(path);continue;}
    pathKeys.get(path).add(key);
    if (!keyPaths.has(key)) keyPaths.set(key, new Set());
    keyPaths.get(key).add(path);
  }
  if (matches === 0) return unavailable('missing-key-lines');

  const ambiguousPaths = new Set();
  const duplicateKeys = new Set();
  for (const [path, keys] of pathKeys) {
    if (keys.size > 1 || keys.size > 0 && ineligiblePaths.has(path)) {ambiguousPaths.add(path);ineligiblePaths.delete(path);}
  }
  for (const [key, paths] of keyPaths) {
    if (paths.size > 1) duplicateKeys.add(key);
  }
  const keys = new Map();
  for (const [path, candidates] of pathKeys) {
    if (ambiguousPaths.has(path)) continue;
    const [key] = candidates;
    // Equivalent native task paths may legitimately share a cache key. The
    // server resolves producer-window ambiguity independently of this mapping.
    if (key) keys.set(path, key);
  }
  return { keys, ineligiblePaths, ambiguousPaths, duplicateKeys,
    ...(keys.size === 0 && ineligiblePaths.size === 0 ? { reason: 'ambiguous-key-mapping' } : {}) };
}
