import {formatDuration, integrationLabel} from './insights.ts';

export type OperationPhases = {setup: number | null; restore: number | null; work: number | null; close: number | null; publish: number | null};
export type OperationReport = {
  version: 1; operationId: string; sessionId?: string;
  workloadDigest: string; coverageDigest: string; environmentDigest?: string; otherCachesDigest?: string;
  outputState: 'fresh' | 'incremental' | 'unknown'; cachePolicy: 'enabled' | 'bypass' | 'unknown';
  cachePreparation: 'hit' | 'miss' | 'mixed' | 'unavailable' | 'bypassed' | 'unknown';
  baselineReceiptId?: string; startedAt: string; finishedAt: string; elapsedMs: number;
  status: 'success' | 'failed' | 'cancelled' | 'unknown'; phaseTimingMs: OperationPhases;
};
export type OperationObservation = {receiptId: string; integration: 'gradle' | 'xcode'; sourceCommit?: string; profile: string; report: OperationReport};
export type OperationComparison = {
  baselineReceiptId: string; candidateReceiptId: string; method: 'bypassToEnabled' | 'coldToWarm' | 'unmatched';
  matched: boolean; reasons: string[]; baselineElapsedMs: number | null; candidateElapsedMs: number;
  observedDifferenceMs: number | null; observedWorkDifferenceMs: number | null; observedOverheadDifferenceMs: number | null;
  assumptions: string[];
};
export type OperationPeriodReport = {observations: OperationObservation[]; comparisons: OperationComparison[]; storedObservations: number; historyTruncated: boolean; unavailable?: boolean};

const phases = [['setup', 'Setup'], ['restore', 'Restore'], ['work', 'Work'], ['close', 'Close'], ['publish', 'Publish']] as const;
const methods = {bypassToEnabled: 'Bypass versus cache enabled', coldToWarm: 'Cold versus warm cache', unmatched: 'Comparison unavailable'};
const preparation = {hit: 'Hit', miss: 'Miss', mixed: 'Mixed', unavailable: 'Unavailable', bypassed: 'Bypassed', unknown: 'Unknown'};
const policies = {enabled: 'Cache enabled', bypass: 'Cache bypassed', unknown: 'Cache policy unknown'};
const limits = 'Observed elapsed differences include cache preparation and update overhead. They do not measure CPU usage, provider charges or whole-project savings.';
const context = 'The caller declares workload, assertion coverage, environment, other caches and session. A session does not attest to one physical machine. OS page cache, JVM and dependency warmth can still differ.';

function el<K extends keyof HTMLElementTagNameMap>(tag: K, text = '', className = ''): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag); node.textContent = text; node.className = className; return node;
}
function short(value: string): string {return value.length > 20 ? `${value.slice(0, 12)}…${value.slice(-6)}` : value;}
function time(value: string): string {return Number.isFinite(Date.parse(value)) ? new Date(value).toISOString().replace('T', ' ').replace('.000Z', 'Z').replace('Z', ' UTC') : 'Unknown';}
export function operationDifference(value: number | null): string {
  return value === null ? 'Unknown' : value === 0 ? '0.0 s · no difference' : `${formatDuration(value)} · ${value > 0 ? 'shorter' : 'longer'}`;
}
export function operationOverhead(report?: OperationReport): number | null {
  if (!report || phases.some(([key]) => report.phaseTimingMs[key] === null)) return null;
  const measured = phases.reduce((total, [key]) => total + report.phaseTimingMs[key]!, 0);
  if (Math.abs(report.elapsedMs - measured) > 5) return null;
  return report.phaseTimingMs.setup! + report.phaseTimingMs.restore! + report.phaseTimingMs.close! + report.phaseTimingMs.publish!;
}

// Render only fixed refusal text. Raw reason strings are never reflected into UI.
export function operationReason(reason: string): string {
  const reasons: Record<string, string> = {
    missingBaseline: 'The selected baseline is missing or expired.',
    baselineReceiptMismatch: 'The selected baseline receipt does not match.',
    projectMismatch: 'The operations belong to different projects.',
    integrationMismatch: 'The operations use different integrations.',
    profileMismatch: 'The compatibility profiles differ.',
    sourceMissing: 'An immutable source revision is missing.',
    sourceMismatch: 'The source revisions differ.',
    unsuccessfulOperation: 'Both operations must have completed successfully.',
    sessionMissing: 'A declared session is missing.',
    sessionMismatch: 'The declared sessions differ.',
    workloadMismatch: 'The declared workloads differ.',
    coverageMismatch: 'The declared assertion coverage differs.',
    environmentMissing: 'A declared environment identity is missing.',
    environmentMismatch: 'The declared environments differ.',
    otherCachesMissing: 'The policy for other caches was not declared.',
    otherCachesMismatch: 'The policies for other caches differ.',
    outputNotFresh: 'Both operations must declare fresh output trees.',
    operationOrderMismatch: 'The baseline must finish before the current operation starts.',
    comparisonWindowExceeded: 'The operations are more than 24 hours apart.',
    cachePolicyMismatch: 'The cache policies do not form a bypass/enabled or cold/warm pair.',
  };
  return Object.hasOwn(reasons, reason) ? reasons[reason]! : 'The comparison context could not be verified.';
}
function value(list: HTMLDListElement, label: string, text: string): void {
  const item = el('div'); item.append(el('dt', label), el('dd', text)); list.append(item);
}
function identity(observation: OperationObservation): HTMLElement {
  const node = el('p', '', 'coverage-note operation-identity');
  const receipt = el('span', `Receipt ${short(observation.receiptId)}`); receipt.title = observation.receiptId;
  const profile = el('span', `Profile ${observation.profile}`);
  const source = el('span', observation.sourceCommit ? `Source ${short(observation.sourceCommit)}` : 'Source unavailable');
  if (observation.sourceCommit) source.title = observation.sourceCommit;
  node.append(receipt, profile, source); return node;
}
function phaseTable(baseline: OperationObservation | undefined, candidate: OperationObservation): HTMLElement {
  const wrapper = el('div', '', 'table-scroll');
  const table = el('table', '', 'operation-phases'); table.setAttribute('aria-label', 'Operation phase timings');
  const head = el('thead'), headings = el('tr');
  for (const name of ['Phase', 'Baseline', 'Current']) {const cell = el('th', name); cell.scope = 'col'; headings.append(cell);}
  head.append(headings); table.append(head); const body = el('tbody');
  for (const [key, label] of phases) {
    const row = el('tr'), heading = el('th', label); heading.scope = 'row'; row.dataset.phase = key;
    row.append(heading, el('td', formatDuration(baseline?.report.phaseTimingMs[key] ?? null)), el('td', formatDuration(candidate.report.phaseTimingMs[key]))); body.append(row);
  }
  const row = el('tr', '', 'operation-overhead'), heading = el('th', 'Measured cache phases'); heading.scope = 'row';
  row.append(heading, el('td', formatDuration(operationOverhead(baseline?.report))), el('td', formatDuration(operationOverhead(candidate.report)))); body.append(row);
  table.append(body); wrapper.append(table); return wrapper;
}
function observationCard(observation: OperationObservation, comparison: OperationComparison | undefined, observations: Map<string, OperationObservation>): HTMLElement {
  const report = observation.report, baseline = comparison ? observations.get(comparison.baselineReceiptId) : undefined;
  const card = el('details', '', 'operation-observation execution-details'); card.dataset.receiptId = observation.receiptId;
  const summary = el('summary', `${integrationLabel(observation.integration)} · ${policies[report.cachePolicy]} · ${formatDuration(report.elapsedMs)} · ${report.status}`);
  const title = el('span', comparison?.matched ? operationDifference(comparison.observedDifferenceMs) : comparison ? 'Comparison unavailable' : 'No selected baseline', 'operation-result');
  summary.append(title); card.append(summary, el('p', `${time(report.startedAt)} · Cache preparation ${preparation[report.cachePreparation]} · ${report.outputState === 'fresh' ? 'Fresh outputs declared' : report.outputState === 'incremental' ? 'Incremental outputs declared' : 'Output state unknown'}`, 'coverage-note'));
  const metrics = el('dl', '', 'operation-metrics');
  value(metrics, 'Baseline complete operation', formatDuration(comparison?.baselineElapsedMs ?? null));
  value(metrics, 'Current complete operation', formatDuration(report.elapsedMs));
  value(metrics, 'Observed elapsed difference', comparison?.matched ? operationDifference(comparison.observedDifferenceMs) : 'Unknown');
  card.append(metrics);
  if (comparison) {
    card.append(el('p', methods[comparison.method], 'operation-method'));
    const selected = el('p', `Selected baseline receipt ${short(comparison.baselineReceiptId)}`, 'coverage-note operation-baseline'); selected.title = comparison.baselineReceiptId; card.append(selected);
    if (!comparison.matched) {
      const reasons = el('ul', '', 'operation-reasons');
      for (const reason of [...new Set(comparison.reasons)]) reasons.append(el('li', operationReason(reason)));
      if (!reasons.childElementCount) reasons.append(el('li', 'The comparison context could not be verified.'));
      card.append(reasons);
    } else {
      const differences = el('dl', '', 'operation-metrics');
      value(differences, 'Observed work difference', operationDifference(comparison.observedWorkDifferenceMs));
      value(differences, 'Observed cache-phase difference', operationDifference(comparison.observedOverheadDifferenceMs)); card.append(differences);
    }
    if (!baseline && comparison.baselineElapsedMs !== null) card.append(el('p', 'The selected baseline is outside the returned observations. Its retained total is shown; phase details are unavailable here.', 'coverage-note'));
  } else card.append(el('p', 'This observation has no selected baseline. Its elapsed time does not establish time saved.', 'coverage-note'));
  card.append(phaseTable(baseline, observation), el('p', 'Phases are disjoint measured intervals. Unknown phases are unmeasured. Measured cache phases combine setup, restore, close and publish only when every phase covers the complete interval within clock rounding. The complete operation ends after the final cache update acknowledgement. Report upload is outside this interval.', 'coverage-note'), identity(observation));
  return card;
}

export function renderOperations(report?: OperationPeriodReport, unavailable = false, compact = false): HTMLElement | null {
  if (!report && !unavailable) return null;
  const section = el('section', '', `operation-results${compact ? ' operation-compact' : ''}`); section.setAttribute('aria-label', 'Operation timings');
  section.append(el('h3', 'Operation timings'));
  if (unavailable || report?.unavailable) {
    section.append(el('p', 'Operation reporting unavailable. Refresh to retry. Other cache and native results remain available.', 'coverage-note operation-unavailable')); return section;
  }
  const observations = report!.observations.slice(0, 32), comparisons = report!.comparisons.slice(0, 32);
  const matched = comparisons.filter(item => item.matched).length;
  section.append(el('p', `${report!.storedObservations.toLocaleString()} recorded operations · ${matched.toLocaleString()} matched comparisons in returned observations`, 'coverage-note operation-counts'));
  if (!observations.length) section.append(el('p', 'No operation timings were recorded in this period. Add prepare, work and update phase markers to observe a complete operation. A cache hit alone does not measure its elapsed benefit.', 'coverage-note operation-empty'));
  else if (compact) {
    const latest = observations[0]!, comparison = comparisons.find(item => item.candidateReceiptId === latest.receiptId);
    section.append(el('p', `Latest ${integrationLabel(latest.integration)} operation · ${formatDuration(latest.report.elapsedMs)} · ${policies[latest.report.cachePolicy]} · ${latest.report.status}`, 'operation-latest'));
    section.append(el('p', comparison?.matched ? `Observed elapsed difference ${operationDifference(comparison.observedDifferenceMs)}. Each comparison stays separate.` : comparison ? `Comparison unavailable. ${operationReason(comparison.reasons[0] ?? '')}` : 'No selected baseline. This elapsed observation does not establish time saved.', 'coverage-note'));
  } else {
    const byReceipt = new Map(observations.map(item => [item.receiptId, item]));
    for (const observation of observations) section.append(observationCard(observation, comparisons.find(item => item.candidateReceiptId === observation.receiptId), byReceipt));
  }
  if (report!.historyTruncated || report!.storedObservations > observations.length) section.append(el('p', `Showing ${observations.length.toLocaleString()} recent observations. Returned or retained history is incomplete.`, 'coverage-note operation-truncated'));
  section.append(el('p', limits, 'coverage-note'), el('p', 'Pairs are not added together. Reused baselines and overlapping observations cannot supply a whole-project savings total.', 'coverage-note operation-no-total'));
  if (!compact) section.append(el('p', context, 'coverage-note operation-context'));
  return section;
}
