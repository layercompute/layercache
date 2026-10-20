export const now = Date.parse('2026-10-20T12:00:00Z');
export const observedAt = '2026-10-20T11:00:00Z';
const gib = 1024 ** 3;

export function volume(overrides = {}) {
  return {
    id: 'c'.repeat(64), roles: ['workspace', 'localCache', 'staging'],
    capacityBytes: 100 * gib, freeBytes: 30 * gib, availableBytes: 25 * gib,
    ...overrides,
  };
}

export function entry(overrides = {}) {
  return {
    id: 'a'.repeat(64), ownerId: 'github-id:1', installationId: 'b'.repeat(64),
    executionId: `client:${'d'.repeat(64)}`, kind: 'client',
    provider: 'unknown', providerEvidence: 'unknown', runnerEnvironment: 'unknown',
    lastObservedAt: observedAt, phase: 'finish',
    sample: {
      os: 'linux', arch: 'amd64',
      cpu: {visibleLogical: 16, affinityLogical: 8, quotaMilliCPU: 2500, effectiveMilliCPU: 2500, limitKnowledge: 'complete'},
      memory: {visibleTotalBytes: 64 * gib, cgroupLimitBytes: 8 * gib, effectiveLimitBytes: 8 * gib, limitKnowledge: 'complete'},
      filesystems: [volume()],
    },
    cacheActivity: 'unconfirmed',
    ...overrides,
  };
}

export function job(overrides = {}) {
  const value = entry({
    id: 'e'.repeat(64), kind: 'githubActions', provider: 'github',
    providerEvidence: 'signed', runnerEnvironment: 'github-hosted',
    executionId: 'github-actions:1:100:1:200', ...overrides,
  });
  delete value.installationId;
  return value;
}

export function inventory(overrides = {}) {
  return {
    version: 1, from: '2026-10-19T12:00:00Z', to: '2026-10-20T12:00:00Z',
    updatedAt: observedAt, retentionDays: 30, staleAfterSeconds: 86400,
    historyTruncated: false, installations: [], jobs: [], ...overrides,
  };
}

export function project(id = 'example/project-one', executions = inventory(), name = id) {
  return {id, name, executions};
}
