import {bytes, renderCost, renderScopes} from '../dashboard/cache-view.js';
import {renderConnection, renderProjectPage} from './project-pages.js';
import {el, panel} from './ui.js';
import type {Member, Project, ProjectPage, PublicConfig, Session, Snapshot, Team, TeamUsage} from './types.js';
const root = document.querySelector<HTMLElement>('#app')!;
const notice = document.querySelector<HTMLElement>('#notice')!;
let session:Session|undefined, generation=0, passwordFormOpen=false;
let workspace:Workspace|undefined;
let publicConfig:PublicConfig|undefined;
class Unauthorized extends Error {}
function message(text:string,error=false){notice.textContent=text;notice.className=error?'notice error':'notice';notice.setAttribute('role',error?'alert':'status');}
async function api<T>(path:string,method='GET',body?:unknown,expireSession=true):Promise<T>{
 const version=generation;
 const response=await fetch(path,{method,credentials:'same-origin',headers:{'Content-Type':'application/json',...(method!=='GET'?{'X-CSRF-Token':session?.csrfToken??''}:{})},...(body===undefined?{}:{body:JSON.stringify(body)}),signal:AbortSignal.timeout(20000)});
 if(response.status===401&&expireSession){const detail='Your session expired. Sign in to continue.';if(version===generation){const wasSignedIn=!!session;session=undefined;passwordFormOpen=false;generation++;renderLogin();if(wasSignedIn)message(detail,true);}throw new Unauthorized(detail);}
 if(!response.ok){let detail='Request failed. Please try again.';try{detail=(await response.json()).error??detail;}catch{}throw new Error(detail);}
 return response.status===204?undefined as T:await response.json() as T;
}
function button(text:string,action:()=>Promise<void>|void,className=''):HTMLButtonElement {const b=el('button',text,className);b.type='button';b.onclick=async()=>{const version=generation;b.disabled=true;message('');try{await action();}catch(error){if(version===generation)message(error instanceof Error?error.message:'Request failed',true);}finally{b.disabled=false;}};return b;}
function field(form:HTMLElement,label:string,name:string,placeholder='',required=true):HTMLInputElement{const wrap=el('label',label);const input=el('input');input.name=name;input.placeholder=placeholder;input.required=required;input.maxLength=name==='repository'?202:80;wrap.append(input);form.append(wrap);return input;}
function roleField(form:HTMLElement,label='Role',value='reader'){const wrap=el('label',label);const select=el('select');for(const role of ['reader','writer','admin']){const option=el('option',role);option.value=role;select.append(option);}select.value=value;wrap.append(select);form.append(wrap);return select;}
function form(title:string,submit:string,action:(form:HTMLFormElement)=>Promise<void>):HTMLFormElement {const node=el('form');node.append(el('h3',title));const b=el('button',submit);b.type='submit';node.onsubmit=async event=>{event.preventDefault();b.disabled=true;message('');try{await action(node);}catch(error){message(error instanceof Error?error.message:'Request failed',true);}finally{b.disabled=false;}};node.append(b);return node;}
function value(form:HTMLFormElement,name:string){return (form.elements.namedItem(name) as HTMLInputElement).value.trim();}
function appendField(form:HTMLFormElement,label:string,name:string,placeholder=''){const input=field(form,label,name,placeholder);form.append(form.querySelector('button')!);return input;}
function passwordField(form:HTMLFormElement,label:string,name:string,autocomplete:'current-password'|'new-password'):HTMLInputElement{const input=appendField(form,label,name);input.type='password';input.autocomplete=autocomplete;input.maxLength=72;return input;}
function renderLogin(){
 document.title='Sign in · LayerCache';
 const version=++generation;root.replaceChildren();const card=el('section','','welcome');card.append(el('p','LAYER CACHE CLOUD','eyebrow'),el('h1','One cache. Your whole team.'),el('p','Connect your CLI, share project caches, and see what your builds reuse.'));const methods=el('div');methods.append(el('p','Loading sign-in options…','muted'));card.append(methods);root.append(card);
 void api<PublicConfig>('/api/public-config','GET',undefined,false).then(config=>{
  if(version!==generation||session)return;publicConfig=config;methods.replaceChildren();
  if(config.githubClientId){const link=el('a','Continue with GitHub','primary');link.href='/auth/github';methods.append(link,el('p','Approved beta creators can create teams. Team administrators need GitHub repository administrator access to add projects. Invited teammates can join existing teams.','muted'));}
  if(config.passwordLogin){let password:HTMLInputElement;const login=form('Email and password','Sign in',async f=>{try{await api('/auth/password','POST',{email:value(f,'email'),password:password.value},false);await refresh();}finally{password.value='';}});const email=appendField(login,'Email','email');email.type='email';email.autocomplete='username';email.maxLength=254;password=passwordField(login,'Password','password','current-password');login.append(el('p','Use the account provided by your LayerCache operator.','muted'));methods.append(login);}
  if(!config.githubClientId&&!config.passwordLogin)methods.append(el('p','Sign-in is not configured. Contact your LayerCache operator.','error'));
 }).catch(error=>{if(version===generation&&!session){methods.replaceChildren(el('p','Unable to load sign-in options.','error'),button('Retry',renderLogin));message(error instanceof Error?error.message:'Please retry',true);}});
}
function renderPasswordChange(required:boolean):HTMLElement{
 const card=el('section','','card');card.setAttribute('aria-label','Account password');let currentPassword:HTMLInputElement,newPassword:HTMLInputElement;
 const change=form('Change your password','Save password',async()=>{try{if(new TextEncoder().encode(newPassword.value).byteLength>72)throw new Error('Choose a password with at most 72 UTF-8 bytes.');await api('/api/password/change','POST',{currentPassword:currentPassword.value,newPassword:newPassword.value});passwordFormOpen=false;await refresh();message('Password changed.');}finally{currentPassword.value='';newPassword.value='';}});
 currentPassword=passwordField(change,'Current password','currentPassword','current-password');newPassword=passwordField(change,'New password','newPassword','new-password');newPassword.minLength=12;
 change.append(el('p',required?'Choose a new password before opening your workspace.':'Use at least 12 characters and at most 72 UTF-8 bytes.','muted'));if(!required)change.append(button('Cancel',()=>{passwordFormOpen=false;render();}));card.append(change);return card;
}

type Route = {kind: 'project'; project: string; page: ProjectPage} | {kind: 'team'; team: string} | {kind: 'missing'};
const pages: {id: ProjectPage; label: string; description: string}[] = [
  {id: 'overview', label: 'Overview', description: 'Your project cache at a glance'},
  {id: 'activity', label: 'Activity', description: 'Explore cache outcomes and artifact reuse'},
  {id: 'infrastructure', label: 'Infrastructure', description: 'CI runs and persistent clients using this project'},
  {id: 'storage', label: 'Storage', description: 'Capacity, retained artifacts and cache maintenance'},
  {id: 'settings', label: 'Settings', description: 'Connect this project and manage access'},
];
function period(): '24h' | '7d' {return new URL(location.href).searchParams.get('period') === '7d' ? '7d' : '24h';}
function projectURL(project: string, page: ProjectPage, range: string = period()): string {
  return `/projects/${encodeURIComponent(project)}/${page}${range === '7d' ? '?period=7d' : ''}`;
}
function teamURL(team: string): string {return `/teams/${encodeURIComponent(team)}/settings`;}
function initialRoute(current: Session): string {
  let saved: string | null = null;
  try {saved = localStorage.getItem(`layercache-project:${current.user.id}`);} catch {}
  const projects = current.projects ?? [];
  const project = projects.find(item => item.id === saved) ?? projects[0];
  return project ? projectURL(project.id, 'overview') : current.teams?.[0] ? teamURL(current.teams[0].id) : '/';
}
function readRoute(): Route {
  try {
    const project = /^\/projects\/([^/]+)\/(overview|activity|infrastructure|storage|settings)\/?$/.exec(location.pathname);
    if (project) return {kind: 'project', project: decodeURIComponent(project[1]!), page: project[2] as ProjectPage};
    const team = /^\/teams\/([^/]+)\/settings\/?$/.exec(location.pathname);
    if (team) return {kind: 'team', team: decodeURIComponent(team[1]!)};
  } catch {}
  return {kind: 'missing'};
}
function intercept(link: HTMLAnchorElement, action: () => void): void {
  link.onclick = event => {if (event.button || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return; event.preventDefault(); action();};
}
async function refresh() {
  const version = generation;
  const [current, config] = await Promise.all([api<Session>('/api/session'), api<PublicConfig>('/api/public-config', 'GET', undefined, false)]);
  if (version !== generation) return;
  session = current; publicConfig = config; render();
}
function render() {
  workspace?.dispose(); workspace = undefined;
  if (!session) return renderLogin();
  const current = session;
  generation++;
  root.replaceChildren();
  if (current.passwordChangeRequired || passwordFormOpen) {
    const header = el('header', '', 'app-header'); header.append(el('span', 'LayerCache', 'brand'), button('Sign out', signOut));
    root.append(header, renderPasswordChange(current.passwordChangeRequired)); return;
  }
  if (location.pathname === '/') history.replaceState({}, '', initialRoute(current));
  workspace = new Workspace(current); root.append(workspace.element); workspace.open(false);
}
async function signOut() {
  await api('/api/logout', 'POST'); workspace?.dispose(); workspace = undefined;
  session = undefined; passwordFormOpen = false; generation++; renderLogin(); message('Signed out.');
}

// One workspace owns navigation and one selected project's requests. A period
// update retains the shell, controls and previous data until its response wins.
class Workspace {
  readonly element = el('div', '', 'workspace');
  private readonly sidebar = el('nav', '', 'sidebar');
  private readonly main = el('main', '', 'main');
  private readonly heading = el('div', '', 'project-header');
  private readonly tabs = el('nav', '', 'project-tabs');
  private readonly toolbar = el('div', '', 'project-toolbar');
  private readonly content = el('div', '', 'page-content');
  private readonly update = el('span', '', 'update-status');
  private readonly selector = el('select');
  private readonly requestError = el('p', '', 'request-error');
  private route: Route = {kind: 'missing'};
  private request = 0;
  private alive = true;
  private readonly version = generation;
  constructor(private readonly current: Session) {
    const header = el('header', '', 'app-header');
    const brand = el('a', '', 'brand'); brand.href = '/';
    const mark = el('span', '', 'brand-mark'); mark.setAttribute('aria-hidden', 'true');
    brand.append(mark, el('span', 'LayerCache'), el('span', 'Cloud', 'brand-edition'));
    intercept(brand, () => this.navigate(initialRoute(this.current)));
    const actions = el('div', '', 'header-actions');
    actions.append(el('span', `@${current.user.login}`, 'account-name'), button('Refresh', async () => {await refresh();}), button('Sign out', signOut));
    header.append(brand, actions); this.element.append(header);
    this.sidebar.setAttribute('aria-label', 'Teams and projects');
    this.tabs.setAttribute('aria-label', 'Project pages');
    this.update.setAttribute('role', 'status'); this.update.setAttribute('aria-live', 'polite');
    this.selector.setAttribute('aria-label', 'Reporting period');
    for (const [id, label] of [['24h', 'Last 24 hours'], ['7d', 'Last 7 days']]) {const option = el('option', label!); option.value = id!; this.selector.append(option);}
    this.selector.onchange = () => {
      if (this.route.kind !== 'project') return;
      const href = projectURL(this.route.project, this.route.page, this.selector.value);
      history.replaceState(history.state, '', href); this.updateLinks(); void this.load(true);
    };
    const periodControl = el('label', 'Cache activity', 'period-control'); periodControl.append(this.selector);
    this.toolbar.append(periodControl, this.update);
    this.requestError.setAttribute('role', 'alert');
    this.main.append(this.heading, this.tabs, this.toolbar, this.requestError, this.content);
    const layout = el('div', '', 'layout workspace-body'); layout.append(this.sidebar, this.main); this.element.append(layout);
  }
  dispose() {this.alive = false; this.request++;}
  private valid(order?: number): boolean {return this.alive && this.version === generation && (order === undefined || order === this.request);}
  navigate(href: string) {
    if (!this.valid()) return;
    history.replaceState({...history.state, scrollY: window.scrollY}, '', location.href);
    history.pushState({scrollY: 0}, '', href); this.open(true);
  }
  open(scroll: boolean, restoreY?: number) {
    this.request++; this.route = readRoute(); this.selector.value = period();
    this.heading.replaceChildren(); this.tabs.replaceChildren(); this.content.replaceChildren();
    this.content.style.minHeight = ''; this.content.removeAttribute('aria-busy');
    this.update.textContent = ''; this.requestError.textContent = '';
    const project = this.route.kind === 'project' ? this.current.projects?.find(item => item.id === this.routeProject()) : undefined;
    const teamID = project?.teamId ?? (this.route.kind === 'team' ? this.route.team : '');
    const team = this.current.teams?.find(item => item.id === teamID);
    this.renderSidebar(team, project);
    this.toolbar.hidden = !project || this.route.kind !== 'project' || this.route.page === 'settings';
    this.tabs.hidden = !project;
    if (scroll) window.scrollTo(0, 0);
    if (project && team && this.route.kind === 'project') {
      document.title = `${project.name} · ${pages.find(item => item.id === this.routePage())?.label ?? 'Project'} · LayerCache`;
      try {localStorage.setItem(`layercache-project:${this.current.user.id}`, project.id);} catch {}
      const title = el('div', '', 'project-title');
      title.append(el('p', team.name, 'eyebrow'), el('h1', project.name), el('p', project.repository, 'project-meta'));
      const scope = el('span', team.kind === 'personal' ? 'Personal Cache' : 'Team Cache', 'badge');
      this.heading.append(title, scope);
      for (const page of pages) {
        const link = el('a', page.label); link.dataset.page = page.id; link.href = projectURL(project.id, page.id);
        if (page.id === this.route.page) {link.className = 'active'; link.setAttribute('aria-current', 'page');}
        intercept(link, () => this.navigate(link.href)); this.tabs.append(link);
      }
      if (this.route.page === 'settings') {
        this.content.append(renderConnection(project, this.current, Boolean(publicConfig?.githubClientId), {message}));
        this.renderProjectAccess(team); return;
      }
      this.content.append(el('div', 'Loading project activity…', 'loading-state'));
      void this.load(false, restoreY); return;
    }
    if (this.route.kind === 'team' && team || location.pathname === '/' && !(this.current.teams?.length)) {
      this.renderTeamSettings(team); return;
    }
    this.heading.append(el('h1', 'Page unavailable'));
    document.title = 'Page unavailable · LayerCache';
    this.content.append(el('p', 'This page does not exist or your account no longer has access. Choose a project from the sidebar.', 'muted'));
  }
  private routeProject(): string {return this.route.kind === 'project' ? this.route.project : '';}
  private routePage(): ProjectPage | undefined {return this.route.kind === 'project' ? this.route.page : undefined;}
  private updateLinks() {
    if (this.route.kind !== 'project') return;
    for (const link of Array.from(this.tabs.querySelectorAll<HTMLAnchorElement>('a[data-page]'))) link.href = projectURL(this.route.project, link.dataset.page as ProjectPage);
  }
  private renderSidebar(team?: Team, project?: Project) {
    this.sidebar.replaceChildren(); this.sidebar.append(el('p', 'WORKSPACE', 'sidebar-heading'));
    const selector = el('select'); selector.setAttribute('aria-label', 'Team');
    for (const item of this.current.teams ?? []) {const option = el('option', item.name); option.value = item.id; selector.append(option);}
    if (team) selector.value = team.id;
    selector.onchange = () => {const first = this.current.projects?.find(item => item.teamId === selector.value); this.navigate(first ? projectURL(first.id, 'overview') : teamURL(selector.value));};
    if (this.current.teams?.length) this.sidebar.append(selector);
    this.sidebar.append(el('p', 'PROJECTS', 'sidebar-heading'));
    const list = el('div', '', 'project-list');
    for (const item of (this.current.projects ?? []).filter(item => !team || item.teamId === team.id)) {
      const b = button(item.name, () => this.navigate(projectURL(item.id, 'overview')), `project-button${item.id === project?.id ? ' selected' : ''}`);
      b.setAttribute('aria-pressed', String(item.id === project?.id)); list.append(b);
    }
    if (!list.childElementCount) list.append(el('p', 'No projects yet', 'muted'));
    this.sidebar.append(list);
    const footer = el('div', '', 'sidebar-footer');
    const settings = el('a', 'Account & team', 'sidebar-settings'); settings.href = team ? teamURL(team.id) : '/'; intercept(settings, () => this.navigate(settings.href)); footer.append(settings);
    if (this.current.invitations?.length) {
      const invitations = button(`${this.current.invitations.length} pending invitation${this.current.invitations.length === 1 ? '' : 's'}`, () => this.navigate(team ? teamURL(team.id) : '/'), 'invitation-link'); footer.append(invitations);
    }
    footer.append(el('p', 'Personal · Team · Public', 'muted')); this.sidebar.append(footer);
  }
  private async load(preserve: boolean, restoreY?: number): Promise<void> {
    if (this.route.kind !== 'project') return;
    const project = this.current.projects?.find(item => item.id === this.routeProject());
    const team = this.current.teams?.find(item => item.id === project?.teamId);
    if (!project || !team) return;
    const page = this.route.page, range = this.selector.value, order = ++this.request;
    if (preserve) this.content.style.minHeight = `${Math.ceil(this.content.getBoundingClientRect().height)}px`;
    this.content.setAttribute('aria-busy', 'true'); this.update.textContent = 'Updating cache activity…'; this.requestError.textContent = '';
    try {
      const snapshot = await api<Snapshot>(`/api/projects/${encodeURIComponent(project.id)}/snapshot?period=${encodeURIComponent(range)}`);
      if (!this.valid(order)) return;
      const y = window.scrollY, x = window.scrollX;
      const view = renderProjectPage(page, project, team, snapshot, {api, message, reload: () => this.load(true), navigate: next => this.navigate(projectURL(project.id, next))});
      this.content.replaceChildren(view);
      this.update.textContent = `Updated ${new Date(snapshot.updatedAt).toLocaleTimeString(undefined, {hour: '2-digit', minute: '2-digit'})}`;
      if (preserve) window.scrollTo(x, y); else if (restoreY !== undefined) window.scrollTo(0, restoreY);
    } catch (error) {
      if (this.valid(order)) {
        this.requestError.textContent = `${error instanceof Error ? error.message : 'Project activity unavailable.'}${preserve ? ' Showing the previous report. Refresh to retry.' : ''}`;
        this.update.textContent = 'Update failed';
        if (!preserve) this.content.replaceChildren(button('Retry project activity', () => this.load(false)));
      }
    } finally {if (this.valid(order)) this.content.removeAttribute('aria-busy');}
  }
  private renderProjectAccess(team: Team) {
    const access = panel('Project access');
    if (team.operatorManaged) access.append(el('p', 'These caches keep their original project identities and native membership authority. Project registration and access changes are managed by the operator.'), el('p', 'Current native permissions and revocations apply to every read, publication and prune.', 'muted'));
    else access.append(el('p', 'Project access follows your team role. Readers restore, writers publish, and administrators manage access.'));
    const link = el('a', 'Manage account and team', 'text-link'); link.href = teamURL(team.id); intercept(link, () => this.navigate(link.href)); access.append(link); this.content.append(access);
  }
  private renderTeamSettings(team?: Team) {
    const current = this.current, version = this.version;
    document.title = `${team?.name ?? 'Account'} · Settings · LayerCache`;
    this.heading.append(el('div')); this.heading.firstElementChild!.append(el('p', 'ACCOUNT & TEAM', 'eyebrow'), el('h1', team?.name ?? 'Welcome to your cloud workspace'));
    for (const invite of current.invitations ?? []) {
      const card = panel(`Join ${invite.teamName}`); card.append(el('p', `You were invited as a ${invite.role}.`), button('Accept invitation', async () => {await api(`/api/invitations/${encodeURIComponent(invite.id)}/accept`, 'POST'); history.replaceState({}, '', '/'); await refresh(); message('Invitation accepted.');})); this.content.append(card);
    }
    if (current.canChangePassword) this.content.append(button('Change password', () => {passwordFormOpen = true; render();}));
    if (current.canManageGitHub && current.canCreateTeam) {
      const create = panel('Your workspaces');
      if (!(current.teams ?? []).some(item => item.kind === 'personal')) create.append(button('Create Personal Cache', async () => {const cache = await api<Team>('/api/teams', 'POST', {kind: 'personal'}); history.pushState({}, '', teamURL(cache.id)); await refresh(); message('Personal Cache created. Only your account can access it.');}));
      const newTeam = form('New team', 'Create team', async f => {const created = await api<Team>('/api/teams', 'POST', {name: value(f, 'name')}); history.pushState({}, '', teamURL(created.id)); await refresh(); message('Team created.');}); appendField(newTeam, 'Team name', 'name', 'Acme engineering'); create.append(newTeam); this.content.append(create);
    }
    if (!team) {this.content.append(el('p', current.canCreateTeam ? 'Create a team to share caches, or accept an invitation above.' : 'Ask your LayerCache operator or team administrator for project access.', 'muted')); return;}
    this.content.append(el('p', `${team.name} / ${team.role}`, 'eyebrow'));
    const usage = panel('Account usage', team.kind === 'personal' ? 'Personal account usage' : 'Team usage'); usage.classList.add('account-usage');
    const data = el('div', 'Loading account usage…', 'muted'); usage.append(data); this.content.append(usage);
    void api<TeamUsage>(`/api/teams/${encodeURIComponent(team.id)}/usage`).then(result => {
      if (!this.valid() || version !== generation || !data.isConnected) return;
      const capacity = result.allocationComplete && result.allocatedCacheBytes !== null ? `${bytes(result.allocatedCacheBytes)} allocated cache capacity` : 'Allocated capacity unavailable';
      data.replaceChildren(el('p', `${result.projectCount} ${result.projectCount === 1 ? 'project' : 'projects'} · ${capacity}`, 'muted'), renderCost(result.cost, team.kind === 'personal' ? 'Personal account cost' : 'Team cost'), el('p', 'Allocated capacity is a configured limit. It does not measure current usage or billed storage.', 'coverage-note'));
    }).catch(error => {if (this.valid() && data.isConnected) data.replaceChildren(el('p', error instanceof Error ? error.message : 'Usage unavailable', 'error'), renderCost(undefined, team.kind === 'personal' ? 'Personal account cost' : 'Team cost'));});
    this.content.append(renderScopes());
    if (team.operatorManaged) {const access = panel('Operator-managed access'); access.append(el('p', 'Existing project identities and native membership authority remain in use. Dashboard membership does not grant cache access. Contact your operator to register projects or change native access.')); this.content.append(access); return;}
    if (!current.canManageGitHub) {this.content.append(panel('Project access', 'Project registration and membership changes require GitHub sign-in or help from your LayerCache operator.')); return;}
    if (team.role === 'admin') {
      const add = form('Add project', 'Create project', async f => {const created = await api<Project>(`/api/teams/${team.id}/projects`, 'POST', {name: value(f, 'name'), repository: value(f, 'repository')}); history.pushState({}, '', projectURL(created.id, 'settings')); await refresh(); message('Project created. Connect your CLI to start caching.');});
      appendField(add, 'Project name', 'name', 'Web app'); appendField(add, 'GitHub repository', 'repository', 'owner/repository'); add.append(el('p', 'Your GitHub account must administer this repository.', 'muted')); this.content.append(add);
    }
    if (team.kind === 'personal') {const access = panel('Personal access'); access.append(el('p', `Only @${current.user.login} can access these projects. Connect your machines with the same account. Personal Cache cannot accept invitations.`), el('p', 'GitHub Actions access requires a workflow initiated by your account in the configured repository.', 'muted')); this.content.append(access); return;}
    const members = panel('Team access', 'Readers restore caches. Writers also upload. Administrators manage the team and projects.'); this.content.append(members);
    void api<Member[]>(`/api/teams/${team.id}/members`).then(list => {
      if (!this.valid() || !members.isConnected) return;
      for (const member of list ?? []) {
        const row = el('div', '', 'member'); row.append(el('strong', `@${member.login}`));
        if (team.role === 'admin') {
          const role = roleField(row, `Role for ${member.login}`, member.role);
          row.append(button('Save role', async () => {await api(`/api/teams/${team.id}/members/${member.userId}`, 'PUT', {role: role.value}); await refresh(); message('Role updated.');}), button('Remove', async () => {if (!window.confirm(`Remove @${member.login} from ${team.name}? Their cache access will end.`)) return; await api(`/api/teams/${team.id}/members/${member.userId}`, 'DELETE'); await refresh(); message('Member removed.');}, 'danger'));
        } else row.append(el('span', member.role)); members.append(row);
      }
    }).catch(error => {if (this.valid() && members.isConnected) members.append(el('p', error instanceof Error ? error.message : 'Unable to load members', 'error'));});
    if (team.role === 'admin') {
      let role: HTMLSelectElement;
      const invite = form('Invite teammate', 'Send invitation', async f => {await api(`/api/teams/${team.id}/invitations`, 'POST', {login: value(f, 'login'), role: role.value}); f.reset(); message('Invitation ready. Ask your teammate to sign in here within 7 days and accept it.');});
      appendField(invite, 'GitHub username', 'login', 'teammate'); role = roleField(invite); invite.append(invite.querySelector('button')!); invite.append(el('p', 'Invitations appear when your teammate signs in. No email is sent.', 'muted')); members.append(invite);
    }
  }
}
function openWorkspace() {
  const version = generation;
  void refresh().catch(error => {
    if (version !== generation) return;
    if (error instanceof Unauthorized) {message(''); return;}
    root.replaceChildren(el('h1', 'Unable to load your workspace'), button('Retry', refresh)); message(error instanceof Error ? error.message : 'Please retry', true);
  });
}
window.addEventListener('popstate', () => {workspace?.open(false, typeof history.state?.scrollY === 'number' ? history.state.scrollY : 0);});
window.addEventListener('pagehide', () => {generation++; workspace?.dispose();});
window.addEventListener('pageshow', event => {if (!event.persisted) return; session = undefined; workspace = undefined; passwordFormOpen = false; message(''); root.replaceChildren(el('p', 'Restoring your workspace…', 'muted')); openWorkspace();});
openWorkspace();
