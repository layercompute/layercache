// Exercise page hierarchy, browser navigation and in-place updates against the
// shipped portal bundle. The synthetic metrics qualify presentation only.
import {chromium, firefox, webkit, expect} from '@playwright/test';
import assert from 'node:assert/strict';
import {createServer, type ServerResponse} from 'node:http';
import {readFileSync, mkdirSync, mkdtempSync, writeFileSync} from 'node:fs';
import path from 'node:path';
import type {ExecutionEntry, ExecutionInventory} from '../../dashboard/execution-view.js';
import type {Snapshot} from '../../cloud/types.js';

declare global {interface Window {uxDocument?: string; uxSidebar?: Element | null; uxPeriod?: Element | null;}}
const engine=process.env.DASHBOARD_BROWSER||'chromium';
if(engine!=='chromium'&&engine!=='firefox'&&engine!=='webkit')throw new Error('Invalid DASHBOARD_BROWSER');
const output=process.env.QA_OUTPUT?path.join(process.env.QA_OUTPUT,'project-ux'):mkdtempSync('/tmp/layercache-project-ux-');mkdirSync(output,{recursive:true});
const projects=[
 {id:'phone',teamId:'engineering',name:'Phone App',repository:'example/phone-app',role:'admin',operatorManaged:true},
 {id:'library',teamId:'engineering',name:'Library',repository:'example/library',role:'reader',operatorManaged:true},
 {id:'github.com/example/shared',teamId:'engineering',name:'Shared library',repository:'example/shared',role:'reader',operatorManaged:true},
];
const observedAt='2026-10-20T12:00:00Z';
function entry(id:string,executionId:string,kind:'client'|'githubActions',installationId?:string):ExecutionEntry{return {id,ownerId:'github-id:1',installationId,executionId,kind,provider:'unknown',providerEvidence:'unknown',runnerEnvironment:kind==='githubActions'?'self-hosted':'unknown',lastObservedAt:observedAt,phase:'finish',cacheActivity:'observed',sample:{os:'linux',arch:'amd64',cpu:{visibleLogical:8,affinityLogical:2,quotaMilliCPU:2000,effectiveMilliCPU:2000},memory:{visibleTotalBytes:16*1024**3,cgroupLimitBytes:4*1024**3,effectiveLimitBytes:4*1024**3},filesystems:[{id:'volume-a',roles:['workspace','staging'],capacityBytes:100*1024**3,freeBytes:40*1024**3,availableBytes:35*1024**3}]}};}
const executions:ExecutionInventory={version:1,from:'2026-10-19T12:00:00Z',to:observedAt,updatedAt:observedAt,retentionDays:30,staleAfterSeconds:86400,historyTruncated:false,installations:[entry('client-1','client:first','client','stable-installation'),entry('client-2','client:second','client','stable-installation')],jobs:[entry('job-1','github-actions:123:456:1:789','githubActions'),entry('job-2','github-actions:123:456:1:790','githubActions'),entry('job-3','github-actions:123:456:2:791','githubActions')]};
const baseSnapshot:Snapshot={scope:'team',updatedAt:observedAt,cache:{state:'connected',reportState:'available',status:{usageBytes:512*1024**2,maxBytes:5*1024**3,artifacts:32,entries:32,role:'admin',visibility:'team',storageBackend:'s3',canPrune:false,storageByIntegration:[{integration:'turbo',usageBytes:256*1024**2,artifacts:16,entries:16},{integration:'actions',usageBytes:128*1024**2,artifacts:8,entries:8},{integration:'shared',usageBytes:128*1024**2,artifacts:8,entries:8}]},report:{eligible:10,hits:7,misses:3,runs:4,degraded:false,netEstimatedBuildTimeSaved:{milliseconds:null,known:0,total:4,confidence:'unknown'},bytes:{downloaded:7168,uploaded:2048},timing:{lookupMs:10,downloadMs:200,verificationMs:30,restoreMs:400,uploadMs:50},sources:[{source:'teamCache',hits:7,downloadedBytes:7168,uploadedBytes:2048}],integrations:[{integration:'buildkit',hits:5,eligible:6},{integration:'turbo',hits:2,eligible:4}]}},executions};
let origin='';
const snapshots:{project:string;period:string;method:string}[]=[],held:{response:ServerResponse;period:string}[]=[];
let hold=false,capacityBytes=5*1024**3,failNextCapacityWrite=false,capacityCanChange=true;
const capacityChanges:unknown[]=[],mutations:{method:string;path:string}[]=[];
function capacity(){return {state:'connected',maxBytes:capacityBytes,usedBytes:512*1024**2,reservedBytes:0,retainedBytes:0,retainedStagingBytes:0,softTargetBytes:1024**3,minimumBytes:1024**3,ceilingBytes:5*1024**3,canChange:capacityCanChange,limitSource:'operator',...(capacityCanChange?{}:{reason:'An administrator can adjust this limit.'})};}
function json(response:ServerResponse,value:unknown,status=200){if(response.destroyed)return;response.writeHead(status,{'Content-Type':'application/json'});response.end(JSON.stringify(value));}
function snapshot(period:string){
 const data=structuredClone(baseSnapshot);const report=data.cache.report;assert.ok(report);
 if(period==='7d'){
  report.hits=8;report.misses=2;
  report.netEstimatedBuildTimeSaved={milliseconds:-500,known:1,total:2,confidence:'low'};
  report.grossAvoidedTaskTime={milliseconds:90000,known:1,total:2,confidence:'low'};
  report.topArtifacts=[
   {ref:'a'.repeat(64),integration:'buildkit',hits:5,restores:1,runs:1,downloadedBytes:5120,lastHitAt:observedAt,grossAvoidedTaskTime:{milliseconds:null,known:0,total:5,confidence:'unknown'}},
   {ref:'b'.repeat(64),integration:'turbo',hits:2,restores:2,runs:2,downloadedBytes:2048,lastHitAt:observedAt,grossAvoidedTaskTime:{milliseconds:90000,known:1,total:2,confidence:'low'}},
  ];
  report.topArtifactsTruncated=true;
  report.activity=[{from:'2026-10-13T12:00:00Z',to:observedAt,runs:4,eligible:10,hits:8,misses:2}];
 }
 return data;
}
const server=createServer(async(request,response)=>{
 const url=new URL(request.url!,origin||'http://fixture');if(request.method!=='GET')mutations.push({method:request.method!,path:url.pathname});
 if(url.pathname==='/'||/^\/(projects|teams)\//.test(url.pathname)||url.pathname==='/app.js'||url.pathname==='/style.css'){
  const name=url.pathname==='/app.js'?'app.js':url.pathname==='/style.css'?'style.css':'index.html';response.writeHead(200,{'Content-Type':name.endsWith('.js')?'text/javascript':name.endsWith('.css')?'text/css':'text/html'});response.end(readFileSync(path.resolve('internal/portal/assets',name)));return;
 }
 if(url.pathname==='/api/session'){json(response,{user:{id:'fixture',login:'ux-user'},csrfToken:'synthetic-csrf',cloudOrigin:origin,authMethod:'password',passwordChangeRequired:false,canChangePassword:true,canManageGitHub:false,canCreateTeam:false,teams:[{id:'engineering',name:'Engineering',role:'admin',kind:'team',operatorManaged:true}],projects,invitations:[]});return;}
 if(url.pathname==='/api/public-config'){json(response,{githubClientId:'',passwordLogin:true});return;}
 if(url.pathname==='/api/teams/engineering/usage'){json(response,{projectCount:2,allocatedCacheBytes:10*1024**3,allocationComplete:true});return;}
 if(url.pathname==='/api/projects/phone/capacity'){
  if(request.method==='GET'){json(response,capacity());return;}
  if(request.method==='PUT'){
   assert.equal(request.headers['x-csrf-token'],'synthetic-csrf');let raw='';for await(const chunk of request)raw+=chunk;
   const body=JSON.parse(raw) as {maxBytes:number;expectedMaxBytes:number;confirm:boolean};capacityChanges.push(body);
   if(body.confirm!==true||!Number.isSafeInteger(body.maxBytes)||body.maxBytes<1024**3){json(response,{error:'Capacity must retain its minimum target'},400);return;}
   if(body.expectedMaxBytes!==capacityBytes){json(response,{error:'Capacity changed; refresh before retrying'},409);return;}
   if(body.maxBytes>5*1024**3){json(response,{error:'Capacity exceeds the configured ceiling'},403);return;}
   capacityBytes=body.maxBytes;if(failNextCapacityWrite){failNextCapacityWrite=false;json(response,{error:'Capacity update could not be confirmed'},503);return;}json(response,capacity());return;
  }
 }
 const match=/^\/api\/projects\/([^/]+)\/snapshot$/.exec(url.pathname);
 if(match){const period=url.searchParams.get('period')||'24h';snapshots.push({project:match[1]!,period,method:request.method!});if(hold){held.push({response,period});return;}json(response,snapshot(period));return;}
 json(response,{error:'Unexpected fixture request'},404);
});
await new Promise<void>(resolve=>server.listen(0,'127.0.0.1',resolve));const address=server.address();assert.ok(address&&typeof address!=='string');origin=`http://127.0.0.1:${address.port}`;
const browser=await {chromium,firefox,webkit}[engine].launch({headless:true,...(engine==='chromium'?{executablePath:process.env.CHROMIUM_PATH||undefined,args:['--no-sandbox']}:{})});
const context=await browser.newContext({viewport:{width:1440,height:1000}}),page=await context.newPage();
const errors:string[]=[],results:{name:string;passed:boolean;error?:string}[]=[];
page.on('pageerror',error=>errors.push(error.message));
await page.addInitScript(()=>{window.uxDocument=crypto.randomUUID();});
async function check(name:string,run:()=>Promise<void>){try{await run();results.push({name,passed:true});}catch(error){results.push({name,passed:false,error:error instanceof Error?error.message:String(error)});await page.screenshot({path:path.join(output,name.replace(/\W/g,'-')+'.png'),fullPage:true});throw error;}}
const summary=()=>page.getByRole('region',{name:'Project overview',exact:true});
const metric=(name:string)=>summary().locator(`[data-metric="${name}"] .stat-value`);
async function navigate(name:'Overview'|'Activity'|'Infrastructure'|'Storage'|'Settings'){await page.getByRole('navigation',{name:'Project pages'}).getByRole('link',{name,exact:true}).click();await page.waitForURL(url=>url.pathname.endsWith('/'+name.toLowerCase()));}
try{
 await check('entry opens its default project with cache metrics above setup and inventory',async()=>{
  await page.goto(origin);await summary().waitFor();await expect(metric('hit-rate')).toHaveText('70%');
  assert.equal(new URL(page.url()).pathname,'/projects/phone/overview');
  assert.equal(await page.locator('main h1').innerText(),'Phone App');
  for(const key of ['storage','hit-rate','time-saved','runs'])assert.equal(await summary().locator(`[data-metric="${key}"]`).count(),1);
  assert.match(await metric('storage').innerText(),/512\.0 MiB/);
  const bounds=await summary().boundingBox();assert.ok(bounds&&bounds.y<650,'Primary metrics must be visible near the top');
  for(const card of await summary().locator('.stat-card').all()){const box=await card.boundingBox();assert.ok(box&&box.y+box.height<800,'Every primary metric should fit above the fold on desktop');}
  assert.equal(await page.getByRole('heading',{name:'Connect your CLI',exact:true}).count(),0);
  assert.equal(await page.locator('.infra-job').count(),0,'Overview must not render every CI job');
  assert.ok(snapshots.every(item=>item.project==='phone'),'Entry eagerly requested unrelated projects');
 });
 await check('missing timing evidence stays unknown despite a known hit rate',async()=>{
  assert.match(await metric('time-saved').innerText(),/Unknown|Not measured/i);
  const evidence=page.getByRole('region',{name:'Savings evidence',exact:true});await evidence.waitFor();
  assert.match(await evidence.innerText(),/unknown|not (?:yet )?measured|unavailable/i);
  assert.doesNotMatch(await evidence.innerText(),/\$[1-9]|[1-9][0-9]*% (?:compute|cost|bill) saved/i);
 });
 await check('overview breaks down native clients without fabricating unmetered zeros',async()=>{
  const table=page.getByRole('table',{name:'Cache by type',exact:true});await table.waitFor();
  assert.match(await table.innerText(),/Docker|BuildKit/i);
  const coverage=page.getByRole('region',{name:'Cache by type',exact:true}).locator('details.integration-coverage');
  if(await coverage.count()){await coverage.locator('summary').click();assert.match(await coverage.innerText(),/Xcode/);assert.match(await coverage.innerText(),/not (?:yet )?metered|unknown/i);}
  await navigate('Activity');const xcode=page.getByRole('table',{name:'Cache by type',exact:true}).getByRole('row').filter({hasText:/Xcode/i});assert.match(await xcode.innerText(),/unknown|not (?:yet )?(?:metered|reported)|no (?:reported|measured)/i);await navigate('Overview');
  const popular=page.getByRole('region',{name:'Most reused artifacts',exact:true});await popular.waitFor();assert.match(await popular.innerText(),/unavailable|not (?:yet )?reported|no (?:artifact|per-artifact|key)/i);
 });
 await check('links preserve document identity and history reloads the focused page',async()=>{
  const identity=await page.evaluate(()=>window.uxDocument);await navigate('Settings');assert.equal(await page.evaluate(()=>window.uxDocument),identity);
  await page.getByRole('heading',{name:'Connect your CLI',exact:true}).waitFor();
  await navigate('Infrastructure');await page.locator('.infrastructure-panel').waitFor();
  await page.goBack();await page.getByRole('heading',{name:'Connect your CLI',exact:true}).waitFor();assert.equal(new URL(page.url()).pathname,'/projects/phone/settings');
  await page.goForward();await page.locator('.infrastructure-panel').waitFor();
  await page.reload();await page.locator('.infrastructure-panel').waitFor();assert.equal(new URL(page.url()).pathname,'/projects/phone/infrastructure');
 });
 await check('GitHub Actions rolls jobs into runs and preserves attempts and unknown providers',async()=>{
  const actions=page.getByRole('region',{name:'GitHub Actions',exact:true});await actions.waitFor();
  assert.equal(await actions.locator('.infra-run').count(),2);assert.equal(await actions.locator('.infra-job').count(),3);
  assert.equal(await actions.locator('.infra-run[open]').count(),0,'CI runs start collapsed');
  const first=actions.locator('.infra-run[data-run-id="456"][data-attempt="1"]');assert.equal(await first.locator('.infra-job').count(),2);
  await first.locator(':scope > summary').click();await first.locator('.infra-job > summary').first().click();assert.match(await first.innerText(),/unknown/i);assert.doesNotMatch(await first.innerText(),/Ubicloud/);
  const clients=page.getByRole('region',{name:'Persistent clients',exact:true});assert.equal(await clients.locator('.infra-client').count(),1,'Same installation should be deduplicated');
  assert.equal(await clients.locator('.infra-job').count(),0,'Transient CI jobs must not appear as persistent clients');
 });
 await check('period updates keep scroll focus controls and previous metrics while loading',async()=>{
  await navigate('Activity');await page.locator('.page-content:not([aria-busy="true"])').waitFor();
  // A realistic long table lets the regression reproduce without relying on a
  // production account's data volume or whether a viewport happens to scroll.
  await page.evaluate(()=>{const content=document.querySelector<HTMLElement>('.page-content')!;content.style.minHeight='1800px';window.uxSidebar=document.querySelector('nav[aria-label="Teams and projects"]');window.uxPeriod=document.querySelector('[aria-label="Reporting period"]');scrollTo(0,400);});
  const beforeScroll=await page.evaluate(()=>scrollY),beforeDocument=await page.evaluate(()=>window.uxDocument),before=snapshots.length;assert.ok(beforeScroll>100);
  hold=true;await page.getByLabel('Reporting period').evaluate((node:HTMLSelectElement)=>{node.focus({preventScroll:true});node.value='7d';node.dispatchEvent(new Event('change',{bubbles:true}));});
  await expect.poll(()=>held.length).toBe(1);await page.locator('.page-content[aria-busy="true"]').waitFor();
  assert.ok(Math.abs((await page.evaluate(()=>scrollY))-beforeScroll)<4,'Filter moved the scroll position while loading');
  assert.equal(await page.evaluate(()=>document.querySelector('nav[aria-label="Teams and projects"]')===window.uxSidebar),true);
  assert.equal(await page.evaluate(()=>document.querySelector('[aria-label="Reporting period"]')===window.uxPeriod),true);
  assert.equal(await page.evaluate(()=>document.activeElement===window.uxPeriod),true);
  assert.equal(await page.evaluate(()=>window.uxDocument),beforeDocument);
  assert.equal(snapshots.length,before+1);assert.equal(snapshots.at(-1)?.project,'phone');assert.equal(snapshots.at(-1)?.period,'7d');
  hold=false;for(const item of held.splice(0))json(item.response,snapshot(item.period));
  await page.locator('.page-content:not([aria-busy="true"])').waitFor();
  assert.ok(Math.abs((await page.evaluate(()=>scrollY))-beforeScroll)<4,'Filter moved the scroll position after loading');
  assert.equal(await page.evaluate(()=>document.activeElement===window.uxPeriod),true);
 });
 await check('a late filter response cannot replace the newer selected reporting period',async()=>{
  hold=true;const control=page.getByLabel('Reporting period');
  await control.selectOption('24h');await expect.poll(()=>held.length).toBe(1);
  await control.selectOption('7d');await expect.poll(()=>held.length).toBe(2);
  const older=held.shift()!,newer=held.shift()!;assert.equal(older.period,'24h');assert.equal(newer.period,'7d');hold=false;
  json(newer.response,snapshot(newer.period));await page.locator('.page-content:not([aria-busy="true"])').waitFor();
  const cache=page.getByRole('region',{name:'Team Cache',exact:true});await expect(cache.getByText('80%',{exact:true})).toBeVisible();
  json(older.response,snapshot(older.period));await page.evaluate(()=>new Promise<void>(resolve=>requestAnimationFrame(()=>requestAnimationFrame(()=>resolve()))));
  await expect(cache.getByText('80%',{exact:true})).toBeVisible();assert.equal(await control.inputValue(),'7d');assert.equal(new URL(page.url()).searchParams.get('period'),'7d');
 });
 await check('common hits expose opaque references and partial timing keeps its sign and coverage',async()=>{
  await navigate('Overview');await expect(metric('hit-rate')).toHaveText('80%');
  const table=page.getByRole('table',{name:'Most reused artifacts',exact:true});await table.waitFor();
  const rows=table.locator('tbody tr');assert.equal(await rows.count(),2);
  assert.match(await rows.nth(0).innerText(),/Artifact aaaaaaaaaa[\s\S]*Docker \/ BuildKit[\s\S]*5/);
  assert.match(await rows.nth(1).innerText(),/Artifact bbbbbbbbbb[\s\S]*Turborepo[\s\S]*2/);
  assert.match(await metric('time-saved').innerText(),/[−-]0\.5 s/);
  const evidence=page.getByRole('region',{name:'Savings evidence',exact:true});assert.match(await evidence.innerText(),/1 of 2 outcomes timed/);
  assert.match(await evidence.innerText(),/CPU-hours saved[\s\S]*Not measured/);assert.match(await evidence.innerText(),/Compute cost saved[\s\S]*Not measured/);
  assert.equal(await page.getByRole('region',{name:'Cache activity',exact:true}).getByRole('progressbar').count(),2);
 });
 await check('capacity changes stay inside the allocation without pruning artifacts',async()=>{
  await navigate('Storage');const capacity=page.getByRole('region',{name:'Cache capacity',exact:true});
  await capacity.getByLabel('Cache limit in GiB').waitFor();const start=capacityChanges.length;
  await capacity.getByLabel('Cache limit in GiB').fill('6');await capacity.getByRole('button',{name:'Save cache limit',exact:true}).click();
  await capacity.getByText('Choose a limit between the minimum and your assigned allocation.',{exact:true}).waitFor();assert.equal(capacityChanges.length,start);
  await capacity.getByLabel('Cache limit in GiB').fill('4');await capacity.getByRole('button',{name:'Save cache limit',exact:true}).click();
  await page.getByText('Cache limit updated.',{exact:true}).waitFor();await expect(capacity.getByLabel('Cache limit in GiB')).toHaveValue('4');
  assert.deepEqual(capacityChanges.at(-1),{maxBytes:4*1024**3,expectedMaxBytes:5*1024**3,confirm:true});
  assert.equal(capacityChanges.length,start+1);assert.deepEqual(mutations,[{method:'PUT',path:'/api/projects/phone/capacity'}],'Changing capacity must not initiate a prune or another mutation');
 });
 await check('storage breakdown uses reported deduplicated bytes rather than transfer totals',async()=>{
  const table=page.getByRole('table',{name:'Stored data by type',exact:true});await table.waitFor();
  const turbo=table.getByRole('row').filter({hasText:'Turbo-protocol archives'});assert.match(await turbo.innerText(),/256\.0 MiB/);
  const shared=table.getByRole('row').filter({hasText:'Shared across integrations'});assert.match(await shared.innerText(),/128\.0 MiB/);
  assert.doesNotMatch(await table.innerText(),/7\.0 KiB|5\.0 KiB|2\.0 KiB/,'Transfer byte totals must not masquerade as cached storage');
 });
 await check('an unconfirmed capacity write reconciles before another write is offered',async()=>{
  const capacity=page.getByRole('region',{name:'Cache capacity',exact:true}),before=capacityChanges.length;failNextCapacityWrite=true;
  await capacity.getByLabel('Cache limit in GiB').fill('2');await capacity.getByRole('button',{name:'Save cache limit',exact:true}).click();
  await capacity.getByText('Capacity update could not be confirmed',{exact:false}).waitFor();
  await expect(capacity.getByRole('button',{name:'Save cache limit',exact:true})).toBeDisabled();
  assert.equal(capacityChanges.length,before+1,'An uncertain mutation must not be retried automatically');
  await capacity.getByRole('button',{name:'Reload capacity',exact:true}).click();
  await expect(capacity.getByLabel('Cache limit in GiB')).toHaveValue('2');await expect(capacity.getByRole('button',{name:'Save cache limit',exact:true})).toBeEnabled();
  assert.equal(capacityChanges.length,before+1);
 });
 await check('read-only capacity state exposes limits without mutation controls',async()=>{
  capacityCanChange=false;await page.getByRole('button',{name:'Refresh',exact:true}).click();
  const capacity=page.getByRole('region',{name:'Cache capacity',exact:true});await capacity.getByText('An administrator can adjust this limit.',{exact:true}).waitFor();
  assert.equal(await capacity.getByRole('button',{name:'Save cache limit',exact:true}).count(),0);capacityCanChange=true;
 });
 await check('explicit project deep links win over the default project',async()=>{
  await page.goto(origin+'/projects/library/overview');await summary().waitFor();assert.equal(await page.locator('main h1').innerText(),'Library');
  await page.reload();await summary().waitFor();assert.equal(new URL(page.url()).pathname,'/projects/library/overview');
  assert.equal(await page.locator('main h1').innerText(),'Library');
  await page.goto(origin);await summary().waitFor();assert.equal(await page.locator('main h1').innerText(),'Library');assert.equal(new URL(page.url()).pathname,'/projects/library/overview','A saved authorized project should override the initial project order');
 });
 await check('project identities with slashes survive a direct link and reload',async()=>{
  const target='/projects/'+encodeURIComponent('github.com/example/shared')+'/overview';
  await page.goto(origin+target);await summary().waitFor();assert.equal(await page.locator('main h1').innerText(),'Shared library');
  await page.reload();await summary().waitFor();assert.equal(new URL(page.url()).pathname,target);assert.equal(await page.locator('main h1').innerText(),'Shared library');
 });
 await page.goto(origin+'/projects/phone/overview');await summary().waitFor();
 for(const width of [320,390,768,1440])await check(`project pages fit viewport ${width}`,async()=>{
  await page.setViewportSize({width,height:900});await navigate('Overview');await summary().waitFor();
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  await expect(page.getByRole('link',{name:'Account & team',exact:true})).toBeVisible();
  await page.screenshot({path:path.join(output,`overview-${width}.png`),fullPage:true});
 });
 await check('no browser exceptions',async()=>assert.deepEqual(errors,[]));
}catch(error){console.error(error);process.exitCode=1;}
finally{writeFileSync(path.join(output,'results.json'),JSON.stringify({browser:engine,results,pageErrors:errors,syntheticAPIs:true},null,2)+'\n');console.log(JSON.stringify({browser:engine,projectUXPassed:results.filter(result=>result.passed).length,projectUXTotal:results.length,output}));await context.close();await browser.close();server.closeAllConnections();await new Promise<void>(resolve=>server.close(()=>resolve()));}
