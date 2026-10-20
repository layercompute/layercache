import type {ActivityBucket, ArtifactReport, Cache, Estimate} from './cache-view.js';

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
  topArtifacts: ArtifactReport[];
  topArtifactsTruncated: boolean;
  activity: ActivityBucket[];
  savings: SavingsInsight;
};

const integrationNames: Record<string, string> = {
  turbo: 'Turborepo', actions: 'GitHub Actions cache', buildkit: 'Docker / BuildKit',
  gradle: 'Gradle', xcode: 'Xcode', bazel: 'Bazel', nx: 'Nx', expo: 'Expo',
};
export function integrationLabel(id: string): string {return integrationNames[id] ?? 'Other integration';}
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
  const report = cache.report, known = report?.integrations ?? [];
  const ids = [...new Set([...Object.keys(integrationNames), ...known.map(item => item.integration)])];
  const integrations = ids.map(id => {
    const entry = known.find(item => item.integration === id);
    return {
      id, name: integrationLabel(id), state: entry ? 'reported' as const : 'not-reported' as const,
      hits: entry?.hits ?? null, eligible: entry?.eligible ?? null,
      misses: entry ? entry.misses ?? entry.eligible - entry.hits : null,
      hitRate: entry && entry.eligible > 0 ? entry.hits / entry.eligible : null,
      downloadedBytes: entry?.bytes?.downloaded ?? null,
      uploadedBytes: entry?.bytes?.uploaded ?? null,
      avoidedTaskTime: entry?.grossAvoidedTaskTime ?? null,
      units: id === 'buildkit' ? 'vertices' : id === 'actions' ? 'archive outcomes' : id === 'turbo' ? 'tasks' : 'reported units',
    };
  }).sort((a, b) => Number(b.state === 'reported') - Number(a.state === 'reported') || (b.hits ?? -1) - (a.hits ?? -1) || a.name.localeCompare(b.name));
  const gross = report?.grossAvoidedTaskTime, net = report?.netEstimatedBuildTimeSaved;
  return {
    storageUsedBytes: cache.status?.usageBytes ?? null,
    storageLimitBytes: cache.status?.maxBytes ?? null,
    artifacts: cache.status?.artifacts ?? null,
    hitRate: report && report.eligible > 0 ? report.hits / report.eligible : null,
    hits: report?.hits ?? null, eligible: report?.eligible ?? null, misses: report?.misses ?? null, runs: report?.runs ?? null,
    reportAvailable: Boolean(report), partial: report?.degraded ?? false, integrations,
    topArtifacts: report?.topArtifacts ?? [], topArtifactsTruncated: report?.topArtifactsTruncated ?? false,
    activity: report?.activity ?? [],
    savings: {
      avoidedTaskTime: gross && gross.total > 0 ? gross : null,
      estimatedBuildTime: net && net.total > 0 ? net : null,
      cpuHoursSaved: null, moneySaved: null,
      explanation: 'Original execution timings estimate reused task work. Tasks can overlap. Build-time estimates use complete timed graphs. CPU-hours and bill savings need producer resource and price evidence.',
    },
  };
}
