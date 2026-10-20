import assert from 'node:assert/strict';
import test from 'node:test';
import {formatDuration, nativeSourceLabel, projectInsights, scopedCompilerRate} from './insights.ts';
import {parseXcodeCompilerLog, xcodeReportFormat} from '../native/xcode-report.ts';
import type {Cache, NativeCompilerCounters, NativeIntegrationReport, Report} from './cache-view.js';

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

function nativeReport(overrides: Partial<NativeIntegrationReport> = {}): NativeIntegrationReport {
  return {
    integration: 'gradle', reportedInvocations: 1, reportedTasks: 0,
    outcomes: {executed: 0, fromCache: 0, upToDate: 0, noSource: 0, skipped: 0, failed: 0, unknown: 0},
    knownEligible: 0, eligibilityUnknown: 0, eligible: 0, hits: 0, misses: 0, hitRate: null,
    sources: [], producerTiming: {known: 0, total: 0}, producerResources: {known: 0, total: 0},
    grossAvoidedTaskTime: {milliseconds: null, known: 0, total: 0, confidence: 'unknown'},
    netEstimatedBuildTimeSaved: {milliseconds: null, known: 0, total: 0, confidence: 'unknown'},
    allocatedCpuSecondsSaved: null, measuredCpuSecondsSaved: null, costSaved: null,
    historyTruncated: false,
    ...overrides,
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

test('coverage distinguishes completed outcomes, empty work and a missing Gradle report', () => {
  const result = projectInsights(cache({integrations: [
    {integration: 'turbo', eligible: 10, hits: 7},
    {integration: 'actions', eligible: 0, hits: 0},
  ]}));
  const turbo = result.integrations.find(item => item.id === 'turbo');
  const actions = result.integrations.find(item => item.id === 'actions');
  const gradle = result.integrations.find(item => item.id === 'gradle');
  assert.equal(turbo?.coverage.support, 'outcomes');
  assert.equal(turbo?.coverage.state, 'reported');
  assert.equal(turbo?.hitRate, 0.7);
  assert.equal(actions?.coverage.support, 'outcomes');
  assert.equal(actions?.coverage.state, 'no-eligible-work');
  assert.equal(actions?.hits, 0);
  assert.equal(actions?.eligible, 0);
  assert.equal(actions?.hitRate, null);
  assert.equal(gradle?.coverage.support, 'outcomes');
  assert.equal(gradle?.coverage.state, 'no-report');
  assert.equal(gradle?.state, 'not-reported');
  assert.equal(gradle?.hits, null);
  assert.equal(gradle?.eligible, null);
  assert.equal(gradle?.misses, null);
  assert.equal(gradle?.hitRate, null);
  assert.match(gradle?.coverage.action ?? '', /reporting/i);
});

test('native cache support does not imply hosted outcome reporting', () => {
  const result = projectInsights(cache());
  const xcode = result.integrations.find(item => item.id === 'xcode');
  assert.equal(xcode?.coverage.support, 'protocol');
  assert.equal(xcode?.coverage.state, 'unsupported');
  assert.match(xcode?.coverage.reason ?? '', /no.*compiler.*(?:recorded|collected)|compiler.*not.*(?:recorded|collected)/i);
  assert.match(xcode?.coverage.action ?? '', /snapshot.*does not|snapshot.*separate/i);
  for (const id of ['bazel', 'nx', 'expo']) {
    const integration = result.integrations.find(item => item.id === id);
    assert.equal(integration?.coverage.support, 'unsupported', id);
    assert.equal(integration?.coverage.state, 'unsupported', id);
    assert.equal(integration?.state, 'not-reported', id);
    assert.equal(integration?.hits, null, id);
    assert.equal(integration?.eligible, null, id);
    assert.equal(integration?.misses, null, id);
    assert.equal(integration?.hitRate, null, id);
    assert.doesNotMatch(integration?.coverage.reason ?? '', /cache is unsupported|caching is unsupported/i, id);
  }
  const buildkit = result.integrations.find(item => item.id === 'buildkit');
  assert.equal(buildkit?.coverage.support, 'local-outcomes');
  assert.equal(buildkit?.coverage.state, 'unsupported');
  assert.equal(buildkit?.hits, null);
  assert.equal(buildkit?.eligible, null);
  assert.match(buildkit?.coverage.reason ?? '', /hosted.*not available/i);
  assert.match(buildkit?.coverage.action ?? '', /local.*buildx/i);
});

test('a local BuildKit outcome report retains its native vertex ratio', () => {
  const result = projectInsights(cache({integrations: [
    {integration: 'buildkit', eligible: 8, hits: 5, misses: 3},
  ]}));
  const buildkit = result.integrations.find(item => item.id === 'buildkit');
  assert.equal(buildkit?.coverage.support, 'local-outcomes');
  assert.equal(buildkit?.coverage.state, 'reported');
  assert.equal(buildkit?.state, 'reported');
  assert.equal(buildkit?.hits, 5);
  assert.equal(buildkit?.eligible, 8);
  assert.equal(buildkit?.misses, 3);
  assert.equal(buildkit?.hitRate, 5 / 8);
  assert.equal(buildkit?.units, 'vertices');
});

test('coverage gives each integration an explanation, a next step and public documentation', () => {
  const result = projectInsights(cache());
  for (const integration of result.integrations) {
    const coverage = integration.coverage;
    assert.ok(coverage.label.trim().length > 0, `${integration.id} label`);
    assert.ok(coverage.reason.trim().length > 0, `${integration.id} explanation`);
    assert.ok(coverage.action.trim().length > 0, `${integration.id} next step`);
    const documentation = new URL(coverage.documentation);
    assert.equal(documentation.origin, 'https://github.com', integration.id);
    assert.match(documentation.pathname, /^\/ziahamza\/layercache\/blob\/main\/(?:action\/|docs\/|native\/)/, integration.id);
    assert.equal(documentation.username, '', integration.id);
    assert.equal(documentation.password, '', integration.id);
    assert.equal(documentation.search, '', integration.id);
  }
});

test('an unreadable report overrides missing-report coverage without discarding known storage', () => {
  const fixture = cache();
  delete fixture.report;
  fixture.reportState = 'unavailable';
  const result = projectInsights(fixture);
  assert.equal(result.reportAvailable, false);
  assert.equal(result.storageUsedBytes, 128);
  assert.equal(result.storageLimitBytes, 1024);
  for (const integration of result.integrations) {
    assert.equal(integration.coverage.state, 'unavailable', integration.id);
    assert.equal(integration.state, 'not-reported', integration.id);
    assert.equal(integration.hits, null, integration.id);
    assert.equal(integration.eligible, null, integration.id);
    assert.equal(integration.misses, null, integration.id);
    assert.equal(integration.hitRate, null, integration.id);
    assert.equal(integration.downloadedBytes, null, integration.id);
    assert.equal(integration.uploadedBytes, null, integration.id);
    assert.match(integration.coverage.reason, /could not be read|unavailable/i, integration.id);
  }
});

test('Gradle native hits remain known when execution eligibility, source and producer evidence are incomplete', () => {
  const gradle = nativeReport({
    reportedTasks: 9,
    outcomes: {executed: 2, fromCache: 3, upToDate: 1, noSource: 1, skipped: 1, failed: 0, unknown: 1},
    knownEligible: 4, eligibilityUnknown: 1, eligible: null, hits: 3, misses: 1, hitRate: null,
    sources: [{source: 'teamCache', hits: 1}, {source: 'unattributed', hits: 2}],
    producerTiming: {known: 1, total: 3}, producerResources: {known: 0, total: 3},
    grossAvoidedTaskTime: {milliseconds: null, known: 1, total: 3, confidence: 'unknown'},
    historyTruncated: true,
  });
  const result = projectInsights(cache({nativeReports: [gradle]}));
  const row = result.integrations.find(item => item.id === 'gradle');
  assert.equal(row?.state, 'reported');
  assert.equal(row?.hits, 3);
  assert.equal(row?.nativeReport?.hits, 3);
  assert.equal(row?.eligible, null);
  assert.equal(row?.misses, null);
  assert.equal(row?.hitRate, null);
  assert.equal(row?.coverage.state, 'reported');
  assert.equal(row?.coverage.label, 'Partial eligibility coverage');
  assert.equal(row?.nativeReport?.knownEligible, 4);
  assert.equal(row?.nativeReport?.eligibilityUnknown, 1);
  assert.deepEqual(row?.nativeReport?.sources, [{source: 'teamCache', hits: 1}, {source: 'unattributed', hits: 2}]);
  assert.deepEqual(row?.nativeReport?.producerTiming, {known: 1, total: 3});
  assert.deepEqual(row?.nativeReport?.producerResources, {known: 0, total: 3});
  assert.deepEqual(row?.avoidedTaskTime, gradle.grossAvoidedTaskTime);
  assert.equal(row?.nativeReport?.historyTruncated, true);
  assert.deepEqual(result.nativeReports, [gradle]);
});

test('complete Gradle eligibility retains its native ratio without counting up-to-date tasks', () => {
  const gradle = nativeReport({
    reportedTasks: 8,
    outcomes: {executed: 2, fromCache: 3, upToDate: 1, noSource: 1, skipped: 1, failed: 0, unknown: 0},
    knownEligible: 5, eligibilityUnknown: 0, eligible: 5, hits: 3, misses: 2, hitRate: 0.6,
    producerTiming: {known: 0, total: 3}, producerResources: {known: 0, total: 3},
  });
  const result = projectInsights(cache({nativeReports: [gradle]}));
  const row = result.integrations.find(item => item.id === 'gradle');
  assert.equal(row?.hits, 3);
  assert.equal(row?.eligible, 5);
  assert.equal(row?.misses, 2);
  assert.equal(row?.hitRate, 0.6);
  assert.equal(row?.units, 'tasks');
  assert.equal(row?.coverage.state, 'reported');
  assert.deepEqual(row?.nativeReport?.outcomes, gradle.outcomes);
});

test('Gradle work with no cache-eligible outcomes differs from missing eligibility or reports', () => {
  const noEligibleWork = nativeReport({
    reportedTasks: 3, eligible: null,
    outcomes: {executed: 0, fromCache: 0, upToDate: 1, noSource: 1, skipped: 1, failed: 0, unknown: 0},
  });
  const empty = projectInsights(cache({nativeReports: [noEligibleWork]})).integrations.find(item => item.id === 'gradle');
  const absent = projectInsights(cache()).integrations.find(item => item.id === 'gradle');
  assert.equal(empty?.state, 'reported');
  assert.equal(empty?.coverage.state, 'no-eligible-work');
  assert.equal(empty?.coverage.label, 'No eligible work');
  assert.doesNotMatch(empty?.coverage.reason ?? '', /partial|unknown eligibility/i);
  assert.equal(empty?.hits, 0);
  assert.equal(empty?.eligible, null);
  assert.equal(empty?.misses, null);
  assert.equal(empty?.hitRate, null);
  const keyless = nativeReport({
    reportedTasks: 2, eligible: null, knownEligible: 1, eligibilityUnknown: 1, hits: 1,
    outcomes: {executed: 1, fromCache: 1, upToDate: 0, noSource: 0, skipped: 0, failed: 0, unknown: 0},
    sources: [{source: 'unattributed', hits: 1}],
    producerTiming: {known: 0, total: 1}, producerResources: {known: 0, total: 1},
  });
  const unknown = projectInsights(cache({nativeReports: [keyless]})).integrations.find(item => item.id === 'gradle');
  assert.equal(unknown?.state, 'reported');
  assert.equal(unknown?.coverage.state, 'reported');
  assert.equal(unknown?.coverage.label, 'Partial eligibility coverage');
  assert.equal(unknown?.hits, 1);
  assert.equal(unknown?.nativeReport?.hits, 1);
  assert.equal(unknown?.nativeReport?.knownEligible, 1);
  assert.equal(unknown?.nativeReport?.eligibilityUnknown, 1);
  assert.deepEqual(unknown?.nativeReport?.sources, [{source: 'unattributed', hits: 1}]);
  assert.equal(unknown?.eligible, null);
  assert.equal(unknown?.misses, null);
  assert.equal(unknown?.hitRate, null);
  assert.equal(absent?.state, 'not-reported');
  assert.equal(absent?.coverage.state, 'no-report');
  assert.equal(absent?.hits, null);
  assert.equal(absent?.eligible, null);
  assert.equal(absent?.misses, null);
  assert.equal(absent?.hitRate, null);
});

test('Xcode RPC hits remain protocol evidence with absent or unsupported compiler parsing', () => {
  const unsupported: NativeCompilerCounters = {scope: 'unknown', coverage: 'unsupported', reason: 'unsupported', hits: null, misses: null, skipped: null, unknown: null};
  for (const compiler of [undefined, unsupported]) {
    const xcode = nativeReport({
      integration: 'xcode',
      counters: {
        rpc: {actionReads: 10, actionHits: 9, actionWrites: 0, casReads: 21, casHits: 20, casWrites: null, cacheErrors: 0},
        ...(compiler ? {compiler} : {}),
      },
      timingMs: {operation: 1000, compile: null, load: 0, restore: null, close: 1, snapshot: null},
    });
    const result = projectInsights(cache({nativeReports: [xcode]}));
    const row = result.integrations.find(item => item.id === 'xcode');
    assert.equal(row?.state, 'not-reported');
    assert.equal(row?.coverage.support, 'protocol');
    assert.equal(row?.coverage.state, 'protocol-only');
    assert.equal(row?.hits, null);
    assert.equal(row?.eligible, null);
    assert.equal(row?.misses, null);
    assert.equal(row?.hitRate, null);
    assert.equal(row?.avoidedTaskTime, null);
    assert.equal(row?.nativeReport?.counters?.rpc?.actionHits, 9);
    assert.equal(row?.nativeReport?.counters?.rpc?.actionWrites, 0);
    assert.equal(row?.nativeReport?.counters?.rpc?.casWrites, null);
    assert.equal(row?.nativeReport?.timingMs?.load, 0);
    assert.equal(row?.nativeReport?.timingMs?.compile, null);
    assert.deepEqual(row?.nativeReport?.counters?.compiler, compiler);
    assert.equal(result.hitRate, 0.7);
  }
});

test('Xcode lifecycle phases alone do not imply protocol counters or task eligibility', () => {
  const unsupported: NativeCompilerCounters = {scope: 'unknown', coverage: 'unsupported', reason: 'unsupported', hits: null, misses: null, skipped: null, unknown: null};
  for (const compiler of [undefined, unsupported]) {
    const xcode = nativeReport({
      integration: 'xcode', eligible: null,
      ...(compiler ? {counters: {compiler}} : {}),
      timingMs: {operation: 5000, compile: null, load: 0, restore: 12, close: null, snapshot: null},
    });
    const row = projectInsights(cache({nativeReports: [xcode]})).integrations.find(item => item.id === 'xcode');
    assert.equal(row?.state, 'not-reported');
    assert.equal(row?.coverage.state, 'timing-only');
    assert.equal(row?.coverage.label, 'Lifecycle timing only');
    assert.doesNotMatch(row?.coverage.reason ?? '', /key-supported|task outcomes|task eligibility/i);
    assert.equal(row?.hits, null);
    assert.equal(row?.eligible, null);
    assert.equal(row?.misses, null);
    assert.equal(row?.hitRate, null);
    assert.equal(row?.avoidedTaskTime, null);
    assert.equal(row?.nativeReport?.counters?.rpc, undefined);
    assert.deepEqual(row?.nativeReport?.timingMs, xcode.timingMs);
    assert.deepEqual(row?.nativeReport?.counters?.compiler, compiler);
  }
});

test('Xcode compiler verdicts remain separate from RPC hits and native task eligibility', () => {
  const cases: NativeCompilerCounters[] = [
    {scope: 'clang-pch', coverage: 'complete', hits: 0, misses: 5, skipped: 0, unknown: 0},
    {scope: 'swift', coverage: 'partial', reason: 'truncated', hits: 3, misses: null, skipped: 0, unknown: null},
    {scope: 'mixed', coverage: 'partial', reason: 'ambiguous', hits: null, misses: 2, skipped: null, unknown: 1},
  ];
  for (const compiler of cases) {
    const xcode = nativeReport({
      integration: 'xcode',
      counters: {compiler, rpc: {actionReads: 50, actionHits: 40, actionWrites: 4, casReads: 100, casHits: 80, casWrites: 8, cacheErrors: 0}},
    });
    const row = projectInsights(cache({nativeReports: [xcode]})).integrations.find(item => item.id === 'xcode');
    assert.equal(row?.state, 'reported', compiler.scope);
    assert.equal(row?.coverage.state, 'reported', compiler.scope);
    assert.equal(row?.coverage.label, 'Compiler verdicts reported', compiler.scope);
    assert.equal(row?.hits, compiler.hits, compiler.scope);
    assert.equal(row?.misses, compiler.misses, compiler.scope);
    assert.equal(row?.eligible, null, compiler.scope);
    assert.equal(row?.hitRate, null, compiler.scope);
    assert.equal(row?.units, 'compiler verdicts', compiler.scope);
    assert.equal(row?.avoidedTaskTime, null, compiler.scope);
    assert.deepEqual(row?.nativeReport?.counters?.compiler, compiler, compiler.scope);
  }
});

test('a scoped compiler ratio requires complete verdicts and excludes skipped work', () => {
  const complete: NativeCompilerCounters = {scope: 'clang-pch', coverage: 'complete', hits: 8, misses: 2, skipped: 90, unknown: 0};
  for (const scope of ['clang-pch', 'swift', 'mixed'] satisfies NativeCompilerCounters['scope'][]) {
    assert.equal(scopedCompilerRate({...complete, scope}), 0.8, scope);
  }
  assert.equal(scopedCompilerRate({...complete, hits: 0, misses: 5}), 0);
  assert.equal(scopedCompilerRate({...complete, hits: 5, misses: 0}), 1);
  assert.equal(scopedCompilerRate({...complete, reason: 'none'}), 0.8);
  assert.equal(scopedCompilerRate({...complete, skipped: null}), 0.8, 'The supported terminal grammar has no skipped counter');
  assert.equal(scopedCompilerRate({...complete, hits: 0, misses: 0}), null);
  assert.equal(scopedCompilerRate(undefined), null);
  const incomplete: Partial<NativeCompilerCounters>[] = [
    {coverage: 'partial'},
    {coverage: 'unsupported'},
    {coverage: 'unknown'},
    {coverage: 'partial', reason: 'truncated'},
    {reason: 'truncated'},
    {reason: 'unsupported'},
    {reason: 'ambiguous'},
    {reason: 'missing'},
    {hits: null},
    {misses: null},
    {unknown: null},
    {unknown: 1},
    {scope: 'unknown'},
  ];
  for (const counters of incomplete) {
    assert.equal(scopedCompilerRate({...complete, ...counters}), null, JSON.stringify(counters));
  }
  const result = projectInsights(cache({nativeReports: [nativeReport({integration: 'xcode', counters: {compiler: complete}})]}));
  const xcode = result.integrations.find(item => item.id === 'xcode');
  assert.equal(xcode?.eligible, null);
  assert.equal(xcode?.hitRate, null);
  assert.equal(result.hitRate, 0.7);
});

test('supported compiler export retains unknown skipped work alongside a scoped classified-verdict rate', async () => {
  const compiler = await parseXcodeCompilerLog('CompileC output.o input.c\nnote: replayed cache hit: key-one\nCompileC output-two.o input-two.c\nnote: cache miss: key-two\n** BUILD SUCCEEDED **\n', xcodeReportFormat);
  assert.equal(compiler.scope, 'clang-pch');
  assert.equal(compiler.coverage, 'complete');
  assert.equal(compiler.skipped, null);
  assert.equal(scopedCompilerRate(compiler), 0.5);
  const result = projectInsights(cache({nativeReports: [nativeReport({integration: 'xcode', counters: {compiler}})]}));
  assert.equal(result.integrations.find(item => item.id === 'xcode')?.hitRate, null);
  assert.equal(result.hitRate, 0.7);
});

test('native counters and projected savings cannot alter legacy project totals or imply CPU savings', () => {
  const gradle = nativeReport({
    reportedTasks: 2, outcomes: {executed: 0, fromCache: 2, upToDate: 0, noSource: 0, skipped: 0, failed: 0, unknown: 0},
    knownEligible: 2, eligible: 2, hits: 2, hitRate: 1,
    grossAvoidedTaskTime: {milliseconds: 99000, known: 2, total: 2, confidence: 'medium'},
    netEstimatedBuildTimeSaved: {milliseconds: 98000, known: 2, total: 2, confidence: 'medium'},
    netAvoidedTaskTime: {milliseconds: -4000, known: 2, total: 2, confidence: 'medium'},
    producerTiming: {known: 2, total: 2}, producerResources: {known: 0, total: 2},
    allocatedCpuSecondsSaved: 196, measuredCpuSecondsSaved: 37, costSaved: 0.01,
  });
  const base = cache({
    topArtifacts: [{ref: 'opaque-turbo-ref', integration: 'turbo', hits: 7, restores: 7, runs: 2, downloadedBytes: 128,
      lastHitAt: '2026-10-20T12:00:00Z', grossAvoidedTaskTime: {milliseconds: 10000, known: 1, total: 7, confidence: 'low'}}],
    activity: [{from: '2026-10-20T00:00:00Z', to: '2026-10-21T00:00:00Z', runs: 2, eligible: 10, hits: 7, misses: 3}],
  });
  const legacy = projectInsights(base);
  assert.ok(base.report);
  const result = projectInsights({...base, report: {...base.report, nativeReports: [gradle]}});
  assert.equal(result.hits, 7);
  assert.equal(result.eligible, 10);
  assert.equal(result.misses, 3);
  assert.equal(result.hitRate, 0.7);
  assert.equal(result.runs, 2);
  assert.deepEqual(result.topArtifacts, legacy.topArtifacts);
  assert.deepEqual(result.activity, legacy.activity);
  assert.deepEqual(result.savings.avoidedTaskTime, legacy.savings.avoidedTaskTime);
  assert.deepEqual(result.savings.estimatedBuildTime, legacy.savings.estimatedBuildTime);
  assert.equal(result.savings.cpuHoursSaved, null);
  assert.equal(result.savings.moneySaved, null);
  const row = result.integrations.find(item => item.id === 'gradle');
  assert.deepEqual(row?.avoidedTaskTime, gradle.grossAvoidedTaskTime);
  assert.deepEqual(row?.nativeReport?.netAvoidedTaskTime, gradle.netAvoidedTaskTime);
  assert.equal(formatDuration(row?.nativeReport?.netAvoidedTaskTime?.milliseconds ?? null), '−4.0 s');
  assert.equal(row?.nativeReport?.allocatedCpuSecondsSaved, 196);
  assert.equal(row?.nativeReport?.measuredCpuSecondsSaved, 37);
  assert.equal(row?.nativeReport?.costSaved, 0.01);
  assert.deepEqual(legacy.nativeReports, []);
});

test('Xcode counters preserve complete timed generic estimates with either savings sign', () => {
  const xcode = nativeReport({
    integration: 'xcode',
    counters: {
      compiler: {scope: 'clang-pch', coverage: 'complete', hits: 8, misses: 2, skipped: null, unknown: 0},
      rpc: {actionReads: 50, actionHits: 40, actionWrites: 4, casReads: 100, casHits: 80, casWrites: 8, cacheErrors: 0},
    },
    timingMs: {operation: 5000, compile: 4000, load: 100, restore: 200, close: 100, snapshot: 600},
  });
  for (const milliseconds of [-2000, 12000]) {
    const fixture = cache({
      grossAvoidedTaskTime: {milliseconds: 20000, known: 7, total: 7, confidence: 'high'},
      netEstimatedBuildTimeSaved: {milliseconds, known: 2, total: 2, confidence: 'high'},
    });
    const baseline = projectInsights(fixture);
    assert.ok(fixture.report);
    const result = projectInsights({...fixture, report: {...fixture.report, nativeReports: [xcode]}});
    assert.equal(result.hits, 7);
    assert.equal(result.eligible, 10);
    assert.equal(result.hitRate, 0.7);
    assert.deepEqual(result.savings, baseline.savings);
    assert.equal(result.savings.estimatedBuildTime?.milliseconds, milliseconds);
    assert.equal(result.savings.estimatedBuildTime?.confidence, 'high');
    assert.deepEqual(result.integrations.find(item => item.id === 'turbo'), baseline.integrations.find(item => item.id === 'turbo'));
    const row = result.integrations.find(item => item.id === 'xcode');
    assert.equal(row?.hits, 8);
    assert.equal(row?.eligible, null);
    assert.equal(row?.hitRate, null);
    assert.equal(row?.nativeReport?.counters?.rpc?.actionHits, 40);
    assert.equal(row?.avoidedTaskTime, null);
  }
});

test('native report failure preserves generic metrics and isolates unavailable coverage', () => {
  const stale = [nativeReport({
    reportedTasks: 2, knownEligible: 2, eligible: 2, hits: 2, hitRate: 1,
    outcomes: {executed: 0, fromCache: 2, upToDate: 0, noSource: 0, skipped: 0, failed: 0, unknown: 0},
  }), nativeReport({
    integration: 'xcode',
    counters: {
      compiler: {scope: 'clang-pch', coverage: 'complete', hits: 10, misses: 0, skipped: null, unknown: 0},
      rpc: {actionReads: 20, actionHits: 20, actionWrites: 0, casReads: 40, casHits: 40, casWrites: 0, cacheErrors: 0},
    },
  })];
  for (const milliseconds of [-2000, 12000]) {
    const fixture = cache({
      integrations: [
        {integration: 'turbo', eligible: 5, hits: 3, misses: 2},
        {integration: 'actions', eligible: 2, hits: 1, misses: 1},
        {integration: 'buildkit', eligible: 3, hits: 3, misses: 0},
      ],
      grossAvoidedTaskTime: {milliseconds: 20000, known: 7, total: 7, confidence: 'high'},
      netEstimatedBuildTimeSaved: {milliseconds, known: 2, total: 2, confidence: 'high'},
    });
    const baseline = projectInsights(fixture);
    assert.ok(fixture.report);
    for (const nativeReports of [undefined, stale]) {
      const result = projectInsights({...fixture, report: {...fixture.report, nativeReports, nativeReportsUnavailable: true}});
      assert.equal(result.reportAvailable, true);
      assert.equal(result.nativeReportsUnavailable, true);
      assert.deepEqual(result.nativeReports, []);
      assert.equal(result.hits, baseline.hits);
      assert.equal(result.eligible, baseline.eligible);
      assert.equal(result.misses, baseline.misses);
      assert.equal(result.hitRate, baseline.hitRate);
      assert.equal(result.runs, baseline.runs);
      assert.deepEqual(result.savings, baseline.savings);
      assert.equal(result.savings.estimatedBuildTime?.milliseconds, milliseconds);
      assert.equal(result.savings.estimatedBuildTime?.known, 2);
      assert.equal(result.savings.estimatedBuildTime?.total, 2);
      for (const row of result.integrations) {
        if (row.id !== 'gradle' && row.id !== 'xcode') {
          assert.deepEqual(row, baseline.integrations.find(item => item.id === row.id), row.id);
          continue;
        }
        assert.equal(row.state, 'not-reported', row.id);
        assert.equal(row.coverage.state, 'unavailable', row.id);
        assert.equal(row.coverage.label, 'Reporting unavailable', row.id);
        assert.match(row.coverage.reason, /native.*(?:unavailable|could not)|(?:unavailable|could not).*native/i, row.id);
        assert.match(row.coverage.reason + ' ' + row.coverage.action, /retry|reload|refresh/i, row.id);
        assert.match(row.coverage.reason + ' ' + row.coverage.action, /operator/i, row.id);
        assert.equal(row.hits, null, row.id);
        assert.equal(row.eligible, null, row.id);
        assert.equal(row.misses, null, row.id);
        assert.equal(row.hitRate, null, row.id);
        assert.equal(row.avoidedTaskTime, null, row.id);
        assert.equal(row.nativeReport, undefined, row.id);
      }
    }
  }
});

test('omitted and false native availability flags remain compatible with older reports', () => {
  const old = projectInsights(cache());
  assert.equal(old.nativeReportsUnavailable, false);
  assert.deepEqual(old.nativeReports, []);
  assert.equal(old.integrations.find(item => item.id === 'gradle')?.coverage.state, 'no-report');
  assert.equal(old.integrations.find(item => item.id === 'xcode')?.coverage.state, 'unsupported');
  const native = nativeReport({
    reportedTasks: 1, knownEligible: 1, eligible: 1, hits: 1, hitRate: 1,
    outcomes: {executed: 0, fromCache: 1, upToDate: 0, noSource: 0, skipped: 0, failed: 0, unknown: 0},
  });
  const omitted = projectInsights(cache({nativeReports: [native]}));
  const explicit = projectInsights(cache({nativeReports: [native], nativeReportsUnavailable: false}));
  assert.deepEqual(explicit, omitted);
  assert.equal(explicit.nativeReportsUnavailable, false);
  assert.equal(explicit.integrations.find(item => item.id === 'gradle')?.hits, 1);
  assert.equal(explicit.integrations.find(item => item.id === 'gradle')?.coverage.state, 'reported');
});

test('native hit source labels retain known access scope and do not guess private ownership', () => {
  assert.equal(nativeSourceLabel('teamCache', 'personal'), 'Personal Cache');
  assert.equal(nativeSourceLabel('teamCache', 'team'), 'Team Cache');
  assert.equal(nativeSourceLabel('teamCache'), 'Private remote');
  assert.equal(nativeSourceLabel('teamCache', 'public'), 'Private remote');
  assert.equal(nativeSourceLabel('localCache', 'team'), 'Local Cache');
  assert.equal(nativeSourceLabel('publicCache', 'personal'), 'Public Cache');
  assert.equal(nativeSourceLabel('unattributed', 'team'), 'Unattributed');
  assert.equal(nativeSourceLabel('unknown-free-form-source', 'team'), 'Unattributed');
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
