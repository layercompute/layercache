// Synthetic observations qualify presentation, not a measured application result.
import type {OperationComparison, OperationObservation, OperationPeriodReport} from '../../dashboard/operation-view.js';

export function operationFixture(): OperationPeriodReport {
  const baseline: OperationObservation = {
    receiptId: 'a'.repeat(64), integration: 'gradle', sourceCommit: 'd'.repeat(40), profile: 'android-gradle-qualified-v1',
    report: {version: 1, operationId: 'run-baseline-operation', sessionId: 'run-declared-session',
      workloadDigest: 'sha256:' + '1'.repeat(64), coverageDigest: 'sha256:' + '2'.repeat(64), environmentDigest: 'sha256:' + '3'.repeat(64), otherCachesDigest: 'sha256:' + '4'.repeat(64),
      outputState: 'fresh', cachePolicy: 'bypass', cachePreparation: 'bypassed', status: 'success',
      startedAt: '2026-10-21T00:00:00Z', finishedAt: '2026-10-21T00:01:40Z', elapsedMs: 100_000,
      phaseTimingMs: {setup: 2000, restore: 0, work: 95_000, close: 1000, publish: 2000}},
  };
  const candidate: OperationObservation = {...structuredClone(baseline), receiptId: 'b'.repeat(64), report: {...structuredClone(baseline.report),
    operationId: 'run-current-operation', baselineReceiptId: baseline.receiptId, cachePolicy: 'enabled', cachePreparation: 'hit',
    startedAt: '2026-10-21T00:02:00Z', finishedAt: '2026-10-21T00:03:20Z', elapsedMs: 80_000,
    phaseTimingMs: {setup: 1000, restore: 6000, work: 70_000, close: 1000, publish: 2000}},
  };
  const comparison: OperationComparison = {baselineReceiptId: baseline.receiptId, candidateReceiptId: candidate.receiptId, method: 'bypassToEnabled', matched: true, reasons: [],
    baselineElapsedMs: 100_000, candidateElapsedMs: 80_000, observedDifferenceMs: 20_000, observedWorkDifferenceMs: 25_000, observedOverheadDifferenceMs: -5000,
    assumptions: ['callerDeclaredContext', 'sameSessionWarmthMayDiffer', 'observedNotCausal', 'cpuAndCostUnknown']};
  return {observations: [candidate, baseline], comparisons: [comparison], storedObservations: 2, historyTruncated: false};
}
