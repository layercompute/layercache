export type GradleNativeTask = {
  workRef: string;
  nativeKey?: string;
  cacheEligibility?: 'eligible' | 'ineligible' | 'unknown';
  outcome: 'executed' | 'fromCache' | 'upToDate' | 'noSource' | 'skipped' | 'failed' | 'unknown';
  startedAt: string;
  finishedAt: string;
  executionMs?: number;
};
export type GradleNativeReport = {
  version: 1;
  invocationId: string;
  startedAt: string;
  finishedAt: string;
  status: 'success' | 'failed' | 'unknown';
  tasks: GradleNativeTask[];
};
export type GradleReportProjection = {body: GradleNativeReport; keyEvidence: string};
type ProjectionScope = {project: string; compatibility: string; now?: number};
export function readBoundedFile(path: string, maximum: number, options?: {privateFile?: boolean}): {text: string; mtimeMs: number; bytes: number};
export function projectGradleInvocation(document: unknown, options: ProjectionScope & {
  infoText?: string; infoMtimeMs?: number; invocationCount?: number;
}): GradleReportProjection;
export function readGradleReports(directory: string, options: ProjectionScope & {infoLogPath?: string}): GradleReportProjection[];
