export type Scope = 'personal' | 'team' | 'public' | 'unknown';
export type Estimate = {milliseconds: number | null; known: number; total: number; confidence: string};
export type Timing = {lookupMs: number; downloadMs: number; verificationMs: number; restoreMs: number; uploadMs: number};
export type Report = {
  eligible: number; hits: number; misses: number; runs: number; degraded: boolean;
  netEstimatedBuildTimeSaved: Estimate;
  bytes?: {downloaded: number; uploaded: number}; timing?: Timing;
  sources?: {source: string; hits: number; downloadedBytes: number; uploadedBytes: number}[] | null;
  integrations: {integration: string; hits: number; eligible: number}[] | null;
};
export type Cache = {
  state: string; reportState: string; pruneAllowed?: boolean;
  status?: {
    usageBytes: number; maxBytes: number | null; artifacts: number; evictionPolicy?: string;
    role?: string; visibility?: Scope; storageBackend?: string; entries?: number; metadataBytes?: number; canPrune?: boolean;
  };
  report?: Report;
};
export type CostReport = {
  state: 'unknown' | 'unavailable' | 'recorded' | 'estimate'; currency?: string;
  periodStart?: string; periodEnd?: string; complete: boolean;
  items?: {name: string; amountMinor: number}[] | null;
  knownSubtotalMinor: number; totalMinor: number | null;
};
type CacheOptions = {scope?: Scope; canPrune?: boolean; onPrune?: (targetBytes: number) => Promise<void>};

function el<K extends keyof HTMLElementTagNameMap>(tag: K, className = '', text = ''): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag); node.className = className; node.textContent = text; return node;
}
export function bytes(value: number): string {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  const index = value > 0 ? Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1) : 0;
  return `${(value / 1024 ** index).toFixed(index ? 1 : 0)} ${units[index]}`;
}
function duration(value: number | null): string {
  if (value === null) return 'Unknown';
  const sign = value < 0 ? '−' : '', seconds = Math.abs(value) / 1000;
  return sign + (seconds >= 3600 ? `${(seconds / 3600).toFixed(1)} h` : seconds >= 60 ? `${(seconds / 60).toFixed(1)} min` : `${seconds.toFixed(1)} s`);
}
function metric(label: string, value: string, detail: string): HTMLDivElement {
  const node = el('div'); node.append(el('div', 'label', label), el('div', 'value', value), el('div', 'detail', detail)); return node;
}
export function scopeName(scope: Scope): string {
  return {personal: 'Personal Cache', team: 'Team Cache', public: 'Public Cache', unknown: 'Private remote'}[scope];
}
export function renderScopes(): HTMLElement {
  const node = el('section', 'scope-explanation'); node.setAttribute('aria-label', 'Cache access and placement');
  node.append(el('h2', '', 'Who can reuse your work'));
  const scopes = el('div', 'scope-grid');
  for (const [name, detail] of [
    ['Personal Cache', 'Private to your account, across your machines and authorized CI.'],
    ['Team Cache', 'Shared with the engineers and CI authorized by your team.'],
    ['Public Cache', 'Readable by everyone. Only trusted Public Builds publish artifacts.'],
  ]) {
    const item = el('div'); item.append(el('h3', '', name), el('p', '', detail)); scopes.append(item);
  }
  node.append(scopes, el('p', 'coverage-note', 'Local and remote describe where a cache runs. They do not identify who can access it. Source activity below shows reported restores and transfers; stored bytes by access scope are unavailable.'));
  return node;
}
function sourceTable(report: Report, scope: Scope): HTMLElement {
  const section = el('section', 'source-usage'); section.append(el('h4', '', 'Reuse by source'));
  const wrapper = el('div', 'table-scroll'), table = el('table'); table.setAttribute('aria-label', 'Reported cache source activity');
  const head = el('thead'), headings = el('tr');
  for (const name of ['Source', 'Hits', 'Downloaded', 'Uploaded']) headings.append(el('th', '', name));
  for (const heading of Array.from(headings.children)) heading.setAttribute('scope', 'col');
  head.append(headings); table.append(head);
  const body = el('tbody');
  const names = {localCache: 'Local Cache', teamCache: scopeName(scope === 'personal' || scope === 'team' ? scope : 'unknown'), publicCache: 'Public Cache', unattributed: 'Unattributed'};
  const known = report.sources !== undefined && report.sources !== null;
  for (const [source, label] of Object.entries(names)) {
    const selected = report.sources?.filter(item => source === 'unattributed' ? !Object.hasOwn(names, item.source) || item.source === 'unattributed' : item.source === source) ?? [];
    const total = selected.reduce((sum, item) => ({hits: sum.hits + item.hits, downloaded: sum.downloaded + item.downloadedBytes, uploaded: sum.uploaded + item.uploadedBytes}), {hits: 0, downloaded: 0, uploaded: 0});
    const row = el('tr', 'source-row'); row.dataset.source = source;
    const heading = el('th', '', label); heading.scope = 'row'; row.append(heading);
    for (const value of [total.hits.toLocaleString(), bytes(total.downloaded), bytes(total.uploaded)]) row.append(el('td', '', known ? value : 'Unknown'));
    body.append(row);
  }
  table.append(body); wrapper.append(table); section.append(wrapper);
  section.append(el('p', 'coverage-note', scope === 'unknown' ? 'Private remote ownership is unknown. Local activity cannot be split into Personal, Team and Public storage from this report.' : 'Local source activity does not reveal its Personal, Team or Public storage composition. Unattributed outcomes have no known source.'));
  return section;
}

function pruneDialog(name: string, currentBytes: number, mutate: (targetBytes: number) => Promise<void>, trigger: HTMLButtonElement): void {
  const dialog = el('dialog', 'prune-dialog'); dialog.setAttribute('aria-label', `Prune ${name}`);
  const form = el('form'); form.noValidate = true;
  form.append(el('h2', '', `Prune ${name}`), el('p', '', 'Remove eligible cached artifacts until storage is at or below your target. Pinned artifacts, retained claims and active reads remain protected. The resulting size may exceed the target.'));
  const label = el('label', '', 'Target storage in bytes'), input = el('input');
  input.name = 'targetBytes'; input.type = 'number'; input.min = '0'; input.max = String(Number.MAX_SAFE_INTEGER); input.step = '1'; input.required = true;
  input.placeholder = `Current usage: ${currentBytes.toLocaleString()} bytes`; label.append(input); form.append(label);
  const confirmation = el('label', 'prune-confirm'), checkbox = el('input'); checkbox.type = 'checkbox'; checkbox.required = true;
  confirmation.append(checkbox, el('span', '', 'I confirm deletion of eligible cached artifacts.')); form.append(confirmation);
  const result = el('p', 'prune-result'); result.setAttribute('role', 'alert'); form.append(result);
  const controls = el('div', 'dialog-actions'), cancel = el('button', '', 'Cancel'), submit = el('button', 'danger', 'Prune cache');
  cancel.type = 'button'; submit.type = 'submit'; controls.append(cancel, submit); form.append(controls); dialog.append(form);
  let busy = false;
  const close = () => {if (!busy) dialog.close();};
  cancel.onclick = close; dialog.addEventListener('cancel', event => {if (busy) event.preventDefault();});
  dialog.addEventListener('close', () => {dialog.remove(); if (trigger.isConnected) trigger.focus();});
  form.onsubmit = async event => {
    event.preventDefault();
    if (busy) return;
    if (!/^\d+$/.test(input.value) || !Number.isSafeInteger(Number(input.value))) {result.textContent = 'Enter a whole number of bytes, zero or greater.'; input.focus(); return;}
    if (!checkbox.checked) {result.textContent = 'Confirm deletion before pruning.'; checkbox.focus(); return;}
    busy = true; cancel.disabled = true; submit.disabled = true; input.disabled = true; checkbox.disabled = true; result.textContent = 'Pruning cache and refreshing storage…';
    try {await mutate(Number(input.value)); busy = false; dialog.close();}
    catch (error) {result.textContent = `${error instanceof Error ? error.message : 'Pruning could not be confirmed.'} A snapshot refresh was requested. Check the snapshot before submitting another request.`;}
    finally {busy = false; cancel.disabled = false; submit.disabled = false; input.disabled = false; checkbox.disabled = false;}
  };
  document.body.append(dialog); dialog.showModal(); input.focus();
}

export function renderCache(name: string, cache: Cache, options: CacheOptions = {}): HTMLElement {
  const node = el('section', 'cache'), heading = el('div', 'cache-heading'); node.setAttribute('aria-label', name);
  const stateNames: Record<string, string> = {connected: '● Connected', unavailable: 'Unavailable', 'not-connected': 'Not connected', 'authentication-required': 'Sign-in required'};
  heading.append(el('h3', '', name), el('span', `state ${cache.state}`, stateNames[cache.state] ?? 'Unavailable')); node.append(heading);
  if (!cache.status) {
    const messages: Record<string, string> = {'not-connected': "Connect this project's remote cache to share artifacts across authorized machines and CI. Choose Connect a project for setup instructions.", 'authentication-required': "Run layercache login for this project's configuration, then refresh. Your account needs project access.", unavailable: name === 'Local Cache' ? "Start this project's daemon with layercache start, then refresh. Cache usage and reports are unavailable while it cannot be reached." : "The remote cache could not be reached. Check the project's connection, then refresh."};
    node.append(el('div', 'empty', messages[cache.state] ?? 'Cache information is unavailable.')); return node;
  }
  const {status, report} = cache, scope = options.scope ?? status.visibility ?? 'unknown';
  const description = name === 'Local Cache' ? 'Placement · Local machine' : `Access · ${scope === 'unknown' ? 'Private ownership unknown' : scopeName(scope)}`;
  node.append(el('p', 'cache-placement', `${description}${status.role ? ` · ${status.role}` : ''}${status.storageBackend ? ` · ${status.storageBackend}` : ''}`));
  const stats = el('div', 'stats');
  const storage = metric('Artifact storage', bytes(status.usageBytes), status.maxBytes === null ? 'Capacity unavailable' : `of ${bytes(status.maxBytes)} artifact capacity`);
  if (status.maxBytes !== null && status.maxBytes > 0) {const meter = el('progress', 'meter'); meter.max = status.maxBytes; meter.value = Math.min(status.usageBytes, status.maxBytes); meter.setAttribute('aria-label', `${name} artifact capacity used`); storage.append(meter);}
  stats.append(storage, metric('Effective cache hit rate', report && report.eligible > 0 ? `${Math.round(report.hits / report.eligible * 100)}%` : 'Unknown', report ? report.eligible > 0 ? `${report.hits.toLocaleString()} hits / ${report.eligible.toLocaleString()} eligible · ${report.misses.toLocaleString()} misses` : 'No eligible outcomes in this period' : cache.reportState === 'authentication-required' ? 'Sign in again to read reports' : 'Report unavailable'));
  node.append(stats);
  const metrics = el('div', 'metrics'), estimate = report?.netEstimatedBuildTimeSaved;
  metrics.append(metric('Stored artifacts', status.artifacts.toLocaleString(), `${status.evictionPolicy || 'Unknown'} eviction policy`), metric('Net estimated time saved', estimate && estimate.total > 0 ? duration(estimate.milliseconds) : 'Unknown', estimate && estimate.total > 0 ? `${estimate.known}/${estimate.total} known · ${estimate.confidence} confidence` : 'Timing evidence unavailable')); node.append(metrics);
  const storageDetail: string[] = [];
  if (status.entries !== undefined) storageDetail.push(`${status.entries.toLocaleString()} entries`);
  if (status.metadataBytes !== undefined) storageDetail.push(`${bytes(status.metadataBytes)} logical metadata`);
  if (storageDetail.length) node.append(el('p', 'coverage-note storage-detail', storageDetail.join(' · ')));
  node.append(el('p', 'coverage-note', 'Logical artifact usage excludes host disk, registry storage and unmanaged native caches.'));
  if (report) {
    const transfer = el('div', 'transfer-metrics');
    transfer.append(metric('Downloaded', report.bytes ? bytes(report.bytes.downloaded) : 'Unknown', 'Reported transfer bytes'), metric('Uploaded', report.bytes ? bytes(report.bytes.uploaded) : 'Unknown', 'Reported transfer bytes')); node.append(transfer);
    const phases = el('div', 'timing-phases'); phases.append(el('h4', '', 'Transfer and restore phases'));
    const list = el('dl');
    for (const [key, label] of [['lookupMs', 'Lookup'], ['downloadMs', 'Download'], ['verificationMs', 'Verify'], ['restoreMs', 'Restore'], ['uploadMs', 'Upload']] as const) list.append(el('dt', '', label), el('dd', '', report.timing ? duration(report.timing[key]) : 'Unknown'));
    phases.append(list, el('p', 'coverage-note', 'Phase durations are reported totals and can overlap. They are not wall-clock speedup.')); node.append(phases, sourceTable(report, scope));
  }
  const integrations = el('div', 'integrations');
  for (const item of report?.integrations ?? []) integrations.append(el('span', 'integration', `${item.integration} · ${item.hits}/${item.eligible} hits`));
  if (report?.degraded) integrations.append(el('span', 'integration', 'Partial or degraded evidence'));
  if (report) integrations.append(el('span', 'integration', `${report.runs} observed runs`));
  node.append(integrations, el('p', 'coverage-note integration-coverage', 'Reports cover reconciled Turbo, Actions and BuildKit outcomes. Native Gradle, Nx, Bazel and Xcode usage is not yet fully metered here.'));
  if ((options.canPrune ?? cache.pruneAllowed) === true && options.onPrune && scope !== 'public') {
    const controls = el('div', 'cache-maintenance'), button = el('button', '', `Prune ${name}`); button.type = 'button'; button.onclick = () => pruneDialog(name, status.usageBytes, options.onPrune!, button);
    controls.append(button, el('span', 'coverage-note', 'Only eligible artifacts can be removed.')); node.append(controls);
  }
  return node;
}

function money(amountMinor: number, currency?: string): string {
  if (!currency) return 'Unknown';
  try {const format = new Intl.NumberFormat(undefined, {style: 'currency', currency}); const digits = format.resolvedOptions().maximumFractionDigits; return `${format.format(amountMinor / 10 ** (digits ?? 2))} ${currency}`;}
  catch {return 'Unknown';}
}
export function renderCost(cost?: CostReport, title = 'Project cost'): HTMLElement {
  const node = el('section', 'cost-report'); node.setAttribute('aria-label', title); node.append(el('h3', '', title));
  const available = cost?.state === 'recorded' || cost?.state === 'estimate';
  if (!cost || !available) {
    node.append(el('div', 'value cost-total', 'Unknown'), el('p', 'coverage-note cost-state', cost?.state === 'unavailable' ? 'Cost ledger unavailable. Refresh to retry.' : 'No cost ledger configured. Current stored bytes do not establish your total cost.'));
    return node;
  }
  node.append(el('p', 'cost-state', cost.state === 'estimate' ? 'Operator estimate' : 'Operator-recorded costs'));
  const period = cost.periodStart && cost.periodEnd ? `${new Date(cost.periodStart).toLocaleString(undefined, {timeZone: 'UTC'})} to ${new Date(cost.periodEnd).toLocaleString(undefined, {timeZone: 'UTC'})} UTC` : 'Period unavailable';
  node.append(el('p', 'coverage-note cost-period', period));
  node.append(metric('Period total', cost.complete && cost.totalMinor !== null ? money(cost.totalMinor, cost.currency) : 'Unknown', cost.complete ? 'Complete configured ledger coverage' : 'Incomplete coverage. Unrecorded costs may exist.'));
  node.querySelector('.value')!.classList.add('cost-total');
  if (!cost.complete) node.append(metric('Known subtotal', money(cost.knownSubtotalMinor, cost.currency), 'Only the configured line items'));
  if (cost.items?.length) {
    const list = el('dl', 'cost-items');
    for (const item of cost.items) list.append(el('dt', '', item.name), el('dd', '', money(item.amountMinor, cost.currency)));
    node.append(list);
  }
  node.append(el('p', 'coverage-note', 'Operator records and estimates are not provider-verified invoices. Estimated time saved is not a money-saved measurement.'));
  return node;
}
