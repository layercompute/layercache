import assert from 'node:assert/strict';
import test from 'node:test';
import {formatDuration, projectInsights} from './insights.ts';
import type {Cache, Report} from './cache-view.js';

function cache(overrides: Partial<Report> = {}): Cache {
  return {
    state: 'connected', reportState: 'connected',
    status: {usageBytes: 128, maxBytes: 1024, artifacts: 4},
    report: {
      hits: 7, eligible: 10, misses: 3, runs: 2, degraded: false,
      integrations: [{integration: 'turbo', eligible: 10, hits: 7}],
      netEstimatedBuildTimeSaved: {milliseconds: -2000, known: 1, total: 2, confidence: 'low'},
      grossAvoidedTaskTime: {milliseconds: 10000, known: 1, total: 7, confidence: 'low'},
      ...overrides,
    },
  };
}

test('project insights preserve native ratios and missing integration coverage', () => {
  const result = projectInsights(cache());
  assert.equal(result.hitRate, 0.7);
  assert.equal(result.storageUsedBytes, 128);
  const turbo = result.integrations.find(item => item.id === 'turbo');
  assert.equal(turbo?.state, 'reported');
  assert.equal(turbo?.units, 'tasks');
  assert.equal(turbo?.misses, 3);
  for (const id of ['xcode', 'gradle', 'bazel', 'nx', 'expo', 'buildkit']) {
    const missing = result.integrations.find(item => item.id === id);
    assert.equal(missing?.state, 'not-reported');
    assert.equal(missing?.hitRate, null);
    assert.equal(missing?.hits, null);
  }
});

test('an explicitly empty metered integration differs from an unreported one', () => {
  const result = projectInsights(cache({hits: 0, eligible: 0, misses: 0, integrations: [{integration: 'buildkit', eligible: 0, hits: 0}]}));
  assert.equal(result.hitRate, null);
  const docker = result.integrations.find(item => item.id === 'buildkit');
  assert.equal(docker?.state, 'reported');
  assert.equal(docker?.hits, 0);
  assert.equal(docker?.hitRate, null);
  assert.equal(docker?.units, 'vertices');
});

test('partial and negative elapsed estimates never become CPU or financial savings', () => {
  const result = projectInsights(cache());
  assert.equal(result.savings.estimatedBuildTime?.milliseconds, -2000);
  assert.equal(result.savings.estimatedBuildTime?.confidence, 'low');
  assert.equal(result.savings.avoidedTaskTime?.known, 1);
  assert.equal(result.savings.avoidedTaskTime?.total, 7);
  assert.equal(result.savings.cpuHoursSaved, null);
  assert.equal(result.savings.moneySaved, null);
  assert.equal(formatDuration(-2000), '−2.0 s');
});

test('unavailable project data remains unknown rather than a zero-usage success', () => {
  const result = projectInsights({state: 'unavailable', reportState: 'unavailable'});
  assert.equal(result.reportAvailable, false);
  assert.equal(result.storageUsedBytes, null);
  assert.equal(result.artifacts, null);
  assert.equal(result.hits, null);
  assert.equal(result.runs, null);
  assert.equal(result.savings.avoidedTaskTime, null);
  assert.equal(result.savings.estimatedBuildTime, null);
  assert.deepEqual(result.topArtifacts, []);
  assert.deepEqual(result.activity, []);
});
