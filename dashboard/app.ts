import {renderCache, renderCost, renderScopes, scopeName, type Cache, type CostReport} from './cache-view.js';
type Snapshot = {projects: {id: string; local: Cache; team: Cache; cost?: CostReport}[]; updatedAt: string};

function consumeSessionFragment(): string {
  const value = location.hash.slice(1);
  if (!/^[a-f0-9]{64}$/.test(value)) return "";
  history.replaceState(null, "", location.pathname);
  return value;
}
let token = consumeSessionFragment();
document.querySelector('#cache-scopes')!.replaceWith(renderScopes());
const projects = document.querySelector<HTMLDivElement>("#projects")!;
const notice = document.querySelector<HTMLParagraphElement>("#notice")!;
const period = document.querySelector<HTMLSelectElement>("#period")!;
const refresh = document.querySelector<HTMLButtonElement>("#refresh")!;
const onboarding = document.querySelector<HTMLDialogElement>("#onboarding")!;
document.querySelector<HTMLButtonElement>("#connect")!.onclick = () => onboarding.showModal();
document.querySelector<HTMLAnchorElement>(".brand")!.onclick = (event) => { event.preventDefault(); projects.scrollIntoView(); };

function el<K extends keyof HTMLElementTagNameMap>(tag: K, className = "", text = ""): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag); node.className = className; node.textContent = text; return node;
}
let busy = false;
let activeRequest: AbortController | undefined;
async function load(): Promise<void> {
  if (busy) return;
  if (!token) { notice.textContent = "Open the complete dashboard link printed by the CLI to connect this browser. Reloading this page requires that link again."; return; }
  busy = true; refresh.disabled = true; period.disabled = true;
  const requestToken = token;
  const controller = new AbortController();
  activeRequest = controller;
  const timeout = setTimeout(() => controller.abort(), 35000);
  notice.textContent = "Refreshing cache data…";
  try {
    const response = await fetch(`/api/snapshot?period=${encodeURIComponent(period.value)}`, {headers: {Authorization: `Bearer ${requestToken}`}, signal: controller.signal});
    if (!response.ok) throw new Error(response.status === 401 ? "Session expired. Open the current dashboard link from the CLI." : response.status === 504 ? "Dashboard request timed out. Refresh to try again." : "Dashboard unavailable. Check that the CLI process is running.");
    const data: Snapshot = await response.json();
    if (requestToken !== token) return;
    projects.replaceChildren();
    for (const project of data.projects) {
      const card = el("article", "project"); const heading = el("div", "project-head"); const title = el("div");
      title.append(el("h2", "", project.id.split("/").at(-1) ?? project.id), el("p", "", project.id));
      heading.append(el("span", "project-icon", "▱"), title, el("span", "pill", "Connected via CLI"));
      const prune = (cache: 'local' | 'team') => async (targetBytes: number) => {
        try {
          const response = await fetch('/api/prune', {method: 'POST', headers: {Authorization: `Bearer ${token}`, 'Content-Type': 'application/json'}, body: JSON.stringify({projectId: project.id, cache, targetBytes, confirm: true}), signal: AbortSignal.timeout(35000)});
          if (!response.ok) throw new Error(response.status === 401 ? 'Session expired. Open the current CLI dashboard link.' : response.status === 403 ? 'Pruning is not authorized for this cache.' : 'Pruning could not be confirmed.');
        } finally {
          while (busy) await new Promise(resolve => setTimeout(resolve, 25));
          await load();
        }
      };
      const grid = el("div", "cache-grid");
      if (project.local.state === 'not-selected') grid.classList.add('single-cache');
      else grid.append(renderCache("Local Cache", project.local, {onPrune: prune('local')}));
      grid.append(renderCache(scopeName(project.team.status?.visibility ?? 'unknown'), project.team, {onPrune: prune('team')}));
      card.append(heading, grid, renderCost(project.cost)); projects.append(card);
    }
    document.querySelector("#count")!.textContent = String(data.projects.length);
    document.querySelector("#updated")!.textContent = `Updated ${new Date(data.updatedAt).toLocaleTimeString()}`;
    notice.textContent = "Local and Team reports are separate observations; their savings should not be added together.";
  } catch (error) {
    if (requestToken !== token) return;
    projects.replaceChildren(); document.querySelector("#count")!.textContent = "—"; document.querySelector("#updated")!.textContent = "Snapshot unavailable";
    notice.textContent = controller.signal.aborted ? "Dashboard request timed out. Check the CLI connection and refresh." : error instanceof Error ? error.message : "Could not load cache data.";
  } finally {
    clearTimeout(timeout); activeRequest = undefined; busy = false; refresh.disabled = false; period.disabled = false;
    if (requestToken !== token) void load();
  }
}
window.addEventListener("hashchange", () => {
  const next = consumeSessionFragment();
  if (!next) return;
  if (busy && next === token) return;
  token = next;
  if (busy) activeRequest?.abort(); else void load();
});
refresh.onclick = () => { void load(); }; period.onchange = () => { void load(); }; void load();
