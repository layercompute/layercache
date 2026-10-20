import {bytes} from '../dashboard/cache-view.js';
import type {ExecutionEntry, ExecutionProject, ResourceSample} from '../dashboard/execution-view.js';

type Job = {entry: ExecutionEntry; jobId?: string; cacheObserved: boolean};
type Run = {key: string; repositoryId?: string; runId?: string; attempt?: string; jobs: Job[]; latest: string};
const maximumEntries = 4096;
const maximumRuns = 50;
const maximumClients = 25;
const providers = {github: 'GitHub', ubicloud: 'Ubicloud', selfHosted: 'Self-hosted', unknown: 'Unknown'};
const filesystemRoles: Record<string, string> = {workspace: 'Workspace', localCache: 'Local cache', staging: 'Temporary data'};
// These fields are issued by the authenticated CI authority. Runner names and
// reported hardware never determine whether two jobs belong to the same run.
const ciIdentity = /^github-actions:([1-9][0-9]{0,19}):([1-9][0-9]{0,19}):([1-9][0-9]{0,19}):([1-9][0-9]{0,19})$/;

function el<K extends keyof HTMLElementTagNameMap>(tag: K, text = '', className = ''): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  node.textContent = text;
  node.className = className;
  return node;
}
function validNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0;
}
function date(value: string): number {return Date.parse(value) || 0;}
function count(value: number, singular: string, plural = `${singular}s`): string {
  return `${value.toLocaleString()} ${value === 1 ? singular : plural}`;
}
function amount(value: unknown): string {return validNumber(value) ? bytes(value) : 'Unknown';}
function cpu(value: unknown): string {
  return validNumber(value) ? `${(value / 1000).toLocaleString(undefined, {maximumFractionDigits: 3})} vCPU` : 'CPU limit unknown';
}
function platform(entry: ExecutionEntry): string {
  const os: Record<string, string> = {linux: 'Linux', darwin: 'macOS', windows: 'Windows', other: 'Other OS'};
  const arch: Record<string, string> = {amd64: 'x64', arm64: 'ARM64', other: 'Other architecture'};
  return [entry.sample.os ? os[entry.sample.os] ?? 'Unknown OS' : '', entry.sample.arch ? arch[entry.sample.arch] ?? 'Unknown architecture' : ''].filter(Boolean).join(' · ') || 'Platform unknown';
}
function sampledAt(value: string): HTMLElement {
  const node = el('time', 'Sample time unknown', 'infra-sampled');
  if (date(value)) {
    node.dateTime = new Date(value).toISOString();
    node.textContent = `${new Intl.DateTimeFormat(undefined, {month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', timeZone: 'UTC'}).format(new Date(value))} UTC`;
    node.title = node.dateTime;
  }
  return node;
}
function metric(parent: HTMLElement, name: string, value: string): void {
  const item = el('div');
  item.append(el('dt', name), el('dd', value));
  parent.append(item);
}
function provider(entry: ExecutionEntry): string {
  if (entry.provider === 'unknown' || !providers[entry.provider]) return 'Compute provider unknown';
  const evidence = entry.providerEvidence === 'signed' ? 'signed' : entry.providerEvidence === 'declared' ? 'declared' : 'unconfirmed';
  return `${providers[entry.provider]} · ${evidence}`;
}
function repositoryPath(repository: string | undefined): string | undefined {
  if (!repository) return;
  // Project repositories are normally github.com/owner/repository. Also accept
  // the owner/repository form used by self-serve project creation.
  const match = /^(?:https:\/\/)?(?:github\.com\/)?([A-Za-z0-9](?:[A-Za-z0-9-]{0,38}))\/([A-Za-z0-9_.-]{1,100})$/.exec(repository);
  if (!match || match[2] === '.' || match[2] === '..') return;
  return `${match[1]}/${match[2]}`;
}
function link(text: string, path: string): HTMLAnchorElement {
  const node = el('a', text);
  node.href = `https://github.com/${path}`;
  node.target = '_blank';
  node.rel = 'noopener noreferrer';
  return node;
}

function resources(sample: ResourceSample): HTMLElement {
  const section = el('div', '', 'infra-resources');
  const limits = el('dl', '', 'infra-resource-grid');
  metric(limits, 'Effective CPU limit', cpu(sample.cpu?.effectiveMilliCPU));
  metric(limits, 'Effective memory limit', amount(sample.memory?.effectiveLimitBytes));
  metric(limits, 'Visible logical CPUs', validNumber(sample.cpu?.visibleLogical) ? sample.cpu.visibleLogical.toLocaleString() : 'Unknown');
  metric(limits, 'Visible memory', amount(sample.memory?.visibleTotalBytes));
  metric(limits, 'CPU affinity', validNumber(sample.cpu?.affinityLogical) ? sample.cpu.affinityLogical.toLocaleString() : 'Unknown');
  metric(limits, 'CPU quota', cpu(sample.cpu?.quotaMilliCPU));
  metric(limits, 'Container memory limit', amount(sample.memory?.cgroupLimitBytes));
  section.append(limits);
  const volumes = new Map<string, NonNullable<ResourceSample['filesystems']>[number]>();
  for (const volume of (sample.filesystems ?? []).slice(0, 64)) {
    const previous = volumes.get(volume.id);
    if (previous) previous.roles = Array.from(new Set([...(previous.roles ?? []), ...(volume.roles ?? [])]));
    else volumes.set(volume.id, {...volume, roles: [...(volume.roles ?? [])]});
  }
  if (!volumes.size) section.append(el('p', 'No filesystem sample reported.', 'muted'));
  for (const volume of volumes.values()) {
    const card = el('section', '', 'infra-volume');
    const roles = (volume.roles ?? []).map(role => filesystemRoles[role] ?? 'Other data').join(' · ');
    card.append(el('h4', roles || 'Filesystem'));
    const stats = el('dl', '', 'infra-resource-grid');
    const capacity = volume.capacityBytes;
    const used = validNumber(capacity) && validNumber(volume.freeBytes) && volume.freeBytes <= capacity ? capacity - volume.freeBytes : undefined;
    const percent = (value: unknown): string => validNumber(value) && validNumber(capacity) && capacity > 0 ? `${bytes(value)} · ${(value / capacity * 100).toLocaleString(undefined, {maximumFractionDigits: 1})}%` : amount(value);
    metric(stats, 'Capacity', amount(capacity));
    metric(stats, 'Used', percent(used));
    metric(stats, 'Free', percent(volume.freeBytes));
    metric(stats, 'Available', percent(volume.availableBytes));
    const reserved = validNumber(volume.freeBytes) && validNumber(volume.availableBytes) && volume.availableBytes <= volume.freeBytes ? volume.freeBytes - volume.availableBytes : undefined;
    metric(stats, 'Reserved', percent(reserved));
    card.append(stats);
    section.append(card);
  }
  return section;
}

function runs(entries: ExecutionEntry[]): Run[] {
  const grouped = new Map<string, Run>();
  const jobs = new Map<string, Job>();
  for (const entry of entries.slice(0, maximumEntries)) {
    if (entry.kind !== 'githubActions') continue;
    const parts = ciIdentity.exec(entry.executionId);
    const jobKey = parts ? entry.executionId : entry.id;
    const previous = jobs.get(jobKey);
    if (previous) {
      previous.cacheObserved ||= entry.cacheActivity === 'observed';
      if (date(entry.lastObservedAt) > date(previous.entry.lastObservedAt)) previous.entry = entry;
      continue;
    }
    const job: Job = {entry, jobId: parts?.[4], cacheObserved: entry.cacheActivity === 'observed'};
    jobs.set(jobKey, job);
    // Malformed or unknown identities stay separate. A shared runner or a
    // missing run ID cannot establish a common workflow run.
    const key = parts ? `${parts[1]}:${parts[2]}:${parts[3]}` : `unknown:${entry.id}`;
    let run = grouped.get(key);
    if (!run) {
      run = {key, repositoryId: parts?.[1], runId: parts?.[2], attempt: parts?.[3], jobs: [], latest: entry.lastObservedAt};
      grouped.set(key, run);
    }
    run.jobs.push(job);
  }
  for (const run of grouped.values()) {
    run.jobs.sort((a, b) => date(b.entry.lastObservedAt) - date(a.entry.lastObservedAt) || a.entry.id.localeCompare(b.entry.id));
    run.latest = run.jobs[0]?.entry.lastObservedAt ?? '';
  }
  return Array.from(grouped.values()).sort((a, b) => date(b.latest) - date(a.latest) || a.key.localeCompare(b.key));
}

function renderJob(job: Job, repository: string | undefined, run: Run): HTMLElement {
  const {entry} = job;
  const details = el('details', '', 'infra-job');
  details.dataset.executionId = entry.id;
  const summary = el('summary');
  summary.append(el('strong', job.jobId ? `Job ${job.jobId}` : 'CI job', 'infra-job-name'), el('span', platform(entry), 'infra-job-platform'), el('span', cpu(entry.sample.cpu?.effectiveMilliCPU), 'infra-job-cpu'), el('span', validNumber(entry.sample.memory?.effectiveLimitBytes) ? bytes(entry.sample.memory.effectiveLimitBytes) : 'Memory limit unknown', 'infra-job-memory'), el('span', job.cacheObserved ? 'Cache activity linked' : 'Cache activity unconfirmed', `infra-badge ${job.cacheObserved ? 'positive' : ''}`));
  details.append(summary);
  const meta = el('div', '', 'infra-job-meta');
  meta.append(el('span', provider(entry)), el('span', `Runner classification · ${entry.runnerEnvironment || 'unknown'}`), sampledAt(entry.lastObservedAt));
  if (repository && run.runId && job.jobId) meta.append(link('Open job in GitHub', `${repository}/actions/runs/${run.runId}/job/${job.jobId}`));
  details.append(meta, resources(entry.sample), el('p', `${entry.phase === 'finish' ? 'Finish' : 'Start'} sample. Cache activity linkage does not establish cache hits or task savings.`, 'coverage-note'));
  return details;
}

function renderRun(run: Run, repository: string | undefined): HTMLElement {
  const details = el('details', '', 'infra-run');
  if (run.runId) details.dataset.runId = run.runId;
  if (run.attempt) details.dataset.attempt = run.attempt;
  if (run.repositoryId) details.dataset.repositoryId = run.repositoryId;
  const summary = el('summary');
  const title = el('div', '', 'infra-run-title');
  title.append(el('strong', run.runId ? `Run ${run.runId}` : 'Unidentified run'), el('span', run.attempt ? `Attempt ${run.attempt}` : 'Run identity unavailable', 'muted'));
  const linked = run.jobs.filter(job => job.cacheObserved).length;
  summary.append(title, el('span', count(run.jobs.length, 'job'), 'infra-run-count'), el('span', `${linked.toLocaleString()} with cache activity`, 'infra-run-activity'), sampledAt(run.latest));
  details.append(summary);
  const content = el('div', '', 'infra-run-content');
  if (repository && run.runId) content.append(link('Open run in GitHub', `${repository}/actions/runs/${run.runId}/attempts/${run.attempt}`));
  for (const job of run.jobs) content.append(renderJob(job, repository, run));
  details.append(content);
  return details;
}

function clients(entries: ExecutionEntry[]): ExecutionEntry[] {
  const installations = new Map<string, ExecutionEntry>();
  for (const entry of entries.slice(0, maximumEntries)) {
    if (entry.kind !== 'client') continue;
    const key = `${entry.ownerId}\0${entry.installationId || entry.id}`;
    const previous = installations.get(key);
    if (!previous || date(entry.lastObservedAt) > date(previous.lastObservedAt)) installations.set(key, entry);
  }
  return Array.from(installations.values()).sort((a, b) => date(b.lastObservedAt) - date(a.lastObservedAt) || a.id.localeCompare(b.id));
}

export function renderInfrastructure(project: ExecutionProject): HTMLElement {
  const panel = el('section', '', 'infrastructure-panel');
  panel.setAttribute('aria-label', 'Project infrastructure');
  const inventory = project.executions;
  if (!inventory || inventory.version !== 1 || !Array.isArray(inventory.installations) || !Array.isArray(inventory.jobs)) {
    panel.append(el('section', '', 'card'));
    panel.firstElementChild!.append(el('h2', 'Execution inventory unavailable'), el('p', 'This project has not returned a supported resource inventory. Older clients can continue caching without reporting it.', 'muted'));
    return panel;
  }
  const groupedRuns = runs(inventory.jobs);
  const installations = clients(inventory.installations);
  const jobs = groupedRuns.flatMap(run => run.jobs);
  const summary = el('dl', '', 'infra-summary');
  metric(summary, 'GitHub Actions run attempts', groupedRuns.length.toLocaleString());
  metric(summary, 'Observed CI jobs', jobs.length.toLocaleString());
  metric(summary, 'Persistent clients', installations.length.toLocaleString());
  metric(summary, 'Jobs with cache activity', jobs.filter(job => job.cacheObserved).length.toLocaleString());
  panel.append(summary);
  if (inventory.historyTruncated || inventory.listTruncated || inventory.jobs.length > maximumEntries || inventory.installations.length > maximumEntries) {
    panel.append(el('p', 'Partial inventory. Retained history or returned rows are limited; displayed counts may omit runs and jobs.', 'coverage-note infra-partial'));
  }
  const ci = el('section', '', 'card infra-ci');
  ci.setAttribute('aria-label', 'GitHub Actions');
  ci.append(el('h2', 'GitHub Actions'), el('p', 'Jobs are grouped by authenticated repository, run and attempt. Expand a run to inspect compute and cache activity.', 'muted'));
  const repository = repositoryPath(project.repository);
  for (const run of groupedRuns.slice(0, maximumRuns)) ci.append(renderRun(run, repository));
  if (!groupedRuns.length) ci.append(el('p', 'No CI jobs were returned for this project. Instrumented jobs appear here after reporting a resource sample.', 'infra-empty'));
  if (groupedRuns.length > maximumRuns) ci.append(el('p', `Showing the newest ${maximumRuns} of ${count(groupedRuns.length, 'run attempt')}.`, 'coverage-note'));
  panel.append(ci);
  const clientSection = el('section', '', 'card infra-clients');
  clientSection.setAttribute('aria-label', 'Persistent clients');
  clientSection.append(el('h2', 'Persistent clients'), el('p', 'Persistent client identities are separate from temporary CI jobs. Each row is one client installation; copied state or separate OS users can prevent a one-to-one mapping to hardware.', 'muted'));
  for (const entry of installations.slice(0, maximumClients)) {
    const details = el('details', '', 'infra-client');
    details.dataset.installationId = entry.installationId || entry.id;
    const identity = entry.installationId || entry.id;
    const title = `Client ${identity.slice(0, 8)}`;
    const summary = el('summary');
    summary.append(el('strong', title), el('span', platform(entry)), el('span', cpu(entry.sample.cpu?.effectiveMilliCPU)), sampledAt(entry.lastObservedAt));
    details.append(summary);
    const stale = date(entry.lastObservedAt) && Date.now() - date(entry.lastObservedAt) > (inventory.staleAfterSeconds || 86400) * 1000;
    details.append(el('p', stale ? 'The latest sample is older than the reporting freshness window. Current connectivity is unknown.' : 'Latest reported resource sample. Current connectivity is unknown.', 'coverage-note'));
    details.append(el('p', entry.cacheActivity === 'observed' ? 'Cache activity linked to this installation.' : 'Cache activity is unconfirmed for this installation.', 'coverage-note infra-client-activity'));
    details.append(resources(entry.sample));
    clientSection.append(details);
  }
  if (!installations.length) clientSection.append(el('p', 'No persistent clients were returned for this project.', 'infra-empty'));
  if (installations.length > maximumClients) clientSection.append(el('p', `Showing the newest ${maximumClients} of ${count(installations.length, 'client installation')}.`, 'coverage-note'));
  clientSection.append(el('p', 'The inventory retains the latest sample per installation. Repeat sightings and unique physical host counts are not available.', 'coverage-note'));
  panel.append(clientSection);
  const note = el('details', '', 'infra-coverage');
  note.append(el('summary', 'Inventory coverage'), el('p', `Observations are retained for ${inventory.retentionDays} days. Counts cover returned observations, not all project executions. Compute providers are shown with their evidence; orchestration alone does not identify the compute provider.`, 'coverage-note'), el('p', 'CPU and memory are reported limits, not measured utilization. Filesystem figures cover complete filesystems, not this project’s cached bytes. Storage is not added across jobs or clients.', 'coverage-note'));
  if (date(inventory.from) && date(inventory.to)) note.append(el('p', `${new Date(inventory.from).toISOString()} to ${new Date(inventory.to).toISOString()}`, 'coverage-note'));
  panel.append(note);
  return panel;
}
