import { chromium, firefox, webkit, expect, type Page } from '@playwright/test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import type { ExecutionInventory } from '../../dashboard/execution-view.js';
const origin=process.env.CLOUD_ORIGIN!;
const engine=process.env.DASHBOARD_BROWSER||'chromium';
if(engine!=='chromium'&&engine!=='firefox'&&engine!=='webkit')throw new Error('Invalid DASHBOARD_BROWSER');
const output=process.env.QA_OUTPUT||fs.mkdtempSync('/tmp/layercache-cloud-browser-');fs.mkdirSync(output,{recursive:true});
const browser=await {chromium,firefox,webkit}[engine].launch({headless:true,...(engine==='chromium'?{executablePath:process.env.CHROMIUM_PATH||undefined,args:['--no-sandbox']}:{})});
const aliceContext=await browser.newContext({viewport:{width:1440,height:1050}}),bobContext=await browser.newContext({viewport:{width:390,height:844}});
const alice=await aliceContext.newPage(),bob=await bobContext.newPage();
const errors:string[]=[];for(const page of [alice,bob])page.on('pageerror',error=>errors.push(error.message));
const requests:{method:string;path:string;status:number}[]=[];
const apiURLs:string[]=[];
for(const page of [alice,bob])page.on('request',request=>{const url=new URL(request.url());if(url.pathname.startsWith('/api/'))apiURLs.push(url.href);});
for(const page of [alice,bob])page.on('response',response=>{const url=new URL(response.url());if(url.origin===origin&&url.pathname.startsWith('/api/')){requests.push({method:response.request().method(),path:url.pathname,status:response.status()});if(requests.length>20)requests.shift();}});
const results:{name:string;passed:boolean;error?:string}[]=[];
async function check(name:string,fn:()=>Promise<void>){try{await fn();results.push({name,passed:true});}catch(error){results.push({name,passed:false,error:error instanceof Error?error.message:String(error)});const diagnostics=await alice.evaluate(()=>({notice:document.querySelector('#notice')?.textContent,invalidFields:Array.from(document.querySelectorAll<HTMLInputElement>('input')).filter(input=>!input.validity.valid).map(input=>({name:input.name,message:input.validationMessage})),headings:Array.from(document.querySelectorAll('h1')).map(node=>node.textContent)}));const bobDiagnostics=await bob.evaluate(()=>({notice:document.querySelector('#notice')?.textContent,url:location.pathname,headings:Array.from(document.querySelectorAll('h1')).map(node=>node.textContent),body:document.querySelector('main')?.textContent?.slice(0,1200)}));console.error(JSON.stringify({failedCheck:name,diagnostics,bobDiagnostics,requests}));await alice.screenshot({path:path.join(output,name.replace(/\W/g,'-')+'.png'),fullPage:true});throw error;}}
async function login(page:Page,user:string){await page.goto(origin);await page.getByRole('link',{name:'Continue with GitHub'}).click();await page.getByRole('link',{name:'Sign in as '+user}).click();await page.getByText('@'+user,{exact:true}).first().waitFor({state:'attached'});const active=await page.request.get(origin+'/api/session');assert.equal(active.status(),200);assert.equal((await active.json()).user.login,user);}
async function callbackLink(page:Page,user:string){await page.goto(origin);await page.getByRole('link',{name:'Continue with GitHub'}).click();const link=await page.getByRole('link',{name:'Sign in as '+user}).getAttribute('href');assert.ok(link);return new URL(link,page.url());}
async function refreshView(page:Page){
 const pending=page.waitForResponse(response=>new URL(response.url()).pathname==='/api/session');
 await page.getByRole('button',{name:'Refresh',exact:true}).click();await pending;
 await expect.poll(async()=>{const refresh=page.getByRole('button',{name:'Refresh',exact:true});return await refresh.count()===0||await refresh.isEnabled();}).toBe(true);
}
const selectedProjectNames=new WeakMap<Page,string>();
async function view(page:Page,name:'Overview'|'Activity'|'Infrastructure'|'Storage'|'Settings'|'Account & team'){
 await expect(page.getByRole('button',{name:'Refresh',exact:true})).toBeEnabled();
 if(new URL(page.url()).pathname.startsWith('/projects/'))selectedProjectNames.set(page,await page.locator('main h1').innerText());
 if(name!=='Account & team'&&await page.getByRole('link',{name,exact:true}).count()===0){
  const selected=selectedProjectNames.get(page);assert.ok(selected,'Choose a project before opening a project page');
  await page.getByRole('button',{name:selected,exact:true}).click();
 }
 await page.getByRole('link',{name,exact:true}).click();
 await page.waitForURL(url=>name==='Account & team'?/\/teams\/[^/]+\/settings$/.test(url.pathname):url.pathname.endsWith('/'+name.toLowerCase()));
 if(name!=='Account & team')await page.locator('.page-content:not([aria-busy="true"])').waitFor();
}
function form(page:Page,title:string){return page.locator('form').filter({has:page.getByRole('heading',{name:title,exact:true})});}
try{
 await check('OAuth provider refuses an unknown client ID',async()=>{const context=await browser.newContext();try{const page=await context.newPage();await page.goto(origin);await page.getByRole('link',{name:'Continue with GitHub'}).click();const authorization=new URL(page.url());authorization.searchParams.set('client_id','unknown-client');const response=await page.goto(authorization.href);assert.equal(response?.status(),400);assert.equal((await page.request.get(origin+'/api/session')).status(),401);}finally{await context.close();}});
 await check('tampered OAuth state cannot sign in and fresh login recovers',async()=>{
  const context=await browser.newContext();
  try{
   const page=await context.newPage(),callback=await callbackLink(page,'alice');
   callback.searchParams.set('state','0'.repeat(48));
   assert.equal((await page.goto(callback.href))?.status(),403);
   assert.equal((await page.request.get(origin+'/api/session')).status(),401);
   await login(page,'alice');
   const session=await page.request.get(origin+'/api/session');
   assert.equal(session.status(),200);
   assert.equal((await session.json()).user.login,'alice');
  }finally{await context.close();}
 });
 await check('changed OAuth PKCE challenge cannot sign in',async()=>{
  const context=await browser.newContext();
  try{
   const page=await context.newPage();
   await page.goto(origin);
   await page.getByRole('link',{name:'Continue with GitHub'}).click();
   const authorization=new URL(page.url());
   authorization.searchParams.set('code_challenge','A'.repeat(43));
   await page.goto(authorization.href);
   const link=await page.getByRole('link',{name:'Sign in as alice'}).getAttribute('href');
   assert.ok(link);
   assert.equal((await page.goto(link))?.status(),403);
   assert.equal((await page.request.get(origin+'/api/session')).status(),401);
  }finally{await context.close();}
 });
 await check('OAuth callback cannot cross browser contexts',async()=>{
  const first=await browser.newContext(),second=await browser.newContext();
  try{
   const firstPage=await first.newPage(),secondPage=await second.newPage();
   await callbackLink(firstPage,'alice');
   const secondCallback=await callbackLink(secondPage,'bob');
   assert.equal((await firstPage.goto(secondCallback.href))?.status(),403);
   assert.equal((await firstPage.request.get(origin+'/api/session')).status(),401);
   await secondPage.goto(secondCallback.href);
   const session=await secondPage.request.get(origin+'/api/session');
   assert.equal(session.status(),200);
   assert.equal((await session.json()).user.login,'bob');
  }finally{await first.close();await second.close();}
 });
 await check('OAuth callback cannot be replayed after sign-out',async()=>{
  const context=await browser.newContext();
  try{
   const page=await context.newPage(),callback=await callbackLink(page,'alice');
   await page.goto(callback.href);
   await page.getByText('@alice',{exact:true}).first().waitFor();
   await page.getByRole('button',{name:'Sign out',exact:true}).click();
   await page.getByRole('link',{name:'Continue with GitHub'}).waitFor();
   assert.equal((await page.goto(callback.href))?.status(),403);
   assert.equal((await page.request.get(origin+'/api/session')).status(),401);
  }finally{await context.close();}
 });
 await check('OAuth PKCE login two independent identities',async()=>{await login(alice,'alice');await login(bob,'bob');assert.equal(await alice.getByText('@bob',{exact:true}).count(),0);});
 await check('non-creator can sign in but cannot create a team',async()=>{assert.equal(await bob.getByRole('button',{name:'Create team',exact:true}).count(),0);const session=await bob.request.get(origin+'/api/session');assert.equal(session.status(),200);const info=await session.json() as {csrfToken:string;canCreateTeam:boolean};assert.equal(info.canCreateTeam,false);const denied=await bob.request.post(origin+'/api/teams',{headers:{Origin:origin,'X-CSRF-Token':info.csrfToken},data:{name:'Unauthorized'}});assert.equal(denied.status(),403);assert.match((await denied.json()).error,/invited beta creators/);});
 await check('create team from empty workspace',async()=>{const f=form(alice,'New team');await f.getByLabel('Team name').fill('Acme engineering');await f.getByRole('button',{name:'Create team',exact:true}).click();await alice.getByText('Team created.',{exact:true}).waitFor();await form(alice,'Add project').waitFor();});
 await check('project requires GitHub repository administration',async()=>{const f=form(alice,'Add project');await f.getByLabel('Project name').fill('Web app');await f.getByLabel('GitHub repository').fill('acme/no-admin');await f.getByRole('button',{name:'Create project',exact:true}).click();await alice.getByRole('alert').filter({hasText:'administrator access is required'}).waitFor();});
 await check('project requires an immutable GitHub repository identity',async()=>{const f=form(alice,'Add project');await f.getByLabel('GitHub repository').fill('acme/zero-id');await f.getByRole('button',{name:'Create project',exact:true}).click();await alice.getByRole('alert').filter({hasText:'administrator access is required'}).waitFor();});
 await check('provision project and read live empty cache',async()=>{const f=form(alice,'Add project');await f.getByLabel('GitHub repository').fill('acme/web');await f.getByRole('button',{name:'Create project',exact:true}).click();await alice.getByText('Project created. Connect your CLI to start caching.').waitFor();await view(alice,'Overview');await alice.getByRole('region',{name:'Project overview',exact:true}).locator('[data-metric="hit-rate"] .stat-value').filter({hasText:'Not measured'}).waitFor();assert.equal(await alice.getByRole('heading',{name:'Web app',exact:true}).count(),1);});
 let project='';
 await check('CLI connection command contains selected cloud and project',async()=>{await view(alice,'Settings');const command=await alice.locator('pre').innerText();assert.ok(command.includes(`--cloud '${origin}'`));assert.ok(command.includes('--github-cli'));project=command.match(/--project '([^']+)'/)![1]!;assert.ok(project);});
 await check('CLI connect exchanges GitHub credentials and uploads real cache bytes',async()=>{const cliRoot=fs.mkdtempSync('/tmp/layercache-cloud-cli-');fs.writeFileSync(path.join(cliRoot,'gh'),'#!/bin/sh\nprintf \'fixture-github-alice\\n\'\n',{mode:0o700});const configPath=path.join(cliRoot,'project.json');const connected=spawnSync(process.env.LAYERCACHE_BIN!,['connect','--cloud',origin,'--project',project,'--github-cli','--config',configPath,'--json'],{encoding:'utf8',timeout:20000,env:{...process.env,PATH:cliRoot+path.delimiter+process.env.PATH,XDG_CACHE_HOME:path.join(cliRoot,'cache'),XDG_CONFIG_HOME:path.join(cliRoot,'config')}});assert.equal(connected.status,0,connected.stderr);const config: {teamToken:string;compatibilityId:string;projectId:string;teamUrl:string}=JSON.parse(fs.readFileSync(configPath,'utf8'));assert.equal(config.projectId,project);assert.equal(config.teamUrl,origin);const token=config.teamToken;const put=await alice.request.put(origin+'/v8/artifacts/qa-ui-artifact',{headers:{Authorization:'Bearer '+token,'X-LayerCache-Compatibility':config.compatibilityId},data:'cache-data'.repeat(1024)});assert.equal(put.status(),200);await view(alice,'Overview');await refreshView(alice);await alice.getByText('10.0 KiB',{exact:true}).first().waitFor();});
 await check('invite reader and accept as intended account',async()=>{await view(alice,'Account & team');const f=form(alice,'Invite teammate');await f.getByLabel('GitHub username').fill('bob');await f.getByRole('combobox').selectOption('reader');await f.getByRole('button',{name:'Send invitation'}).click();await alice.getByText('Invitation ready.',{exact:false}).waitFor();await refreshView(bob);await bob.getByRole('heading',{name:'Join Acme engineering'}).waitFor();await bob.getByRole('button',{name:'Accept invitation'}).click();await bob.getByText('Invitation accepted.',{exact:true}).waitFor();await bob.getByText('10.0 KiB',{exact:true}).first().waitFor();});
 await check('reader can see cache and cannot administer or create teams',async()=>{
  const pattern='**/api/teams/*/members';
  let releaseMembers!:()=>void,finishMembers!:()=>void,requestIntercepted=false;
  const gate=new Promise<void>(resolve=>{releaseMembers=resolve;}),handled=new Promise<void>(resolve=>{finishMembers=resolve;});
  await bob.route(pattern,async route=>{
   requestIntercepted=true;await gate;
   try{await route.continue();}finally{finishMembers();}
  });
  try{
   const requested=bob.waitForRequest(request=>/\/api\/teams\/[^/]+\/members$/.test(new URL(request.url()).pathname));
   await view(bob,'Account & team');await requested;
   const members=bob.getByRole('region',{name:'Team access',exact:true});
   const member=members.locator('.member').filter({has:bob.getByText('@bob',{exact:true})});
   // An empty loading panel cannot establish that administrator controls are absent.
   await expect(member).toHaveCount(0);releaseMembers();
   await expect(member).toBeVisible();await expect(member.locator('span')).toHaveText('reader');
   assert.equal(await bob.getByRole('button',{name:'Create team',exact:true}).count(),0);
   assert.equal(await bob.getByRole('button',{name:'Create project',exact:true}).count(),0);
   assert.equal(await bob.getByRole('button',{name:'Send invitation',exact:true}).count(),0);
   assert.equal(await bob.getByRole('button',{name:'Save role',exact:true}).count(),0);
  }finally{releaseMembers();if(requestIntercepted)await handled;await bob.unroute(pattern);}
 });
 await check('reader cannot prune at either UI or API',async()=>{
  await view(bob,'Storage');
  assert.equal(await bob.getByRole('button',{name:/^Prune /}).count(),0);
  const info=await (await bob.request.get(origin+'/api/session')).json() as {csrfToken:string};
  assert.equal((await bob.request.post(origin+`/api/projects/${project}/prune`,{headers:{Origin:origin,'X-CSRF-Token':info.csrfToken},data:{targetBytes:0,confirm:true}})).status(),403);
 });
 await check('administrator promotes reader to writer',async()=>{await view(alice,'Account & team');await refreshView(alice);await alice.getByLabel('Role for bob').selectOption('writer');const member=alice.locator('.member').filter({has:alice.getByText('@bob',{exact:true})});await member.getByRole('button',{name:'Save role'}).click();await alice.getByText('Role updated.',{exact:true}).waitFor();const updated=await (await bob.request.get(origin+'/api/session')).json() as {teams:{name:string;role:string}[]};assert.equal(updated.teams.find(team=>team.name==='Acme engineering')?.role,'writer');await refreshView(bob);await view(bob,'Account & team');await bob.getByText('Acme engineering / writer',{exact:true}).waitFor();await view(bob,'Storage');assert.equal(await bob.getByRole('button',{name:'Create project',exact:true}).count(),0);});
 await check('writer cannot prune at either UI or API',async()=>{
  await bob.getByText('10.0 KiB',{exact:true}).first().waitFor();assert.equal(await bob.getByRole('button',{name:/^Prune /}).count(),0);
  const info=await (await bob.request.get(origin+'/api/session')).json() as {csrfToken:string};
  assert.equal((await bob.request.post(origin+`/api/projects/${project}/prune`,{headers:{Origin:origin,'X-CSRF-Token':info.csrfToken},data:{targetBytes:0,confirm:true}})).status(),403);
 });
 await check('legacy allocation remains unknown in actual API and UI',async()=>{
  await view(alice,'Account & team');
  const session=await (await alice.request.get(origin+'/api/session')).json() as {teams:{id:string;kind:string}[]};const team=session.teams.find(team=>team.kind==='team')!;
  const response=await alice.request.get(origin+`/api/teams/${team.id}/usage`);assert.equal(response.status(),200);
  const usage=await response.json() as {projectCount:number;allocatedCacheBytes:number|null;allocationComplete:boolean};
  assert.equal(usage.projectCount,1);assert.equal(usage.allocatedCacheBytes,null);assert.equal(usage.allocationComplete,false);
  const account=alice.getByRole('region',{name:'Account usage',exact:true});await account.getByText('1 project · Allocated capacity unavailable',{exact:true}).waitFor();
  assert.doesNotMatch(await account.innerText(),/0 B allocated cache capacity/);
 });
 await check('execution inventory loads only the selected project and follows its reporting period',async()=>{
  const session=await (await alice.request.get(origin+'/api/session')).json() as {csrfToken:string;teams:{id:string;kind:string}[]};
  const team=session.teams.find(team=>team.kind==='team')!;
  const created=await alice.request.post(origin+`/api/teams/${team.id}/projects`,{headers:{Origin:origin,'X-CSRF-Token':session.csrfToken},data:{name:'Worker library',repository:'acme/worker'}});
  assert.equal(created.status(),201);const otherProject=(await created.json()).id as string;
  const snapshots:{project:string;period:string|null}[]=[];
  const pattern='**/api/projects/*/snapshot?*';
  await alice.route(pattern,async route=>{
   const url=new URL(route.request().url()),id=url.pathname.split('/')[3]!,period=url.searchParams.get('period');snapshots.push({project:id,period});
   assert.equal(id,project,'A focused project view requested a different project');
   const response=await route.fetch();assert.equal(response.status(),200);const snapshot=await response.json();
   const executions:ExecutionInventory={version:1,from:period==='7d'?'2026-10-13T12:00:00Z':'2026-10-19T12:00:00Z',to:'2026-10-20T12:00:00Z',updatedAt:'2026-10-20T12:00:00Z',retentionDays:30,staleAfterSeconds:86400,historyTruncated:false,jobs:[],installations:[]};
   await route.fulfill({response,json:{...snapshot,executions}});
  });
  try{
   await view(alice,'Infrastructure');await refreshView(alice);
   await alice.getByRole('button',{name:'Worker library',exact:true}).waitFor();
   assert.ok(snapshots.length>0);assert.ok(snapshots.every(item=>item.project===project));assert.equal(snapshots.filter(item=>item.project===otherProject).length,0);
   const before=snapshots.length,pending=alice.waitForResponse(response=>response.url().includes(`/api/projects/${project}/snapshot?period=7d`));await alice.getByLabel('Reporting period').selectOption('7d');await pending;
   assert.equal(snapshots.length,before+1,'Period changes request exactly the selected project');
   assert.equal(snapshots.at(-1)?.period,'7d');
  }finally{await alice.unroute(pattern);await view(alice,'Overview');await refreshView(alice);await alice.getByText('10.0 KiB',{exact:true}).first().waitFor();}
 });
 await check('hosted evidence and account costs keep scope and coverage',async()=>{
  const snapshot=await (await alice.request.get(origin+`/api/projects/${project}/snapshot`)).json();
  snapshot.cache.report={eligible:10,hits:7,misses:3,runs:13,degraded:false,hitRate:0,netEstimatedBuildTimeSaved:{milliseconds:3000,known:2,total:4,confidence:'low'},bytes:{downloaded:7168,uploaded:2048},timing:{lookupMs:10,downloadMs:200,verificationMs:30,restoreMs:400,uploadMs:50},sources:[{source:'teamCache',hits:6,downloadedBytes:6144,uploadedBytes:2048},{source:'publicCache',hits:1,downloadedBytes:1024,uploadedBytes:0}],integrations:[{integration:'turbo',hits:7,eligible:10}]};
  snapshot.cost={state:'estimate',currency:'EUR',periodStart:'2026-10-01T00:00:00Z',periodEnd:'2026-11-01T00:00:00Z',complete:false,items:[{name:'Known project estimate',amountMinor:80}],knownSubtotalMinor:80,totalMinor:null};
  const session=await (await alice.request.get(origin+'/api/session')).json() as {teams:{id:string;kind:string}[]};const team=session.teams.find(team=>team.kind==='team')!;
  const usage=await (await alice.request.get(origin+`/api/teams/${team.id}/usage`)).json();usage.cost={state:'recorded',currency:'USD',periodStart:'2026-10-01T00:00:00Z',periodEnd:'2026-11-01T00:00:00Z',complete:true,items:[{name:'Team storage',amountMinor:175}],knownSubtotalMinor:175,totalMinor:175};
  await alice.route(`**/api/projects/${project}/snapshot?*`,route=>route.fulfill({json:snapshot}));await alice.route(`**/api/teams/${team.id}/usage`,route=>route.fulfill({json:usage}));
  await view(alice,'Storage');await refreshView(alice);await alice.getByText('70%',{exact:true}).waitFor();
  const cache=alice.getByRole('region',{name:'Team Cache',exact:true});assert.match(await cache.innerText(),/Access · Team Cache/);
  assert.equal(await cache.locator('.source-row[data-source="teamCache"] th').innerText(),'Team Cache');assert.equal(await cache.locator('.source-row[data-source="publicCache"] td').first().innerText(),'1');
  assert.match(await cache.innerText(),/Native task outcomes and protocol activity have separate coverage/);
  const projectCost=alice.getByRole('region',{name:'Project cost',exact:true});assert.equal(await projectCost.locator('.cost-total').innerText(),'Unknown');assert.match(await projectCost.innerText(),/Known subtotal[\s\S]*0\.80.*EUR/);
  await view(alice,'Account & team');const teamCost=alice.getByRole('region',{name:'Team cost',exact:true});assert.match(await teamCost.locator('.cost-total').innerText(),/1\.75.*USD/);assert.match(await teamCost.innerText(),/Operator-recorded/);
  await view(alice,'Storage');assert.match(await alice.locator('main').innerText(),/Project and account costs may overlap/);
  await alice.unroute(`**/api/projects/${project}/snapshot?*`);await alice.unroute(`**/api/teams/${team.id}/usage`);await refreshView(alice);await alice.getByText('10.0 KiB',{exact:true}).first().waitFor();
  assert.equal(await alice.getByRole('region',{name:'Project cost',exact:true}).locator('.cost-total').innerText(),'Unknown');
 });
 await check('session survives reload and reporting period changes',async()=>{await bob.reload();await bob.getByText('10.0 KiB',{exact:true}).first().waitFor();const response=bob.waitForResponse(r=>r.url().includes('/snapshot?period=7d'));await bob.getByLabel('Reporting period').selectOption('7d');assert.equal((await response).status(),200);});
 await check('personal cache is owner-only and preserves the shared project',async()=>{
  assert.equal(await bob.getByRole('button',{name:'Create Personal Cache',exact:true}).count(),0);
  await view(alice,'Account & team');await alice.getByRole('button',{name:'Create Personal Cache',exact:true}).click();
  await alice.getByText('Personal Cache created. Only your account can access it.',{exact:true}).waitFor();
  await alice.getByRole('heading',{name:'Personal access',exact:true}).waitFor();
  assert.equal(await alice.getByRole('button',{name:'Send invitation',exact:true}).count(),0);
  assert.equal(await alice.getByRole('button',{name:'Save role',exact:true}).count(),0);
  const add=form(alice,'Add project');await add.getByLabel('Project name').fill('Private web');await add.getByLabel('GitHub repository').fill('acme/web');await add.getByRole('button',{name:'Create project',exact:true}).click();
  await alice.getByText('Project created. Connect your CLI to start caching.',{exact:true}).waitFor();
  await view(alice,'Overview');await alice.getByRole('region',{name:'Project overview',exact:true}).locator('[data-metric="hit-rate"] .stat-value').filter({hasText:'Not measured'}).waitFor();
  await view(alice,'Settings');const command=await alice.locator('pre').innerText(),privateProject=command.match(/--project '([^']+)'/)![1]!;
  assert.notEqual(privateProject,project);
  await refreshView(bob);
  assert.equal(await bob.getByRole('heading',{name:'Private web',exact:true}).count(),0);
  assert.equal((await bob.request.get(origin+`/api/projects/${privateProject}/snapshot`)).status(),403);
  const info=await (await alice.request.get(origin+'/api/session')).json() as {csrfToken:string;teams:{id:string;kind:string}[]};
  const personal=info.teams.find(t=>t.kind==='personal')!;
  assert.equal((await alice.request.post(origin+`/api/teams/${personal.id}/invitations`,{headers:{Origin:origin,'X-CSRF-Token':info.csrfToken},data:{login:'bob',role:'reader'}})).status(),403);
  await alice.reload();await alice.getByLabel('Team',{exact:true}).selectOption({label:'Personal Cache'});await alice.getByRole('heading',{name:'Private web',exact:true}).waitFor();await view(alice,'Storage');
  await alice.getByRole('button',{name:'Prune Personal Cache',exact:true}).waitFor();assert.match(await alice.getByRole('region',{name:'Personal Cache',exact:true}).innerText(),/Access · Personal Cache/);
  assert.equal(await alice.getByRole('button',{name:'Create Personal Cache',exact:true}).count(),0);
  await alice.getByLabel('Team',{exact:true}).selectOption({label:'Acme engineering'});await view(alice,'Storage');await alice.getByText('10.0 KiB',{exact:true}).first().waitFor();
 });
 for(const width of [320,390,768,1440])await check(`viewport ${width}`,async()=>{await alice.setViewportSize({width,height:900});assert.equal(await alice.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);await alice.screenshot({path:path.join(output,`viewport-${width}.png`),fullPage:true});});
 await check('hosted prune confirmation cancellation and uncertain mutation',async()=>{
  await view(alice,'Storage');
  let posts=0,snapshots=0;const listener=(request:import('@playwright/test').Request)=>{const url=new URL(request.url());if(url.pathname===`/api/projects/${project}/prune`&&request.method()==='POST')posts++;if(url.pathname===`/api/projects/${project}/snapshot`)snapshots++;};alice.on('request',listener);
  const prune=alice.getByRole('button',{name:'Prune Team Cache',exact:true});await prune.click();let dialog=alice.getByRole('dialog',{name:'Prune Team Cache'});
  await dialog.getByLabel('Target storage in bytes').fill('0');await dialog.getByLabel('I confirm deletion of eligible cached artifacts.').check();await dialog.getByRole('button',{name:'Cancel',exact:true}).click();assert.equal(posts,0);
  await alice.route(`**/api/projects/${project}/prune`,route=>route.fulfill({status:503,json:{error:'cache pruning could not be confirmed; refresh cache status'}}));
  await prune.click();dialog=alice.getByRole('dialog',{name:'Prune Team Cache'});await dialog.getByLabel('Target storage in bytes').fill('0');await dialog.getByLabel('I confirm deletion of eligible cached artifacts.').check();const before=snapshots;
  await dialog.getByRole('button',{name:'Prune cache',exact:true}).click();await dialog.getByText('A snapshot refresh was requested.',{exact:false}).waitFor();assert.equal(posts,1);assert.ok(snapshots>before);
  await alice.waitForTimeout(600);assert.equal(posts,1);await dialog.getByRole('button',{name:'Cancel',exact:true}).click();await alice.unroute(`**/api/projects/${project}/prune`);alice.off('request',listener);
 });
 await check('administrator prunes real isolated project and sees fresh storage',async()=>{
  await alice.getByRole('button',{name:'Prune Team Cache',exact:true}).click();const dialog=alice.getByRole('dialog',{name:'Prune Team Cache'});
  await dialog.getByLabel('Target storage in bytes').fill('0');await dialog.getByLabel('I confirm deletion of eligible cached artifacts.').check();
  const response=alice.waitForResponse(response=>new URL(response.url()).pathname===`/api/projects/${project}/prune`);await dialog.getByRole('button',{name:'Prune cache',exact:true}).click();assert.equal((await response).status(),200);
  await alice.waitForFunction(()=>!document.querySelector('dialog[open]'));const cache=alice.getByRole('region',{name:'Team Cache',exact:true});assert.equal(await cache.locator('.stats .value').first().innerText(),'0 B');
  await view(bob,'Storage');await refreshView(bob);await bob.getByRole('region',{name:'Team Cache',exact:true}).locator('.stats .value').first().filter({hasText:'0 B'}).waitFor();
 });
 await check('last administrator protection reaches UI',async()=>{await view(alice,'Account & team');await alice.getByLabel('Role for alice').selectOption('reader');const member=alice.locator('.member').filter({has:alice.getByText('@alice',{exact:true})});await member.getByRole('button',{name:'Save role'}).click();await alice.getByRole('alert').filter({hasText:'last administrator'}).waitFor();});
 await check('remove member immediately removes project visibility',async()=>{alice.once('dialog',dialog=>dialog.accept());await alice.locator('.member').filter({has:alice.getByText('@bob',{exact:true})}).getByRole('button',{name:'Remove'}).click();await alice.getByText('Member removed.',{exact:true}).waitFor();await refreshView(bob);await bob.getByRole('heading',{name:'Page unavailable'}).waitFor();assert.equal(await bob.getByRole('button',{name:'Web app',exact:true}).count(),0);assert.equal(await bob.getByRole('heading',{name:'Web app',exact:true}).count(),0);});
 await check('sign out destroys browser session',async()=>{await bob.getByRole('button',{name:'Sign out',exact:true}).click();await bob.getByRole('link',{name:'Continue with GitHub'}).waitFor();await bob.reload();await bob.getByRole('link',{name:'Continue with GitHub'}).waitFor();});
 await check('expired server session clears private workspace',async()=>{await alice.route('**/api/session',route=>route.fulfill({status:401,json:{error:'expired'}}));await refreshView(alice);await alice.getByRole('link',{name:'Continue with GitHub'}).waitFor();assert.equal(await alice.locator('pre').count(),0);assert.equal(await alice.getByRole('heading',{name:'Web app',exact:true}).count(),0);});
 await check('customer API request URLs use only the authenticated origin',async()=>{for(const raw of apiURLs){const url=new URL(raw);assert.equal(url.origin,origin);assert.equal(url.hash,'');for(const name of url.searchParams.keys())assert.ok(name==='period','Unexpected API query selector: '+name);}});
 await check('no browser exceptions',async()=>assert.deepEqual(errors,[]));
}catch(error){console.error(error);}finally{fs.writeFileSync(path.join(output,'results.json'),JSON.stringify(results,null,2));console.log(JSON.stringify({browser:engine,output,results},null,2));await browser.close();if(results.some(r=>!r.passed))process.exitCode=1;}
