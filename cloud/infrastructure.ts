import {bytes} from '../dashboard/cache-view.js';
import type {ExecutionEntry, ExecutionProject, ResourceSample} from '../dashboard/execution-view.js';

type Job = {entry: ExecutionEntry; jobId?: string; cacheObserved: boolean};
type Run = {key: string; repositoryId?: string; runId?: string; attempt?: string; jobs: Job[]; latest: string};
const maximumEntries = 4096;
const maximumObservedExecutions = maximumEntries * 2;
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
    if (!previous) installations.set(key, entry);
    else {
      const selected = date(entry.lastObservedAt) > date(previous.lastObservedAt) ? entry : previous;
      // Each count already covers an installation. Duplicate returned samples
      // can overlap, so adding their counts would manufacture recurrence.
      const observedExecutions = Math.max(executionCount(previous) ?? 0, executionCount(entry) ?? 0) || undefined;
      installations.set(key, {...selected, observedExecutions});
    }
  }
  return Array.from(installations.values()).sort((a, b) => date(b.lastObservedAt) - date(a.lastObservedAt) || a.id.localeCompare(b.id));
}

function executionCount(entry: ExecutionEntry): number | undefined {
  return validNumber(entry.observedExecutions) && entry.observedExecutions > 0 && entry.observedExecutions <= maximumObservedExecutions ? entry.observedExecutions : undefined;
}
function freshClient(entry: ExecutionEntry, staleAfter: number, now: number): boolean {
  const observed = date(entry.lastObservedAt);
  return observed > 0 && observed <= now && now - observed <= staleAfter * 1000;
}
function renderClient(entry: ExecutionEntry, staleAfter: number, now: number): HTMLElement {
  const details = el('details', '', 'infra-client');
  details.dataset.installationId = entry.installationId || entry.id;
  const identity = entry.installationId || entry.id;
  const title = `Client ${identity.slice(0, 8)}`;
  const observations = executionCount(entry);
  if (observations !== undefined) details.dataset.observedExecutions = String(observations);
  const summary = el('summary');
  const recurrence = el('span', observations === undefined ? 'Executions unknown' : `${observations.toLocaleString()}+ executions`, 'infra-client-count');
  recurrence.title = observations === undefined ? 'Execution count unavailable' : `At least ${count(observations, 'reported execution')} in the selected period`;
  summary.append(el('strong', title), el('span', platform(entry)), el('span', cpu(entry.sample.cpu?.effectiveMilliCPU)), recurrence, sampledAt(entry.lastObservedAt));
  details.append(summary);
  details.append(el('p', observations === undefined ? 'Execution count unavailable. Older inventory cannot establish repeated use.' : `At least ${count(observations, 'reported execution')} in the selected period.`, 'coverage-note infra-client-recurrence'));
  details.append(el('p', freshClient(entry, staleAfter, now) ? 'Latest reported resource sample. Current connectivity is unknown.' : 'The latest sample is outside the current freshness window or its time is unavailable. Current connectivity is unknown.', 'coverage-note'));
  details.append(el('p', entry.cacheActivity === 'observed' ? 'Cache activity linked to this installation.' : 'Cache activity is unconfirmed for this installation.', 'coverage-note infra-client-activity'));
  details.append(resources(entry.sample));
  return details;
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
  const now = Date.now();
  const staleAfter = validNumber(inventory.staleAfterSeconds) && inventory.staleAfterSeconds > 0 ? inventory.staleAfterSeconds : 86400;
  const repeated = installations.filter(entry => (executionCount(entry) ?? 0) >= 2 && freshClient(entry, staleAfter, now));
  const otherClients = installations.filter(entry => !repeated.includes(entry));
  const jobs = groupedRuns.flatMap(run => run.jobs);
  const summary = el('dl', '', 'infra-summary');
  metric(summary, 'GitHub Actions run attempts', groupedRuns.length.toLocaleString());
  metric(summary, 'Observed CI jobs', jobs.length.toLocaleString());
  metric(summary, 'Repeated clients', repeated.length.toLocaleString());
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
  clientSection.append(el('h2', 'Repeated clients'), el('p', 'Client installations reported in at least two distinct executions in the selected period, with a resource sample inside the current freshness window. Temporary CI jobs stay in GitHub Actions.', 'muted'));
  for (const entry of repeated.slice(0, maximumClients)) clientSection.append(renderClient(entry, staleAfter, now));
  if (!repeated.length) clientSection.append(el('p', 'No clients yet meet the repeated-use and freshness requirements.', 'infra-empty'));
  if (repeated.length > maximumClients) clientSection.append(el('p', `Showing the newest ${maximumClients} of ${count(repeated.length, 'repeated client')}.`, 'coverage-note'));
  clientSection.append(el('p', 'Counts are a lower bound from retained primary samples. Matching start and finish samples count once. Installations do not prove unique physical hosts.', 'coverage-note'));
  panel.append(clientSection);
  if (otherClients.length) {
    const other = el('details', '', 'card infra-other-clients');
    other.setAttribute('aria-label', 'Other client observations');
    other.append(el('summary', `Other client observations · ${otherClients.length.toLocaleString()}`), el('p', 'New clients, unknown execution counts and samples outside the freshness window stay here until repeated current use is established.', 'muted'));
    for (const entry of otherClients.slice(0, maximumClients)) other.append(renderClient(entry, staleAfter, now));
    if (otherClients.length > maximumClients) other.append(el('p', `Showing the newest ${maximumClients} of ${count(otherClients.length, 'other client observation')}.`, 'coverage-note'));
    panel.append(other);
  }
  const note = el('details', '', 'infra-coverage');
  note.append(el('summary', 'Inventory coverage'), el('p', `Observations are retained for ${inventory.retentionDays} days. Counts cover returned observations, not all project executions. Compute providers are shown with their evidence; orchestration alone does not identify the compute provider.`, 'coverage-note'), el('p', 'CPU and memory are reported limits, not measured utilization. Filesystem figures cover complete filesystems, not this project’s cached bytes. Storage is not added across jobs or clients.', 'coverage-note'));
  if (date(inventory.from) && date(inventory.to)) note.append(el('p', `${new Date(inventory.from).toISOString()} to ${new Date(inventory.to).toISOString()}`, 'coverage-note'));
  panel.append(note);
  return panel;
}
