import type {ActivityBucket, ArtifactReport, Cache, Estimate, NativeCompilerCounters, NativeIntegrationReport, Scope} from './cache-view.js';

export type IntegrationInsight = {
  id: string;
  name: string;
  state: 'reported' | 'not-reported';
  hits: number | null;
  eligible: number | null;
  misses: number | null;
  hitRate: number | null;
  downloadedBytes: number | null;
  uploadedBytes: number | null;
  avoidedTaskTime: Estimate | null;
  units: string;
  coverage: IntegrationCoverage;
  nativeReport?: NativeIntegrationReport;
};
export type IntegrationCoverage = {
  support: 'outcomes' | 'local-outcomes' | 'protocol' | 'unsupported';
  state: 'reported' | 'no-eligible-work' | 'no-report' | 'protocol-only' | 'timing-only' | 'unsupported' | 'unavailable';
  label: string;
  reason: string;
  action: string;
  documentation: string;
};
export type SavingsInsight = {
  avoidedTaskTime: Estimate | null;
  estimatedBuildTime: Estimate | null;
  cpuHoursSaved: null;
  moneySaved: null;
  explanation: string;
};
export type ProjectInsights = {
  storageUsedBytes: number | null;
  storageLimitBytes: number | null;
  artifacts: number | null;
  hitRate: number | null;
  hits: number | null;
  eligible: number | null;
  misses: number | null;
  runs: number | null;
  reportAvailable: boolean;
  partial: boolean;
  integrations: IntegrationInsight[];
  nativeReports: NativeIntegrationReport[];
  nativeReportsUnavailable: boolean;
  topArtifacts: ArtifactReport[];
  topArtifactsTruncated: boolean;
  activity: ActivityBucket[];
  savings: SavingsInsight;
};

const integrationNames: Record<string, string> = {
  turbo: 'Turborepo', actions: 'GitHub Actions cache', buildkit: 'Docker / BuildKit',
  gradle: 'Gradle', xcode: 'Xcode', bazel: 'Bazel', nx: 'Nx', expo: 'Expo',
};
type IntegrationGuide = Pick<IntegrationCoverage, 'support' | 'action' | 'documentation'> & {units: string; missingReason: string};
const docs = 'https://github.com/layercompute/layercache/blob/main/';
const integrationGuides: Record<string, IntegrationGuide> = {
  turbo: {support: 'outcomes', units: 'tasks', missingReason: 'No completed Turborepo task summaries were recorded in this period.', action: 'Enable summaries in the LayerCache Turbo action or CLI. Artifact requests alone do not establish task outcomes.', documentation: docs + 'action/turbo/README.md#build-time-reports'},
  actions: {support: 'outcomes', units: 'archive outcomes', missingReason: 'No GitHub Actions cache archive outcomes were recorded in this period.', action: 'Connect the workflow to the LayerCache Actions cache endpoint. Other GitHub artifact transfers are separate.', documentation: docs + 'docs/actions-cache-contract.md'},
  buildkit: {support: 'local-outcomes', units: 'vertices', missingReason: 'The hosted registry can reuse Docker layers, but hosted BuildKit outcome reporting is not available yet.', action: 'The local LayerCache buildx command records native vertex results. Registry configuration alone does not upload those reports.', documentation: docs + 'docs/native-metering.md#coverage-audit'},
  gradle: {support: 'outcomes', units: 'tasks', missingReason: 'No Gradle task outcome report was recorded in this period. Native task-cache traffic alone cannot establish task reuse.', action: 'Use a reporting-enabled LayerCache Gradle client. Keep native filesystem hits, up-to-date tasks and remote restores distinct.', documentation: docs + 'docs/native-metering.md#gradle'},
  xcode: {support: 'protocol', units: 'compiler verdicts', missingReason: 'No native compiler verdicts were recorded in this period. Xcode action lookups and CAS requests are separate protocol evidence.', action: 'Use a reporting-enabled Xcode lifecycle to publish its final summary. Compiler coverage depends on its native result parser. A snapshot restore does not establish compiler replay.', documentation: docs + 'docs/native-metering.md#xcode'},
  bazel: {support: 'unsupported', units: 'actions', missingReason: 'The native Bazel cache is supported, but build-event outcome reporting is not implemented yet.', action: 'Keep native Build Event Protocol results for qualification. AC/CAS HTTP requests do not establish reused actions.', documentation: docs + 'docs/bazel-cache.md'},
  nx: {support: 'unsupported', units: 'tasks', missingReason: 'The native Nx cache is supported, but completed task outcome reporting is not implemented yet.', action: 'Native Nx run results can distinguish task execution and cache reuse. Cache archive requests alone cannot supply those outcomes.', documentation: docs + 'docs/nx-cache.md'},
  expo: {support: 'unsupported', units: 'app artifacts', missingReason: 'The Expo provider can reuse development apps, but app-restore reporting is not uploaded yet.', action: 'Use the Expo cache provider and retain its restore result. App restores must stay separate from compiler or task hits.', documentation: docs + 'native/README.md'},
};
function coverageFor(id: string, reported: boolean, eligible: number | null, available: boolean, native?: NativeIntegrationReport, nativeUnavailable = false): IntegrationCoverage {
  const guide = integrationGuides[id] ?? {support: 'unsupported' as const, units: 'reported units', missingReason: 'This integration has no declared outcome reporting contract.', action: 'Check the integration documentation before interpreting its cache activity.', documentation: docs + 'README.md'};
  if (nativeUnavailable && (id === 'gradle' || id === 'xcode')) return {support: guide.support, state: 'unavailable', label: 'Reporting unavailable', reason: 'Native outcome reports could not be read. Turbo, Actions and BuildKit reports remain available.', action: 'Refresh project activity. If this continues, ask your operator to restore native reporting.', documentation: docs + 'docs/native-metering.md#reporting-contract-and-limits'};
  const compiler = native?.counters?.compiler;
  const compilerReported = compiler !== undefined && [compiler.hits, compiler.misses, compiler.skipped, compiler.unknown].some(count => count !== null);
  const protocolOnly = native?.integration === 'xcode' && native.reportedTasks === 0 && Boolean(native.counters?.rpc) && !compilerReported;
  const timingOnly = native?.integration === 'xcode' && Boolean(native.timingMs) && !native.counters?.rpc && !compilerReported;
  const incompleteTasks = native?.integration === 'gradle' && native.eligibilityUnknown > 0;
  const noEligibleTasks = native?.integration === 'gradle' && native.knownEligible === 0 && native.eligibilityUnknown === 0;
  const state = protocolOnly ? 'protocol-only' : timingOnly ? 'timing-only' : noEligibleTasks ? 'no-eligible-work' : reported ? eligible === 0 ? 'no-eligible-work' : 'reported' : !available ? 'unavailable' : guide.support === 'outcomes' ? 'no-report' : 'unsupported';
  const labels = {reported: 'Outcomes reported', 'no-eligible-work': 'No eligible work', 'no-report': 'No reports this period', 'protocol-only': 'Protocol activity only', 'timing-only': 'Lifecycle timing only', unsupported: guide.support === 'protocol' ? 'Compiler outcomes unavailable' : guide.support === 'local-outcomes' ? 'Hosted reports unavailable' : 'Outcome reporting unavailable', unavailable: 'Reporting unavailable'};
  const reason = protocolOnly ? 'Action-index and CAS request counters were reported. They do not establish compiler replay or eligible compiler actions.' : timingOnly ? 'Lifecycle elapsed phases were recorded. Compiler verdicts and protocol request counters are unavailable; phase duration does not measure time saved.' : compilerReported ? `Compiler verdict coverage is ${compiler.coverage} for ${compiler.scope}. These verdicts have no native task denominator or matched producer timing.` : incompleteTasks ? `${native.knownEligible.toLocaleString()} eligible tasks were reported; ${native.eligibilityUnknown.toLocaleString()} task outcomes have unknown eligibility. FROM-CACHE proves a native hit; source and timing evidence are separate.` : noEligibleTasks ? 'A completed Gradle report recorded no cache-eligible executed or restored tasks.' : reported ? eligible === 0 ? 'A completed report recorded no eligible work in this period.' : 'Counts describe the native work units reported by this integration.' : !available ? 'The project report could not be read. Usage and cache reuse remain unknown.' : guide.missingReason;
  return {support: guide.support, state, label: compilerReported ? 'Compiler verdicts reported' : incompleteTasks ? 'Partial eligibility coverage' : labels[state], reason: reason + (native?.historyTruncated ? ' Retained native history is incomplete.' : ''), action: guide.action, documentation: guide.documentation};
}
export function integrationLabel(id: string): string {return integrationNames[id] ?? 'Other integration';}
export function compilerScopeLabel(scope: NativeCompilerCounters['scope']): string {return {'clang-pch': 'Clang / PCH', swift: 'Swift', mixed: 'Mixed compiler scope', unknown: 'Unknown compiler scope'}[scope];}
export function nativeSourceLabel(source: string, scope: Scope = 'unknown'): string {
  if (source === 'teamCache') return scope === 'personal' ? 'Personal Cache' : scope === 'team' ? 'Team Cache' : 'Private remote';
  return ({localCache: 'Local Cache', publicCache: 'Public Cache', unattributed: 'Unattributed'} as Record<string, string>)[source] ?? 'Unattributed';
}
export function scopedCompilerRate(compiler: NativeCompilerCounters | undefined): number | null {
  if (!compiler || compiler.coverage !== 'complete' || compiler.scope === 'unknown' || compiler.reason && compiler.reason !== 'none' || compiler.hits === null || compiler.misses === null || compiler.unknown !== 0) return null;
  const eligible = compiler.hits + compiler.misses;
  return eligible > 0 ? compiler.hits / eligible : null;
}
export function formatDuration(value: number | null): string {
  if (value === null || !Number.isFinite(value)) return 'Unknown';
  const sign = value < 0 ? '−' : '', seconds = Math.abs(value) / 1000;
  return sign + (seconds >= 3600 ? `${(seconds / 3600).toFixed(1)} h` : seconds >= 60 ? `${(seconds / 60).toFixed(1)} min` : `${seconds.toFixed(1)} s`);
}

// Derive views from one authorized project report. Unknown integrations remain
// separate from native reporters that explicitly measured zero eligible work.
// CPU and financial savings need producer allocation and billing evidence; a
// recent host sample or a hit percentage cannot supply those facts.
export function projectInsights(cache: Cache): ProjectInsights {
  const report = cache.report, known = report?.integrations ?? [], nativeReportsUnavailable = report?.nativeReportsUnavailable === true;
  const nativeReports = nativeReportsUnavailable ? [] : report?.nativeReports ?? [];
  const ids = [...new Set([...Object.keys(integrationNames), ...known.map(item => item.integration), ...nativeReports.map(item => item.integration)])];
  const integrations = ids.map(id => {
    const entry = nativeReportsUnavailable && (id === 'gradle' || id === 'xcode') ? undefined : known.find(item => item.integration === id), native = nativeReports.find(item => item.integration === id);
    // Xcode protocol requests have no compiler denominator. Keep them separate
    // from task outcomes, including when the lifecycle itself completed.
    const nativeTasks = native?.integration === 'gradle';
    const compiler = native?.counters?.compiler;
    const compilerReported = !nativeTasks && compiler && [compiler.hits, compiler.misses, compiler.skipped, compiler.unknown].some(count => count !== null);
    const reported = Boolean(entry || nativeTasks || compilerReported), eligible = nativeTasks ? native.eligible : compilerReported ? null : entry?.eligible ?? null;
    return {
      id, name: integrationLabel(id), state: reported ? 'reported' as const : 'not-reported' as const,
      hits: nativeTasks ? native.outcomes.fromCache : compilerReported ? compiler.hits : entry?.hits ?? null, eligible,
      misses: nativeTasks ? eligible === null ? null : native.misses : compilerReported ? compiler.misses : entry ? entry.misses ?? entry.eligible - entry.hits : null,
      hitRate: nativeTasks ? native.hitRate : compilerReported ? null : entry && entry.eligible > 0 ? entry.hits / entry.eligible : null,
      downloadedBytes: entry?.bytes?.downloaded ?? null,
      uploadedBytes: entry?.bytes?.uploaded ?? null,
      avoidedTaskTime: nativeTasks ? native.grossAvoidedTaskTime : entry?.grossAvoidedTaskTime ?? null,
      units: integrationGuides[id]?.units ?? 'reported units',
      coverage: coverageFor(id, reported, eligible, Boolean(report), native, nativeReportsUnavailable),
      ...(native ? {nativeReport: native} : {}),
    };
  }).sort((a, b) => Number(b.state === 'reported') - Number(a.state === 'reported') || (b.hits ?? -1) - (a.hits ?? -1) || a.name.localeCompare(b.name));
  const gross = report?.grossAvoidedTaskTime, net = report?.netEstimatedBuildTimeSaved;
  return {
    storageUsedBytes: cache.status?.usageBytes ?? null,
    storageLimitBytes: cache.status?.maxBytes ?? null,
    artifacts: cache.status?.artifacts ?? null,
    hitRate: report && report.eligible > 0 ? report.hits / report.eligible : null,
    hits: report?.hits ?? null, eligible: report?.eligible ?? null, misses: report?.misses ?? null, runs: report?.runs ?? null,
    reportAvailable: Boolean(report), partial: report?.degraded ?? false, integrations, nativeReports, nativeReportsUnavailable,
    topArtifacts: report?.topArtifacts ?? [], topArtifactsTruncated: report?.topArtifactsTruncated ?? false,
    activity: report?.activity ?? [],
    savings: {
      avoidedTaskTime: gross && gross.total > 0 ? gross : null,
      estimatedBuildTime: net && net.total > 0 ? net : null,
      cpuHoursSaved: null, moneySaved: null,
      explanation: 'Original execution timings estimate reused task work. Tasks can overlap. Build-time estimates cover only complete timed Turbo, Actions and BuildKit graphs. Native Gradle and Xcode reports are shown separately. CPU-hours need matched producer duration and allocation; measured CPU use and bill savings need further usage and price evidence.',
    },
  };
}
