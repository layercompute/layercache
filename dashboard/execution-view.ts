import {bytes} from './cache-view.js';

export type ResourceSample = {
  os?: string | null; arch?: string | null;
  cpu?: {visibleLogical: number | null; affinityLogical: number | null; quotaMilliCPU: number | null; effectiveMilliCPU: number | null; limitKnowledge?: string} | null;
  memory?: {visibleTotalBytes: number | null; cgroupLimitBytes: number | null; effectiveLimitBytes: number | null; limitKnowledge?: string} | null;
  filesystems?: {id: string; roles: string[] | null; capacityBytes: number | null; freeBytes: number | null; availableBytes: number | null}[] | null;
};
export type ExecutionEntry = {
  id: string; ownerId: string; installationId?: string; executionId: string; observedExecutions?: number;
  kind: 'client' | 'githubActions'; provider: 'github' | 'ubicloud' | 'selfHosted' | 'unknown';
  providerEvidence: 'signed' | 'declared' | 'unknown'; runnerEnvironment: 'github-hosted' | 'self-hosted' | 'unknown';
  lastObservedAt: string; phase: 'start' | 'finish'; sample: ResourceSample; cacheActivity: 'unconfirmed' | 'observed';
};
export type ExecutionInventory = {
  version: 1; from: string; to: string; updatedAt: string; retentionDays: number;
  staleAfterSeconds: number; historyTruncated: boolean; listTruncated?: boolean; jobCount?: number; installations: ExecutionEntry[]; jobs: ExecutionEntry[];
};
export type ExecutionProject = {id: string; name?: string; repository?: string; executions?: ExecutionInventory | null};
type Observed = {entry: ExecutionEntry; projects: Map<string, string>; staleAfter: number; volumeRoles: Map<string, Set<string>>; cacheObserved: boolean};
const maximumRows = 50;
const filesystemRoles: Record<string, string> = {workspace: 'Workspace', localCache: 'Local cache', staging: 'Temporary data'};
function el<K extends keyof HTMLElementTagNameMap>(tag: K, text = '', className = ''): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag); node.textContent = text; node.className = className; return node;
}
function number(value: unknown): value is number {return typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= Number.MAX_SAFE_INTEGER;}
function date(value: string): number {return Date.parse(value) || 0;}
function time(value: string): string {return date(value) ? `${new Date(value).toISOString().replace('T', ' ').replace('.000Z', ' UTC')}` : 'Unknown';}
function metric(list: HTMLDListElement, name: string, value: string): void {const item = el('div'); item.append(el('dt', name), el('dd', value)); list.append(item);}
function amount(value: unknown): string {return number(value) ? bytes(value) : 'Unknown';}
function cores(value: unknown): string {return number(value) ? `${(value / 1000).toLocaleString(undefined, {maximumFractionDigits: 3})} vCPU` : 'Unknown';}
function count(value: unknown): string {return number(value) ? value.toLocaleString() : 'Unknown';}
function short(value: string): string {return value.length > 20 ? `${value.slice(0, 12)}…${value.slice(-6)}` : value;}
function projectLabel(project: ExecutionProject): string {const identity = project.repository ?? project.id; return project.name && project.name !== identity ? `${project.name} (${identity})` : identity;}
function collectRoles(observed: Observed, entry: ExecutionEntry): void {
  for (const volume of (entry.sample.filesystems ?? []).slice(0, 64)) {
    const roles = observed.volumeRoles.get(volume.id) ?? new Set<string>();
    for (const role of volume.roles ?? []) roles.add(role);
    observed.volumeRoles.set(volume.id, roles);
  }
}
function withPercent(value: number | null | undefined, total: number | null | undefined): string {
  if (!number(value)) return 'Unknown';
  return `${bytes(value)}${number(total) && total > 0 ? ` (${(value / total * 100).toLocaleString(undefined, {maximumFractionDigits: 1})}%)` : ''}`;
}
function volumes(sample: ResourceSample): HTMLElement {
  const section = el('section', '', 'execution-storage'); section.append(el('h4', 'Filesystem capacity'));
  const unique = new Map<string, NonNullable<ResourceSample['filesystems']>[number]>();
  for (const volume of (sample.filesystems ?? []).slice(0, 64)) {
    const previous = unique.get(volume.id);
    if (previous) previous.roles = Array.from(new Set([...(previous.roles ?? []), ...(volume.roles ?? [])]));
    else unique.set(volume.id, {...volume, roles: [...(volume.roles ?? [])]});
  }
  if (!unique.size) section.append(el('p', 'Unknown. No filesystem sample was reported.', 'coverage-note'));
  for (const volume of unique.values()) {
    const card = el('div', '', 'execution-volume');
    card.append(el('p', volume.roles?.length ? volume.roles.map(role => filesystemRoles[role] ?? 'Other data').join(' · ') : 'Observed filesystem', 'execution-volume-name'));
    const list = el('dl', '', 'execution-resources');
    const valid = number(volume.capacityBytes) && number(volume.freeBytes) && volume.freeBytes <= volume.capacityBytes;
    const used = valid ? volume.capacityBytes! - volume.freeBytes! : null;
    const reserved = number(volume.freeBytes) && number(volume.availableBytes) && volume.availableBytes <= volume.freeBytes ? volume.freeBytes - volume.availableBytes : null;
    metric(list, 'Capacity', amount(volume.capacityBytes));
    metric(list, 'Used', withPercent(used, volume.capacityBytes));
    metric(list, 'Free', withPercent(volume.freeBytes, volume.capacityBytes));
    metric(list, 'Available', withPercent(volume.availableBytes, volume.capacityBytes));
    metric(list, 'Reserved', withPercent(reserved, volume.capacityBytes));
    card.append(list); section.append(card);
  }
  return section;
}
function renderEntry(observed: Observed, now: number, client: boolean, openResources: boolean): HTMLElement {
  const {entry, projects} = observed;
  const card = el('article', '', client ? 'execution-installation' : 'execution-job'); card.dataset.executionId = entry.id;
  const heading = el('div', '', 'execution-heading');
  const platform = [entry.sample.os, entry.sample.arch].filter(Boolean).join(' · ');
  heading.append(el('h4', `${client ? 'Client installation' : 'GitHub Actions job'}${platform ? ` · ${platform}` : ''}`));
  const stale = date(entry.lastObservedAt) > 0 && now - date(entry.lastObservedAt) > observed.staleAfter * 1000;
  heading.append(el('span', stale ? 'Sample older than 24 hours' : date(entry.lastObservedAt) ? 'Recorded sample' : 'Sample time unknown', stale ? 'execution-stale' : 'execution-status'));
  card.append(heading, el('p', `${client ? 'Installation' : 'Execution'} ${short(client ? entry.installationId ?? entry.id : entry.executionId || entry.id)}`, 'execution-identity'));
  const jobIdentity = !client && /^github-actions:(\d+):(\d+):(\d+):(\d+)$/.exec(entry.executionId);
  if (jobIdentity) card.append(el('p', `Repository ID ${jobIdentity[1]} · run ${jobIdentity[2]} · attempt ${jobIdentity[3]} · job ${jobIdentity[4]}`, 'execution-identity'));
  card.append(el('p', `${projects.size} ${projects.size === 1 ? 'project' : 'projects'} · ${Array.from(projects.values()).sort().join(', ')}`, 'execution-projects'));
  if (!client) {
    const names = {github: 'GitHub', ubicloud: 'Ubicloud', selfHosted: 'Self-hosted', unknown: 'Unknown'};
    const evidence = entry.providerEvidence === 'declared' ? 'declared' : entry.providerEvidence === 'signed' ? 'signed' : 'unconfirmed';
    card.append(el('p', `Orchestrator · GitHub Actions. Compute provider · ${names[entry.provider] ?? 'Unknown'}${entry.provider !== 'unknown' ? ` (${evidence})` : ''}.`, 'execution-provider'));
    card.append(el('p', `Runner classification · ${entry.runnerEnvironment ?? 'unknown'}`, 'coverage-note'));
  }
  card.append(el('p', observed.cacheObserved ? 'Cache activity observed' : 'Setup observed; cache activity not yet linked.', 'coverage-note execution-cache-activity'));
  const resources = el('dl', '', 'execution-resources');
  metric(resources, 'Visible logical CPUs', count(entry.sample.cpu?.visibleLogical));
  metric(resources, 'CPU affinity', count(entry.sample.cpu?.affinityLogical));
  metric(resources, 'CPU quota', cores(entry.sample.cpu?.quotaMilliCPU));
  metric(resources, 'Effective CPU limit', cores(entry.sample.cpu?.effectiveMilliCPU));
  metric(resources, 'Visible memory', amount(entry.sample.memory?.visibleTotalBytes));
  metric(resources, 'Container memory limit', amount(entry.sample.memory?.cgroupLimitBytes));
  metric(resources, 'Effective memory limit', amount(entry.sample.memory?.effectiveLimitBytes));
  const details = el('details', '', 'execution-details'); details.open = openResources;
  const latestSample = {...entry.sample, filesystems: entry.sample.filesystems?.map(volume => ({...volume, roles: Array.from(observed.volumeRoles.get(volume.id) ?? volume.roles ?? [])}))};
  details.append(el('summary', 'Resources and filesystem capacity'), resources, volumes(latestSample));
  card.append(details, el('p', `Observed ${time(entry.lastObservedAt)} · ${entry.phase === 'finish' ? 'finish' : 'start'} sample`, 'coverage-note execution-sampled'));
  return card;
}

// Aggregate only inventories obtained through the caller's authorized project
// list. Installation identifiers are scoped by owner; volume IDs are scoped
// within that installation. Signed job IDs are normalized by the server.
export function renderExecutions(projects: ExecutionProject[], now = Date.now()): HTMLElement {
  const section = el('section', '', 'execution-inventory'); section.setAttribute('aria-label', 'Where used');
  section.append(el('h2', 'Where used'), el('p', 'Observed client installations and CI jobs across these projects. An installation identifies one OS user installation, not physical hardware.', 'coverage-note'));
  const installations = new Map<string, Observed>(), jobs = new Map<string, Observed>();
  let supported = 0, missing = 0, truncated = false;
  const inventories: ExecutionInventory[] = [];
  for (const project of projects.slice(0, 32)) {
    const inventory = project.executions;
    if (!inventory || inventory.version !== 1 || !Array.isArray(inventory.installations) || !Array.isArray(inventory.jobs)) {missing++; continue;}
    supported++; inventories.push(inventory); truncated ||= inventory.historyTruncated || inventory.listTruncated === true;
    for (const [entries, target, client] of [[inventory.installations, installations, true], [inventory.jobs, jobs, false]] as const) {
      if (entries.length > 200) truncated = true;
      for (const entry of entries.slice(0, 4096).sort((a, b) => date(b.lastObservedAt) - date(a.lastObservedAt)).slice(0, 200)) {
        const key = client ? `${entry.ownerId}\0${entry.installationId ?? entry.id}` : entry.id;
        const previous = target.get(key);
        if (previous) {
          collectRoles(previous, entry);
          previous.cacheObserved ||= entry.cacheActivity === 'observed';
          previous.projects.set(project.id, projectLabel(project));
          if (date(entry.lastObservedAt) > date(previous.entry.lastObservedAt)) {previous.entry = entry; previous.staleAfter = inventory.staleAfterSeconds || 86400;}
        } else {
          const observed = {entry, projects: new Map([[project.id, projectLabel(project)]]), staleAfter: inventory.staleAfterSeconds || 86400, volumeRoles: new Map<string, Set<string>>(), cacheObserved: entry.cacheActivity === 'observed'};
          collectRoles(observed, entry); target.set(key, observed);
        }
      }
    }
  }
  if (!supported) {section.append(el('p', 'Execution inventory unavailable. Older clients or servers may not report it; installation and job counts are unknown.', 'execution-empty')); return section;}
  section.append(el('p', `${installations.size} observed client ${installations.size === 1 ? 'installation' : 'installations'} · ${jobs.size} observed CI ${jobs.size === 1 ? 'job' : 'jobs'}`, 'execution-counts'));
  const from = inventories.map(item => date(item.from)).filter(Boolean), to = inventories.map(item => date(item.to)).filter(Boolean);
  section.append(el('p', `Inventory covers observations${from.length && to.length ? ` from ${time(new Date(Math.min(...from)).toISOString())} to ${time(new Date(Math.max(...to)).toISOString())}` : ''}. The 30-day inventory window does not give lifetime counts. Samples older than 24 hours are marked stale, without an offline inference.`, 'coverage-note'));
  if (missing || truncated || projects.length > 32) section.append(el('p', `Partial inventory.${missing ? ` ${missing} project ${missing === 1 ? 'inventory is' : 'inventories are'} unavailable.` : ''}${truncated ? ' Stored history or returned rows are limited.' : ''}${projects.length > 32 ? ' Only the first 32 authorized projects are included.' : ''} Counts describe the observations returned.`, 'coverage-note execution-partial'));
  for (const [title, entries, client] of [['Client installations', installations, true], ['CI jobs', jobs, false]] as const) {
    const group = el('section', '', 'execution-group'); group.setAttribute('aria-label', title); group.append(el('h3', title));
    const ordered = Array.from(entries.values()).sort((a, b) => date(b.entry.lastObservedAt) - date(a.entry.lastObservedAt) || a.entry.id.localeCompare(b.entry.id));
    if (!ordered.length) group.append(el('p', 'No observations returned in this reporting period. This does not establish that no other installations or jobs exist.', 'execution-empty'));
    ordered.slice(0, maximumRows).forEach((observed, index) => group.append(renderEntry(observed, now, client, client && index === 0)));
    if (ordered.length > maximumRows) group.append(el('p', `Showing the newest ${maximumRows} of ${ordered.length} observed ${client ? 'installations' : 'jobs'}.`, 'coverage-note'));
    section.append(group);
  }
  section.append(el('p', 'GitHub Actions identifies orchestration; declared providers come from workflow configuration. Observed cache activity does not measure task coverage or savings.', 'coverage-note'));
  section.append(el('p', 'Storage describes whole filesystems, not project cache usage. Used is capacity minus free; reserved is free minus available. Capacity is not added across projects.', 'coverage-note'));
  return section;
}
