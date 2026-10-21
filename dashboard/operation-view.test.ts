import assert from 'node:assert/strict';
import test from 'node:test';
import {operationDifference, operationOverhead, operationReason} from './operation-view.ts';
import {operationFixture} from '../qa/dashboard/operation-fixtures.ts';

test('observed differences preserve negative, zero and unmeasured values', () => {
  assert.equal(operationDifference(-12_000), '−12.0 s · longer');
  assert.equal(operationDifference(20_000), '20.0 s · shorter');
  assert.equal(operationDifference(0), '0.0 s · no difference');
  assert.equal(operationDifference(null), 'Unknown');
});

test('cache phases stay unknown when an interval or phase is unmeasured', () => {
  const report = operationFixture().observations[0]!.report;
  assert.equal(operationOverhead(report), 10_000);
  assert.equal(operationOverhead({...report, phaseTimingMs: {...report.phaseTimingMs, restore: null}}), null);
  assert.equal(operationOverhead({...report, elapsedMs: 85_000}), null);
  assert.equal(operationOverhead({...report, elapsedMs: 80_005}), 10_000);
  assert.equal(operationOverhead(undefined), null);
});

test('an observed zero phase remains measured while missing phases remain unknown', () => {
  const report = operationFixture().observations[1]!.report;
  assert.equal(report.phaseTimingMs.restore, 0);
  assert.equal(operationOverhead(report), 5000);
  assert.equal(operationOverhead({...report, phaseTimingMs: {...report.phaseTimingMs, work: null}}), null);
});

test('mismatch explanations describe repairable context without reflecting raw diagnostics', () => {
  assert.equal(operationReason('sourceMismatch'), 'The source revisions differ.');
  assert.equal(operationReason('missingBaseline'), 'The selected baseline is missing or expired.');
  assert.match(operationReason('otherCachesMissing'), /not declared/);
  assert.equal(operationReason('<img src=x onerror=alert(1)>'), 'The comparison context could not be verified.');
  assert.equal(operationReason('__proto__'), 'The comparison context could not be verified.');
  assert.equal(operationReason('constructor'), 'The comparison context could not be verified.');
});
