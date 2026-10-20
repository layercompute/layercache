import {bytes, renderCache, renderCost, scopeName, type Estimate} from '../dashboard/cache-view.js';
import {formatDuration, integrationLabel, projectInsights} from '../dashboard/insights.js';
import {renderInfrastructure} from './infrastructure.js';
import {el, empty, panel, table} from './ui.js';
import type {API, Project, ProjectPage, Session, Snapshot, Team} from './types.js';

type Actions = {api: API; reload: () => Promise<void>; navigate: (page: ProjectPage) => void; message: (text: string, error?: boolean) => void};
function formatNumber(value: number | null): string {return value === null ? 'Unknown' : value.toLocaleString();}
function percent(value: number | null): string {return value === null ? 'Not measured' : `${Math.round(value * 100)}%`;}
function estimateDetail(value: Estimate | null): string {
  return value ? `${value.known.toLocaleString()} of ${value.total.toLocaleString()} outcomes timed · ${value.confidence} confidence` : 'Original execution timing unavailable';
}
function stat(id: string, label: string, value: string, detail: string): HTMLElement {
  const node = el('div', '', 'stat-card'); node.dataset.metric = id;
  node.append(el('p', label, 'stat-label'), el('div', value, 'stat-value'), el('p', detail, 'stat-detail'));
  return node;
}
function pageLink(text: string, target: ProjectPage, actions: Actions): HTMLElement {
  const button = el('button', text, 'text-link'); button.type = 'button'; button.onclick = () => actions.navigate(target); return button;
}

function integrations(snapshot: Snapshot, compact = false): HTMLElement {
  const insights = projectInsights(snapshot.cache);
  const section = panel('Cache by type', 'Reported reuse in the selected period');
  const {wrapper, body} = table('Cache by type', ['Type', 'Hit rate', 'Hits / eligible', 'Avoided task time']);
  for (const item of insights.integrations) {
    if (compact && item.state !== 'reported') continue;
    const row = el('tr'); row.dataset.integration = item.id;
    const title = el('th'); title.scope = 'row';
    const name = el('div', '', 'integration-name');
    const dot = el('span', '', `integration-dot integration-${item.id}`); dot.setAttribute('aria-hidden', 'true');
    name.append(dot, el('span', item.name)); title.append(name);
    const rate = el('td');
    if (item.state === 'reported') {
      rate.append(el('span', percent(item.hitRate), 'rate-value'));
      const meter = el('div', '', 'bar-track'), fill = el('div', '', 'bar-fill');
      // Native progress avoids inline styles under the dashboard's strict CSP.
      const progress = el('progress'); progress.max = 1; progress.value = item.hitRate ?? 0;
      progress.setAttribute('aria-label', `${item.name} hit rate`); fill.append(progress); meter.append(fill); rate.append(meter);
    } else rate.append(el('span', 'Not yet metered', 'coverage-pill'));
    const counts = item.state === 'reported' ? `${formatNumber(item.hits)} / ${formatNumber(item.eligible)}` : 'Not measured';
    const count = el('td', counts); count.title = item.units;
    const time = el('td', formatDuration(item.avoidedTaskTime?.milliseconds ?? null));
    if (item.avoidedTaskTime) time.title = estimateDetail(item.avoidedTaskTime);
    row.append(title, rate, count, time); body.append(row);
  }
  if (body.childElementCount) section.append(wrapper);
  else section.append(empty('No cache outcomes reported', 'Reported native outcomes will appear here. Unmetered activity remains unknown.'));
  if (compact) {
    const missing = insights.integrations.filter(item => item.state !== 'reported');
    if (missing.length) {
      const coverage = el('details', '', 'integration-coverage');
      coverage.append(el('summary', `${missing.length} cache types not yet metered`));
      const types = el('div', '', 'coverage-types');
      for (const item of missing) types.append(el('span', `${item.name} · Not yet metered`, 'coverage-pill'));
      coverage.append(types); section.append(coverage);
    }
  }
  section.append(el('p', 'Hit rates cover reported native work units. Unmetered usage stays unknown.', 'coverage-note'));
  return section;
}

function commonHits(snapshot: Snapshot): HTMLElement {
  const insights = projectInsights(snapshot.cache), section = panel('Most reused artifacts', 'Which compatible artifacts are being restored most often');
  if (!insights.topArtifacts.length) {
    section.append(empty('No artifact-level hits reported', 'Artifacts will appear here when the native integration reports a matching cache hit.'));
    return section;
  }
  const {wrapper, body} = table('Most reused artifacts', ['Artifact', 'Type', 'Hits', 'Downloaded']);
  for (const item of insights.topArtifacts.slice(0, 8)) {
    const row = el('tr');
    const heading = el('th', `Artifact ${item.ref.slice(0, 10)}`, 'key-ref'); heading.scope = 'row'; heading.title = item.ref;
    row.append(heading, el('td', integrationLabel(item.integration)), el('td', item.hits.toLocaleString()), el('td', bytes(item.downloadedBytes)));
    body.append(row);
  }
  section.append(wrapper, el('p', `Artifact references keep cache keys and task paths private. Hit units follow each native integration.${insights.topArtifactsTruncated ? ' Only the most frequently hit artifacts are included.' : ''}`, 'coverage-note'));
  return section;
}

function storedTypes(snapshot: Snapshot): HTMLElement {
  const section = panel('Stored data by type', 'Deduplicated artifact bytes, grouped by their storage protocol');
  const rows = snapshot.cache.status?.storageByIntegration;
  if (!rows) {section.append(empty('Storage breakdown unavailable', 'This backend has not reported an artifact breakdown.')); return section;}
  if (!rows.length) {section.append(empty('No stored artifacts', 'Published artifacts will appear here.')); return section;}
  const labels: Record<string, string> = {turbo: 'Turbo-protocol archives', actions: 'GitHub Actions archives', gradle: 'Gradle', nx: 'Nx', retained: 'Retained artifacts', shared: 'Shared across integrations', other: 'Other artifacts'};
  const {wrapper, body} = table('Stored data by type', ['Type', 'Cached bytes', 'Artifacts', 'References']);
  for (const item of [...rows].sort((a, b) => b.usageBytes - a.usageBytes)) {
    const row = el('tr'); row.dataset.integration = item.integration;
    const name = el('th', labels[item.integration] ?? integrationLabel(item.integration)); name.scope = 'row';
    row.append(name, el('td', bytes(item.usageBytes)), el('td', item.artifacts.toLocaleString()), el('td', item.entries.toLocaleString())); body.append(row);
  }
  section.append(wrapper, el('p', 'Each blob is counted once. Bytes referenced by multiple integrations appear under Shared. Xcode and Expo snapshots currently share the Turbo archive transport. Separate Docker registry and Bazel engine stores are excluded.', 'coverage-note'));
  return section;
}

function savings(snapshot: Snapshot, compact = false): HTMLElement {
  const {savings} = projectInsights(snapshot.cache), section = panel('Savings evidence', 'Timing-backed estimates, with coverage shown');
  const list = el('dl', '', 'savings-breakdown');
  const rows = [
    ['Avoided task time', formatDuration(savings.avoidedTaskTime?.milliseconds ?? null), estimateDetail(savings.avoidedTaskTime)],
    ['Estimated build time saved', formatDuration(savings.estimatedBuildTime?.milliseconds ?? null), estimateDetail(savings.estimatedBuildTime)],
    ['CPU-hours saved', 'Not measured', 'Needs the original task duration and its allocated CPU'],
    ['Compute cost saved', 'Not measured', 'Needs matched resource usage and provider prices'],
  ];
  for (const [label, value, detail] of compact ? rows.slice(0, 2) : rows) {
    const row = el('div', '', 'savings-row');
    const title = el('div'); title.append(el('dt', label), el('p', detail, 'muted')); row.append(title, el('dd', value)); list.append(row);
  }
  section.append(list);
  if (compact) section.append(el('p', 'CPU-hours saved · Not measured. Compute cost saved · Not measured.', 'coverage-note'));
  section.append(el('p', savings.explanation, 'coverage-note'));
  return section;
}

function activity(snapshot: Snapshot): HTMLElement {
  const {activity} = projectInsights(snapshot.cache), section = panel('Cache activity', 'Hits and misses over the selected period');
  if (!activity.length) {section.append(empty('No time series available', 'Activity appears as clients report cache outcomes.')); return section;}
  const chart = el('div', '', 'mini-trend'); chart.setAttribute('aria-label', 'Cache lookup history');
  const maximum = Math.max(1, ...activity.map(item => item.eligible));
  for (const item of activity) {
    const column = el('div', '', 'trend-column');
    const label = `${new Date(item.from).toLocaleString()}: ${item.hits} hits, ${item.misses} misses`;
    column.title = label;
    const hit = el('progress', '', 'trend-hits'); hit.max = maximum; hit.value = item.hits; hit.setAttribute('aria-label', label);
    const total = el('progress', '', 'trend-total'); total.max = maximum; total.value = item.eligible; total.setAttribute('aria-label', `${item.eligible} eligible outcomes`);
    column.append(total, hit); chart.append(column);
  }
  const labels = el('div', '', 'trend-label');
  labels.append(el('span', new Date(activity[0]!.from).toLocaleDateString(undefined, {month: 'short', day: 'numeric'})), el('span', 'Hits · All lookups'), el('span', new Date(activity.at(-1)!.to).toLocaleDateString(undefined, {month: 'short', day: 'numeric'})));
  section.append(chart, labels); return section;
}

function overview(snapshot: Snapshot, actions: Actions): HTMLElement {
  const node = el('section', '', 'overview'); node.setAttribute('aria-label', 'Project overview');
  const insights = projectInsights(snapshot.cache), stats = el('div', '', 'summary-grid');
  stats.append(
    stat('storage', 'Cached data', insights.storageUsedBytes === null ? 'Unknown' : bytes(insights.storageUsedBytes), insights.storageLimitBytes === null ? 'Capacity unavailable' : `${bytes(insights.storageLimitBytes)} capacity · ${formatNumber(insights.artifacts)} artifacts`),
    stat('hit-rate', 'Effective hit rate', percent(insights.hitRate), insights.hits === null ? 'No cache report available' : `${formatNumber(insights.hits)} hits / ${formatNumber(insights.eligible)} eligible`),
    stat('time-saved', 'Est. build time saved', formatDuration(insights.savings.estimatedBuildTime?.milliseconds ?? null), estimateDetail(insights.savings.estimatedBuildTime)),
    stat('runs', 'Reported runs', formatNumber(insights.runs), `${formatNumber(insights.misses)} cache misses in this period`),
  );
  node.append(stats);
  if (insights.partial) node.append(el('p', 'Some outcomes have partial or degraded evidence. Totals cover only the reported work.', 'callout'));
  const grid = el('div', '', 'overview-grid'); grid.append(integrations(snapshot, true), savings(snapshot, true)); node.append(grid);
  const lower = el('div', '', 'overview-grid'); lower.append(activity(snapshot), commonHits(snapshot)); node.append(lower);
  const footer = el('div', '', 'overview-footer'); footer.append(pageLink('Explore activity', 'activity', actions), pageLink('Manage storage', 'storage', actions)); node.append(footer);
  return node;
}

type Capacity = {
  state: 'connected' | 'unsupported'; maxBytes: number | null; usedBytes: number | null;
  reservedBytes: number | null; retainedBytes: number | null; retainedStagingBytes: number | null;
  softTargetBytes: number | null; minimumBytes: number | null; ceilingBytes: number | null;
  canChange: boolean; limitSource: string; reason?: string;
};
const GiB = 1024 ** 3;
function capacityEditor(project: Project, actions: Actions): HTMLElement {
  const section = panel('Cache capacity', 'Change the project limit within its assigned storage allocation'); section.classList.add('capacity-card');
  const content = el('div'); content.append(el('p', 'Loading capacity…', 'muted')); section.append(content);
  let capacity: Capacity | undefined;
  const read = async () => {capacity = await actions.api<Capacity>(`/api/projects/${encodeURIComponent(project.id)}/capacity`);};
  const render = () => {
    if (!capacity) return;
    content.replaceChildren();
    if (capacity.state !== 'connected' || capacity.maxBytes === null) {content.append(empty('Capacity management unavailable', capacity.reason ?? 'This storage backend does not support capacity changes.')); return;}
    const summary = el('div', '', 'capacity-summary');
    summary.append(stat('capacity-current', 'Current cache limit', bytes(capacity.maxBytes), capacity.ceilingBytes === null ? 'Assigned allocation unavailable' : `${bytes(capacity.ceilingBytes)} assigned allocation`));
    content.append(summary);
    const limits = el('dl', '', 'savings-breakdown');
    for (const [label, value] of [['Stored bytes', capacity.usedBytes], ['Retained reservations', capacity.reservedBytes], ['Retained artifacts', capacity.retainedBytes], ['Soft cleanup target', capacity.softTargetBytes]] as const) {
      const row = el('div', '', 'savings-row'); row.append(el('dt', label), el('dd', value === null ? 'Unknown' : bytes(value))); limits.append(row);
    }
    content.append(limits);
    if (!capacity.canChange) {content.append(el('p', capacity.reason ?? 'An administrator can adjust this limit.', 'limit-note')); return;}
    const form = el('form', '', 'capacity-form'); form.noValidate = true;
    const label = el('label', 'Cache limit in GiB'), input = el('input');
    input.name = 'capacityGiB'; input.type = 'number'; input.step = 'any'; input.required = true;
    input.min = String((capacity.minimumBytes ?? 1) / GiB); if (capacity.ceilingBytes !== null) input.max = String(capacity.ceilingBytes / GiB);
    input.value = String(capacity.maxBytes / GiB); label.append(input); form.append(label);
    form.append(el('p', `Minimum ${bytes(capacity.minimumBytes ?? 1)}. Raising the assigned allocation requires your operator. Changing this limit does not delete cached artifacts.`, 'limit-note'));
    const result = el('p', '', 'capacity-result'); result.setAttribute('role', 'status');
    const submit = el('button', 'Save cache limit', 'primary'); submit.type = 'submit'; form.append(submit, result); content.append(form);
    let reconciliationRequired = false;
    form.onsubmit = async event => {
      event.preventDefault(); if (submit.disabled || !capacity) return;
      const entered = Number(input.value), amount = Math.round(entered * GiB);
      if (!input.value.trim() || !Number.isFinite(entered) || !Number.isSafeInteger(amount) || amount < (capacity.minimumBytes ?? 1) || capacity.ceilingBytes !== null && amount > capacity.ceilingBytes) {
        result.textContent = 'Choose a limit between the minimum and your assigned allocation.'; input.focus(); return;
      }
      submit.disabled = true; input.disabled = true;
      try {
        capacity = await actions.api<Capacity>(`/api/projects/${encodeURIComponent(project.id)}/capacity`, 'PUT', {maxBytes: amount, expectedMaxBytes: capacity.maxBytes, confirm: true});
        actions.message('Cache limit updated.'); await actions.reload();
      } catch (error) {
        reconciliationRequired = true;
        result.textContent = `${error instanceof Error ? error.message : 'Capacity could not be confirmed.'} Reload capacity before trying again.`;
        const retry = el('button', 'Reload capacity'); retry.type = 'button'; retry.onclick = () => {void read().then(() => {if (section.isConnected) render();}).catch(error => {result.textContent = error instanceof Error ? error.message : 'Capacity unavailable';});};
        result.append(retry);
      } finally {submit.disabled = reconciliationRequired; input.disabled = reconciliationRequired;}
    };
  };
  void read().then(() => {if (section.isConnected) render();}).catch(error => {if (section.isConnected) content.replaceChildren(el('p', error instanceof Error ? error.message : 'Capacity unavailable', 'error'));});
  return section;
}

export function renderProjectPage(page: ProjectPage, project: Project, team: Team, snapshot: Snapshot, actions: Actions): HTMLElement {
  if (page === 'overview') return overview(snapshot, actions);
  if (page === 'infrastructure') return renderInfrastructure({...project, executions: snapshot.executions});
  const node = el('div');
  const scope = snapshot.scope ?? snapshot.cache.status?.visibility ?? team.kind;
  const report = snapshot.cache.report;
  if (page === 'activity') {
    node.append(activity(snapshot));
    const grid = el('div', '', 'overview-grid'); grid.append(integrations(snapshot), commonHits(snapshot)); node.append(grid);
    const details = panel('Transfer and source details', 'Reported cache activity, excluding unmetered native work');
    details.append(renderCache(scopeName(scope), snapshot.cache, {scope, canPrune: false})); node.append(details);
    node.append(savings(snapshot));
    if (report?.eligible === 0) node.append(el('p', 'No measured lookups', 'coverage-note'));
    return node;
  }
  if (page === 'storage') {
    node.append(storedTypes(snapshot));
    const grid = el('div', '', 'storage-grid'); grid.append(capacityEditor(project, actions));
    const details = panel('Stored artifacts', 'Project storage and explicit maintenance');
    const prune = async (targetBytes: number) => {try {await actions.api(`/api/projects/${encodeURIComponent(project.id)}/prune`, 'POST', {targetBytes, confirm: true});} finally {await actions.reload();}};
    details.append(renderCache(scopeName(scope), snapshot.cache, {scope, canPrune: snapshot.cache.status?.canPrune === true, onPrune: prune})); grid.append(details); node.append(grid);
    const cost = panel('Storage and service cost'); cost.append(renderCost(snapshot.cost), el('p', 'Project and account costs may overlap. Local and remote reports must not be added into a global savings total.', 'coverage-note')); node.append(cost);
    return node;
  }
  return node;
}

export function renderConnection(project: Project, session: Session, githubLoginAvailable: boolean, actions: Pick<Actions, 'message'>): HTMLElement {
  const section = panel('Connect your CLI', 'Use this project from your machines and authorized CI');
  const quote = (value: string) => `'${value.replaceAll("'", "'\\''")}'`;
  const command = `layercache connect --cloud ${quote(session.cloudOrigin)} --project ${quote(project.id)} --github-cli${project.operatorManaged ? ' --remote-only' : ''}`;
  section.append(el('pre', command));
  const copy = el('button', 'Copy command', 'primary'); copy.type = 'button';
  copy.onclick = () => {void navigator.clipboard.writeText(command).then(() => actions.message('Connection command copied.')).catch(() => actions.message('Copy the command from the box above.', true));};
  section.append(copy, el('p', githubLoginAvailable ? 'Uses your GitHub CLI sign-in. Omit --github-cli to sign in through a browser code.' : 'Uses existing GitHub CLI sign-in. Run gh auth login first. Your dashboard password does not replace GitHub authentication.', 'muted'));
  if (project.operatorManaged) section.append(el('p', 'This connection uses the hosted backend directly and creates no Local Cache store.', 'muted'));
  return section;
}
