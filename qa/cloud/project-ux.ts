// Exercise page hierarchy, browser navigation and in-place updates against the
// shipped portal bundle. The synthetic metrics qualify presentation only.
// QA_PROJECT_NATIVE_FAILURE_ONLY=1 selects the mixed-report and outage controls.
import {chromium, firefox, webkit, expect} from '@playwright/test';
import assert from 'node:assert/strict';
import {createServer, type ServerResponse} from 'node:http';
import {readFileSync, mkdirSync, mkdtempSync, writeFileSync} from 'node:fs';
import path from 'node:path';
import type {ExecutionEntry, ExecutionInventory} from '../../dashboard/execution-view.js';
import type {Snapshot} from '../../cloud/types.js';
import type {NativeIntegrationReport} from '../../dashboard/cache-view.js';
import {operationFixture} from '../dashboard/operation-fixtures.ts';
import type {OperationPeriodReport} from '../../dashboard/operation-view.js';

declare global {interface Window {uxDocument?: string; uxSidebar?: Element | null; uxPeriod?: Element | null;}}
const engine=process.env.DASHBOARD_BROWSER||'chromium';
if(engine!=='chromium'&&engine!=='firefox'&&engine!=='webkit')throw new Error('Invalid DASHBOARD_BROWSER');
const output=process.env.QA_OUTPUT?path.join(process.env.QA_OUTPUT,'project-ux'):mkdtempSync('/tmp/layercache-project-ux-');mkdirSync(output,{recursive:true});
const projects=[
 {id:'phone',teamId:'engineering',name:'Phone App',repository:'example/phone-app',role:'admin',operatorManaged:true},
 {id:'library',teamId:'engineering',name:'Library',repository:'example/library',role:'reader',operatorManaged:true},
 {id:'github.com/example/shared',teamId:'engineering',name:'Shared library',repository:'example/shared',role:'reader',operatorManaged:true},
];
const observedAt=new Date().toISOString();
const priorObservation=new Date(Date.now()-60_000).toISOString(),staleObservation=new Date(Date.now()-48*60*60*1000).toISOString();
function entry(id:string,executionId:string,kind:'client'|'githubActions',installationId?:string):ExecutionEntry{return {id,ownerId:'github-id:1',installationId,executionId,kind,provider:'unknown',providerEvidence:'unknown',runnerEnvironment:kind==='githubActions'?'self-hosted':'unknown',lastObservedAt:observedAt,phase:'finish',cacheActivity:'observed',sample:{os:'linux',arch:'amd64',cpu:{visibleLogical:8,affinityLogical:2,quotaMilliCPU:2000,effectiveMilliCPU:2000},memory:{visibleTotalBytes:16*1024**3,cgroupLimitBytes:4*1024**3,effectiveLimitBytes:4*1024**3},filesystems:[{id:'volume-a',roles:['workspace','staging'],capacityBytes:100*1024**3,freeBytes:40*1024**3,availableBytes:35*1024**3}]}};}
function client(id:string,executionId:string,installationId:string,observedExecutions?:number,lastObservedAt=observedAt,phase:'start'|'finish'='finish'):ExecutionEntry{return {...entry(id,executionId,'client',installationId),...(observedExecutions===undefined?{}:{observedExecutions}),lastObservedAt,phase};}
const executions:ExecutionInventory={version:1,from:new Date(Date.now()-24*60*60*1000).toISOString(),to:observedAt,updatedAt:observedAt,retentionDays:30,staleAfterSeconds:86400,historyTruncated:false,installations:[
 client('client-1','client:first','stable-installation',2),client('client-2','client:second','stable-installation',2,priorObservation),
 client('one-off','client:one-off','single-installation',1),client('unknown-count','client:unknown','unknown-installation'),
 client('stale-client','client:stale','stale-installation',9,staleObservation),
 client('phase-start','client:same-execution','phase-installation',1,priorObservation,'start'),client('phase-finish','client:same-execution','phase-installation',1),
],jobs:[entry('job-1','github-actions:123:456:1:789','githubActions'),entry('job-2','github-actions:123:456:1:790','githubActions'),entry('job-3','github-actions:123:456:2:791','githubActions')]};
const baseSnapshot:Snapshot={scope:'team',updatedAt:observedAt,cache:{state:'connected',reportState:'available',status:{usageBytes:512*1024**2,maxBytes:5*1024**3,artifacts:32,entries:32,role:'admin',visibility:'team',storageBackend:'s3',canPrune:false,storageByIntegration:[{integration:'turbo',usageBytes:256*1024**2,artifacts:16,entries:16},{integration:'actions',usageBytes:128*1024**2,artifacts:8,entries:8},{integration:'shared',usageBytes:128*1024**2,artifacts:8,entries:8}]},report:{eligible:10,hits:7,misses:3,runs:4,degraded:false,netEstimatedBuildTimeSaved:{milliseconds:null,known:0,total:4,confidence:'unknown'},bytes:{downloaded:7168,uploaded:2048},timing:{lookupMs:10,downloadMs:200,verificationMs:30,restoreMs:400,uploadMs:50},sources:[{source:'teamCache',hits:7,downloadedBytes:7168,uploadedBytes:2048}],integrations:[{integration:'buildkit',hits:5,eligible:6},{integration:'turbo',hits:2,eligible:4,grossAvoidedTaskTime:{milliseconds:null,known:0,total:2,confidence:'unknown'}}]}},executions};
function nativeReport(integration:'gradle'|'xcode'):NativeIntegrationReport{return {integration,reportedInvocations:1,reportedTasks:0,outcomes:{executed:0,fromCache:0,upToDate:0,noSource:0,skipped:0,failed:0,unknown:0},knownEligible:0,eligibilityUnknown:0,eligible:null,hits:0,misses:0,hitRate:null,sources:[],producerTiming:{known:0,total:0},producerResources:{known:0,total:0},grossAvoidedTaskTime:{milliseconds:null,known:0,total:0,confidence:'unknown'},netEstimatedBuildTimeSaved:{milliseconds:null,known:0,total:0,confidence:'unknown'},allocatedCpuSecondsSaved:null,measuredCpuSecondsSaved:null,costSaved:null,historyTruncated:false};}
const gradle:NativeIntegrationReport={...nativeReport('gradle'),reportedTasks:8,outcomes:{executed:2,fromCache:3,upToDate:1,noSource:1,skipped:1,failed:0,unknown:0},knownEligible:4,eligibilityUnknown:1,hits:3,misses:1,sources:[{source:'teamCache',hits:1},{source:'unattributed',hits:2}],producerTiming:{known:1,total:3},producerResources:{known:0,total:3},grossAvoidedTaskTime:{milliseconds:9000,known:1,total:3,confidence:'low'},netAvoidedTaskTime:{milliseconds:-500,known:1,total:3,confidence:'low'}};
const xcode:NativeIntegrationReport={...nativeReport('xcode'),counters:{rpc:{actionReads:12,actionHits:9,actionWrites:2,casReads:30,casHits:24,casWrites:4,cacheErrors:0},compiler:{scope:'unknown',coverage:'unsupported',reason:'unsupported',hits:null,misses:null,skipped:null,unknown:null}},timingMs:{operation:5030,compile:5000,load:null,restore:0,close:20,snapshot:null}};
assert.ok(baseSnapshot.cache.report);baseSnapshot.cache.report.nativeReports=[gradle,xcode];
let origin='';
const snapshots:{project:string;period:string;method:string}[]=[],held:{response:ServerResponse;period:string}[]=[];
let hold=false,capacityBytes=5*1024**3,failNextCapacityWrite=false,capacityCanChange=true,largeInfrastructure=false,signedIn=true,alternateAccount=false,completeCompiler=false,mixedTimedTurbo=false,nativeReportsUnavailable=false;
let operationReports:OperationPeriodReport|undefined,operationReportsUnavailable=false;
const capacityChanges:unknown[]=[],mutations:{method:string;path:string}[]=[];
function capacity(){return {state:'connected',maxBytes:capacityBytes,usedBytes:512*1024**2,reservedBytes:0,retainedBytes:0,retainedStagingBytes:0,softTargetBytes:1024**3,minimumBytes:1024**3,ceilingBytes:5*1024**3,canChange:capacityCanChange,limitSource:'operator',...(capacityCanChange?{}:{reason:'An administrator can adjust this limit.'})};}
function json(response:ServerResponse,value:unknown,status=200){if(response.destroyed)return;response.writeHead(status,{'Content-Type':'application/json'});response.end(JSON.stringify(value));}
function snapshot(period:string){
 const data=structuredClone(baseSnapshot);const report=data.cache.report;assert.ok(report);
 if(period==='7d'){
  report.hits=8;report.misses=2;
  report.nativeReports=[{...structuredClone(gradle),knownEligible:5,eligibilityUnknown:0,eligible:5,hits:3,misses:2,hitRate:0.6,sources:[{source:'teamCache',hits:2},{source:'localCache',hits:1}],producerTiming:{known:2,total:3},producerResources:{known:0,total:3},grossAvoidedTaskTime:{milliseconds:9000,known:2,total:3,confidence:'low'},netAvoidedTaskTime:{milliseconds:8500,known:2,total:3,confidence:'low'}},{...structuredClone(xcode),counters:{...xcode.counters,compiler:{scope:'clang-pch',coverage:'partial',reason:'ambiguous',hits:4,misses:2,skipped:null,unknown:1}}}];
  report.netEstimatedBuildTimeSaved={milliseconds:-500,known:1,total:2,confidence:'low'};
  report.grossAvoidedTaskTime={milliseconds:90000,known:1,total:2,confidence:'low'};
  report.topArtifacts=[
   {ref:'a'.repeat(64),integration:'buildkit',hits:5,restores:1,runs:1,downloadedBytes:5120,lastHitAt:observedAt,grossAvoidedTaskTime:{milliseconds:null,known:0,total:5,confidence:'unknown'}},
   {ref:'b'.repeat(64),integration:'turbo',hits:2,restores:2,runs:2,downloadedBytes:2048,lastHitAt:observedAt,grossAvoidedTaskTime:{milliseconds:90000,known:1,total:2,confidence:'low'}},
  ];
  if(completeCompiler){const nativeXcode=report.nativeReports?.find(item=>item.integration==='xcode');assert.ok(nativeXcode?.counters);nativeXcode.counters.compiler={scope:'clang-pch',coverage:'complete',reason:'none',hits:4,misses:2,skipped:null,unknown:0};}
  report.topArtifactsTruncated=true;
  report.activity=[{from:'2026-10-13T12:00:00Z',to:observedAt,runs:4,eligible:10,hits:8,misses:2}];
  if(largeInfrastructure){assert.ok(data.executions);data.executions.jobs=Array.from({length:45},(_,index)=>entry(`large-job-${index}`,`github-actions:123:${1000+index}:1:${2000+index}`,'githubActions'));}
 }
 if(mixedTimedTurbo){report.eligible=4;report.hits=2;report.misses=2;report.runs=1;report.integrations=[{integration:'turbo',eligible:4,hits:2,grossAvoidedTaskTime:{milliseconds:180000,known:2,total:2,confidence:'high'}}];report.grossAvoidedTaskTime={milliseconds:180000,known:2,total:2,confidence:'high'};report.netEstimatedBuildTimeSaved={milliseconds:120000,known:1,total:1,confidence:'high'};report.nativeReports=[structuredClone(xcode)];}
 if(nativeReportsUnavailable){report.nativeReportsUnavailable=true;delete report.nativeReports;}
 if(operationReports)report.operationReports=structuredClone(operationReports);
 if(operationReportsUnavailable){report.operationReportsUnavailable=true;delete report.operationReports;}
 return data;
}
const server=createServer(async(request,response)=>{
 const url=new URL(request.url!,origin||'http://fixture');if(request.method!=='GET')mutations.push({method:request.method!,path:url.pathname});
 if(url.pathname==='/'||/^\/(projects|teams)\//.test(url.pathname)||url.pathname==='/app.js'||url.pathname==='/style.css'){
  const name=url.pathname==='/app.js'?'app.js':url.pathname==='/style.css'?'style.css':'index.html';response.writeHead(200,{'Content-Type':name.endsWith('.js')?'text/javascript':name.endsWith('.css')?'text/css':'text/html'});response.end(readFileSync(path.resolve('internal/portal/assets',name)));return;
 }
 if(url.pathname==='/api/logout'&&request.method==='POST'){signedIn=false;json(response,undefined,204);return;}
 if(url.pathname==='/auth/password'&&request.method==='POST'){let raw='';for await(const chunk of request)raw+=chunk;const body=JSON.parse(raw);assert.equal(body.email,'alternate@example.com');assert.equal(body.password,'synthetic-password');alternateAccount=true;signedIn=true;json(response,undefined,204);return;}
 if(url.pathname==='/api/session'){if(!signedIn){json(response,{error:'Unauthorized'},401);return;}json(response,{user:alternateAccount?{id:'second-fixture',login:'alternate-user'}:{id:'fixture',login:'ux-user'},csrfToken:'synthetic-csrf',cloudOrigin:origin,authMethod:'password',passwordChangeRequired:false,canChangePassword:true,canManageGitHub:false,canCreateTeam:false,teams:alternateAccount?[{id:'alternate-team',name:'Another team',role:'reader',kind:'team',operatorManaged:true}]:[{id:'engineering',name:'Engineering',role:'admin',kind:'team',operatorManaged:true}],projects:alternateAccount?[{id:'alternate',teamId:'alternate-team',name:'Another project',repository:'example/alternate',role:'reader',operatorManaged:true}]:projects,invitations:[]});return;}
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
const nativeFailureOnly=process.env.QA_PROJECT_NATIVE_FAILURE_ONLY==='1',operationsOnly=process.env.QA_PROJECT_OPERATIONS_ONLY==='1',focusedChecks=new Set(operationsOnly?['operation overview stays concise and Activity shows the same complete pair once','operation refresh preserves the page and unknown phases without savings credit','operation reporting outage preserves native and generic metrics','no browser exceptions']:['mixed timed Turbo and Xcode evidence preserves the scoped generic build estimate','native reporting outage preserves generic metrics without claiming absent native use','no browser exceptions']);
page.on('pageerror',error=>errors.push(error.message));
await page.addInitScript(()=>{window.uxDocument=crypto.randomUUID();});
async function check(name:string,run:()=>Promise<void>){if((nativeFailureOnly||operationsOnly)&&!focusedChecks.has(name))return;try{await run();results.push({name,passed:true});}catch(error){results.push({name,passed:false,error:error instanceof Error?error.message:String(error)});await page.screenshot({path:path.join(output,name.replace(/\W/g,'-')+'.png'),fullPage:true});throw error;}}
const summary=()=>page.getByRole('region',{name:'Project overview',exact:true});
const metric=(name:string)=>summary().locator(`[data-metric="${name}"] .stat-value`);
async function navigate(name:'Overview'|'Activity'|'Infrastructure'|'Storage'|'Settings'){await page.getByRole('navigation',{name:'Project pages'}).getByRole('link',{name,exact:true}).click();await page.waitForURL(url=>url.pathname.endsWith('/'+name.toLowerCase()));}
try{
 if(nativeFailureOnly||operationsOnly){await page.goto(origin+'/projects/phone/overview?period=7d');await summary().waitFor();}
 await check('operation overview stays concise and Activity shows the same complete pair once',async()=>{
  operationReports=operationFixture();await page.goto(origin+'/projects/phone/overview?period=7d');await summary().waitFor();
  const overview=summary().getByRole('region',{name:'Operation timings',exact:true});await expect(overview).toBeVisible();assert.equal(await overview.locator('.operation-observation').count(),0);assert.match(await overview.innerText(),/Latest Gradle operation[\s\S]*1\.3 min[\s\S]*20\.0 s · shorter/);await expect(metric('hit-rate')).toHaveText('80%');
  await navigate('Activity');await expect(page.getByRole('region',{name:'Operation timings',exact:true})).toHaveCount(1);const section=page.getByRole('region',{name:'Operation timings',exact:true});await section.locator('.operation-observation summary').first().click();assert.match(await section.innerText(),/Baseline complete operation[\s\S]*1\.7 min[\s\S]*Current complete operation[\s\S]*1\.3 min/);
  assert.match(await section.innerText(),/Observed elapsed difference[\s\S]*20\.0 s · shorter/);assert.match(await page.getByRole('region',{name:'Gradle task outcomes',exact:true}).innerText(),/3 native cache hits/);
 });
 await check('operation refresh preserves the page and unknown phases without savings credit',async()=>{
  assert.ok(operationReports);operationReports.observations[0]!.report.phaseTimingMs.restore=null;operationReports.comparisons[0]!.observedOverheadDifferenceMs=null;
  const documentId=await page.evaluate(()=>window.uxDocument);await page.getByRole('button',{name:'Refresh',exact:true}).click();await page.locator('.page-content:not([aria-busy="true"])').waitFor();const section=page.getByRole('region',{name:'Operation timings',exact:true});await expect(section.locator('.operation-observation').first().locator('summary')).toContainText('20.0 s · shorter');await section.locator('.operation-observation summary').first().click();
  assert.deepEqual(await section.getByRole('table',{name:'Operation phase timings'}).first().locator('tr[data-phase="restore"] td').allTextContents(),['0.0 s','Unknown']);assert.match(await section.innerText(),/Observed cache-phase difference[\s\S]*Unknown/);assert.equal(await page.evaluate(()=>window.uxDocument),documentId);
  assert.match(await section.locator('.operation-no-total').innerText(),/Pairs are not added together/);for(const width of [320,1440]){await page.setViewportSize({width,height:1000});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);}
 });
 await check('operation reporting outage preserves native and generic metrics',async()=>{
  operationReportsUnavailable=true;await navigate('Overview');await page.getByRole('button',{name:'Refresh',exact:true}).click();await expect(summary().getByRole('region',{name:'Operation timings',exact:true})).toContainText('Operation reporting unavailable');await expect(metric('hit-rate')).toHaveText('80%');await expect(metric('time-saved')).toHaveText('−0.5 s');
  await navigate('Activity');await expect(page.getByRole('region',{name:'Operation timings',exact:true})).toContainText('Other cache and native results remain available');assert.equal(await page.getByRole('region',{name:'Gradle task outcomes',exact:true}).count(),1);operationReports=undefined;operationReportsUnavailable=false;
 });
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
  if(await coverage.count()){await coverage.locator('summary').click();assert.match(await coverage.innerText(),/Xcode/);assert.match(await coverage.innerText(),/protocol|compiler verdict|unavailable/i);await expect(coverage.getByRole('link',{name:'Xcode setup',exact:true})).toHaveAttribute('href','https://github.com/ziahamza/layercache/blob/main/docs/native-metering.md#xcode');}
  await navigate('Activity');const xcode=page.getByRole('table',{name:'Cache by type',exact:true}).getByRole('row').filter({hasText:/Xcode/i});assert.match(await xcode.innerText(),/protocol activity only|unknown|not measured/i);await navigate('Overview');
  const popular=page.getByRole('region',{name:'Most reused artifacts',exact:true});await popular.waitFor();assert.match(await popular.innerText(),/unavailable|not (?:yet )?reported|no (?:artifact|per-artifact|key)/i);
 });
 await check('native task reuse remains visible when eligibility and savings evidence are incomplete',async()=>{
  await navigate('Activity');
  const row=page.getByRole('table',{name:'Cache by type',exact:true}).locator('tr[data-integration="gradle"]');
  assert.match(await row.innerText(),/tasks[\s\S]*Not measured[\s\S]*3 \/ Unknown/);assert.equal(await row.getByRole('progressbar').count(),0,'Unknown eligibility must not draw a zero-percent meter');
  assert.match(await row.locator('td').last().innerText(),/9\.0 s[\s\S]*1\/3 timed/);const untimed=page.getByRole('table',{name:'Cache by type',exact:true}).locator('tr[data-integration="turbo"]');assert.match(await untimed.locator('td').last().innerText(),/Unknown[\s\S]*0\/2 timed/);
  const native=page.getByRole('region',{name:'Gradle task outcomes',exact:true});await native.waitFor();
  assert.deepEqual(await native.getByRole('table',{name:'Gradle task outcomes',exact:true}).locator('tbody td').allTextContents(),['2','3','1','1','1','0','0']);
  assert.match(await native.innerText(),/3 native cache hits[\s\S]*4 known eligible tasks[\s\S]*1 outcomes with unknown eligibility/);
  assert.match(await native.innerText(),/Team Cache 1[\s\S]*Unattributed 2/);assert.match(await native.innerText(),/producer timing matched for 1 of 3[\s\S]*producer resources matched for 0 of 3/i);assert.match(await native.innerText(),/Net avoided task elapsed time: [−-]0\.5 s[\s\S]*1 of 3 outcomes timed/);
  const savings=page.getByRole('region',{name:'Savings evidence',exact:true});assert.match(await savings.innerText(),/Current host capacity is not saved CPU usage/);assert.match(await savings.innerText(),/provider prices and billing rules/);
  await navigate('Overview');await expect(metric('hit-rate')).toHaveText('70%');assert.match(await summary().locator('[data-metric="hit-rate"] .stat-label').innerText(),/Reported cache hit rate/);
 });
 await check('Xcode protocol requests never fabricate compiler replay and missing phase timings stay unknown',async()=>{
  await navigate('Activity');
  const protocol=page.getByRole('table',{name:'Xcode protocol activity',exact:true});await protocol.waitFor();
  assert.deepEqual(await protocol.locator('tbody tr').nth(0).locator('td').allTextContents(),['12','9','2']);
  assert.deepEqual(await protocol.locator('tbody tr').nth(1).locator('td').allTextContents(),['30','24','4']);
  const row=page.getByRole('table',{name:'Cache by type',exact:true}).locator('tr[data-integration="xcode"]');assert.match(await row.innerText(),/Protocol activity only/);assert.equal(await row.getByRole('progressbar').count(),0);
  const compiler=page.getByRole('table',{name:'Xcode compiler verdicts',exact:true});assert.deepEqual(await compiler.locator('tbody td').allTextContents(),['Unknown','Unknown','Unknown','Unknown']);
  const phases=page.getByRole('table',{name:'Xcode lifecycle timings',exact:true});assert.match(await phases.getByRole('row').filter({hasText:'Cache load'}).innerText(),/Unknown/);assert.match(await phases.getByRole('row').filter({hasText:'Snapshot restore'}).innerText(),/0\.0 s/);
  const evidence=page.getByRole('region',{name:'Xcode protocol and compiler evidence',exact:true});assert.match(await evidence.innerText(),/not compiler actions avoided or snapshot restores/);assert.match(await evidence.innerText(),/compiler result format is not supported/);
  await navigate('Overview');await expect(metric('hit-rate')).toHaveText('70%');
 });
 await check('settings and account pages hide project reporting controls and ignore hidden filter events',async()=>{
  await navigate('Settings');await page.getByRole('heading',{name:'Connect your CLI',exact:true}).waitFor();
  await expect(page.locator('.project-toolbar')).toBeHidden();await expect(page.getByLabel('Reporting period')).toBeHidden();
  const before=snapshots.length,settingsURL=page.url();
  await page.getByLabel('Reporting period').evaluate((node:HTMLSelectElement)=>{node.value='7d';node.dispatchEvent(new Event('change',{bubbles:true}));});
  await page.evaluate(()=>new Promise<void>(resolve=>requestAnimationFrame(()=>requestAnimationFrame(()=>resolve()))));
  await page.getByRole('heading',{name:'Connect your CLI',exact:true}).waitFor();assert.equal(page.url(),settingsURL);assert.equal(snapshots.length,before);
  await page.getByRole('link',{name:'Account & team',exact:true}).click();await page.waitForURL(url=>url.pathname==='/teams/engineering/settings');
  await expect(page.locator('.project-tabs')).toBeHidden();await expect(page.locator('.project-toolbar')).toBeHidden();
  await page.goto(origin+'/projects/unknown/overview');await page.getByRole('heading',{name:'Page unavailable',exact:true}).waitFor();
  await expect(page.locator('.project-tabs')).toBeHidden();await expect(page.locator('.project-toolbar')).toBeHidden();
  await page.getByRole('button',{name:'Phone App',exact:true}).click();await summary().waitFor();await expect(page.locator('.project-toolbar')).toBeVisible();
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
  const clients=page.getByRole('region',{name:'Persistent clients',exact:true});assert.equal(await clients.locator('.infra-client:visible').count(),1,'Only the fresh repeated installation should appear by default');
  assert.equal(await clients.locator('.infra-job').count(),0,'Transient CI jobs must not appear as persistent clients');
 });
 await check('one-off unknown and stale clients stay out of the repeated-client default',async()=>{
  const clients=page.getByRole('region',{name:'Persistent clients',exact:true}),other=page.locator('details.infra-other-clients');
  assert.equal(await clients.locator('.infra-client:visible').count(),1);assert.equal(await other.getAttribute('open'),null);
  const stable=clients.locator('.infra-client:visible');assert.match(await stable.locator(':scope > summary').innerText(),/2\+ executions/);assert.match(await stable.locator('.infra-client-recurrence').textContent()??'',/At least 2 reported executions/i);
  await other.locator(':scope > summary').click();assert.equal(await other.locator('.infra-client').count(),4,'Duplicate phases must remain a single installation');
  const text=await other.textContent()??'';assert.match(text,/At least 1 reported execution/i);assert.match(text,/9 reported executions/i);assert.match(text,/unknown|unavailable|not (?:yet )?reported/i);
  assert.equal(await page.locator('.infra-client').count(),5,'Installation rows should deduplicate without promoting two phase rows');
  await other.locator(':scope > summary').click();assert.equal(await clients.locator('.infra-client:visible').count(),1);
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
 await check('scoped compiler verdicts and complete native eligibility remain distinct from global archive totals',async()=>{
  await navigate('Activity');const table=page.getByRole('table',{name:'Cache by type',exact:true});
  const gradle=table.locator('tr[data-integration="gradle"]');assert.match(await gradle.innerText(),/60%[\s\S]*3 \/ 5/);await expect(gradle.getByRole('progressbar')).toHaveAttribute('value','0.6');
  const xcode=table.locator('tr[data-integration="xcode"]');assert.match(await xcode.innerText(),/compiler verdicts[\s\S]*Not measured[\s\S]*4 \/ Unknown/);assert.equal(await xcode.getByRole('progressbar').count(),0);
  const compiler=page.getByRole('region',{name:'Xcode compiler verdicts',exact:true});assert.match(await compiler.innerText(),/Clang \/ PCH[\s\S]*partial coverage/);assert.deepEqual(await compiler.getByRole('table',{name:'Xcode compiler verdicts',exact:true}).locator('tbody td').allTextContents(),['4','2','Unknown','1']);
  const native=page.getByRole('region',{name:'Gradle task outcomes',exact:true});assert.match(await native.innerText(),/Team Cache 2[\s\S]*Local Cache 1/);
  await navigate('Overview');await expect(metric('hit-rate')).toHaveText('80%');assert.equal(await page.getByRole('table',{name:'Most reused artifacts',exact:true}).locator('tbody tr').count(),2);
 });
 await check('complete compiler verdict ratios remain scoped and cannot change task hit rates or savings',async()=>{
  completeCompiler=true;await navigate('Activity');await page.getByRole('button',{name:'Refresh',exact:true}).click();
  const compiler=page.getByRole('region',{name:'Xcode compiler verdicts',exact:true});await expect(compiler.getByText(/Scoped compiler-verdict hit rate: 67%/)).toBeVisible();assert.match(await compiler.innerText(),/4 hits \/ 6 classified compiler verdicts/);
  const xcode=page.getByRole('table',{name:'Cache by type',exact:true}).locator('tr[data-integration="xcode"]');assert.match(await xcode.innerText(),/Not measured[\s\S]*4 \/ Unknown/);assert.equal(await xcode.getByRole('progressbar').count(),0);
  const savings=page.getByRole('region',{name:'Savings evidence',exact:true});assert.match(await savings.innerText(),/CPU-hours saved[\s\S]*Not measured/);assert.match(await savings.innerText(),/Compute cost saved[\s\S]*Not measured/);
  await navigate('Overview');await expect(metric('hit-rate')).toHaveText('80%');completeCompiler=false;
 });
 await check('mixed timed Turbo and Xcode evidence preserves the scoped generic build estimate',async()=>{
  mixedTimedTurbo=true;await navigate('Overview');await page.getByRole('button',{name:'Refresh',exact:true}).click();await expect(metric('time-saved')).toHaveText('2.0 min');await expect(metric('hit-rate')).toHaveText('50%');
  assert.match(await summary().locator('[data-metric="time-saved"] .stat-detail').innerText(),/1 of 1 outcomes timed[\s\S]*Turbo, Actions and BuildKit reported graphs/);
  await navigate('Activity');const savings=page.getByRole('region',{name:'Savings evidence',exact:true});assert.match(await savings.innerText(),/Estimated build time saved[\s\S]*Turbo, Actions and BuildKit reported graphs[\s\S]*2\.0 min/);
  const xcode=page.getByRole('table',{name:'Xcode protocol activity',exact:true});assert.deepEqual(await xcode.locator('tbody tr').first().locator('td').allTextContents(),['12','9','2']);
  assert.match(await savings.innerText(),/Native Gradle and Xcode reports are shown separately/);assert.match(await savings.innerText(),/CPU-hours saved[\s\S]*Not measured/);
 });
 await check('native reporting outage preserves generic metrics without claiming absent native use',async()=>{
  nativeReportsUnavailable=true;await navigate('Overview');await page.getByRole('button',{name:'Refresh',exact:true}).click();await expect(metric('time-saved')).toHaveText('2.0 min');await expect(metric('hit-rate')).toHaveText('50%');
  const types=page.getByRole('region',{name:'Cache by type',exact:true});await expect(types.getByText('Native reporting is unavailable. Turbo, Actions and BuildKit data is still shown.',{exact:true})).toBeVisible();
  const details=types.locator('details.integration-coverage');await details.locator(':scope > summary').click();
  for(const id of ['gradle','xcode']){const row=details.getByRole('table',{name:'Reporting coverage',exact:true}).locator(`tr[data-integration="${id}"]`);assert.match(await row.innerText(),/Reporting unavailable[\s\S]*could not be read[\s\S]*Refresh project activity/);assert.doesNotMatch(await row.innerText(),/No reports this period/);}
  await navigate('Activity');const table=page.getByRole('table',{name:'Cache by type',exact:true});
  for(const id of ['gradle','xcode']){const row=table.locator(`tr[data-integration="${id}"]`);assert.match(await row.innerText(),/Reporting unavailable[\s\S]*Not measured/);assert.equal(await row.getByRole('progressbar').count(),0);}
  assert.match(await table.locator('tr[data-integration="turbo"]').innerText(),/50%[\s\S]*2 \/ 4/);assert.equal(await page.getByRole('region',{name:'Native outcome evidence',exact:true}).count(),0);assert.equal(await page.getByRole('region',{name:'Xcode protocol and compiler evidence',exact:true}).count(),0);
  const savings=page.getByRole('region',{name:'Savings evidence',exact:true});assert.match(await savings.innerText(),/Estimated build time saved[\s\S]*Turbo, Actions and BuildKit reported graphs[\s\S]*2\.0 min/);
  mixedTimedTurbo=false;nativeReportsUnavailable=false;await page.getByRole('button',{name:'Refresh',exact:true}).click();await expect(page.getByRole('region',{name:'Team Cache',exact:true}).getByText('80%',{exact:true})).toBeVisible();
 });
 await check('a shorter report at the top removes the previous full-page height',async()=>{
  largeInfrastructure=true;await navigate('Infrastructure');await page.locator('.page-content:not([aria-busy="true"])').waitFor();
  assert.equal(await page.locator('.infra-run').count(),45);await page.evaluate(()=>scrollTo(0,0));
  const longHeight=await page.locator('.page-content').evaluate(node=>node.getBoundingClientRect().height),before=snapshots.length;
  await page.getByLabel('Reporting period').selectOption('24h');await page.locator('.page-content:not([aria-busy="true"])').waitFor();
  assert.equal(await page.locator('.infra-run').count(),2);assert.equal(snapshots.length,before+1);
  const shortHeight=await page.locator('.page-content').evaluate(node=>node.getBoundingClientRect().height);
  assert.ok(shortHeight<longHeight-500,'A shorter report retained the previous full content height');
  assert.equal(await page.evaluate(()=>scrollY),0);assert.equal(await page.locator('.page-content').evaluate(node=>Number.parseFloat((node as HTMLElement).style.minHeight)||0),0,'No scroll reservation is needed at the top');
 });
 await check('a shrinking report preserves deep scroll then releases spare height when scrolling up',async()=>{
  const content=page.locator('.page-content'),shortHeight=await content.evaluate(node=>node.getBoundingClientRect().height);
  await page.getByLabel('Reporting period').selectOption('7d');await page.locator('.page-content:not([aria-busy="true"])').waitFor();
  assert.equal(await page.locator('.infra-run').count(),45);
  await page.evaluate(()=>scrollTo(0,document.documentElement.scrollHeight-innerHeight-120));const y=await page.evaluate(()=>scrollY);assert.ok(y>shortHeight);
  hold=true;const before=snapshots.length;
  await page.getByLabel('Reporting period').evaluate((node:HTMLSelectElement)=>{node.focus({preventScroll:true});node.value='24h';node.dispatchEvent(new Event('change',{bubbles:true}));});
  await expect.poll(()=>held.length).toBe(1);assert.ok(Math.abs((await page.evaluate(()=>scrollY))-y)<4);
  hold=false;for(const item of held.splice(0))json(item.response,snapshot(item.period));
  await page.locator('.page-content:not([aria-busy="true"])').waitFor();assert.equal(await page.locator('.infra-run').count(),2);
  assert.ok(Math.abs((await page.evaluate(()=>scrollY))-y)<4,'Shrinking content moved the user before they chose to scroll');
  assert.equal(snapshots.length,before+1);const after=snapshots.length;
  await page.evaluate(()=>scrollTo(0,0));
  await expect.poll(()=>content.evaluate(node=>Number.parseFloat((node as HTMLElement).style.minHeight)||0)).toBe(0);
  const releasedHeight=await content.evaluate(node=>node.getBoundingClientRect().height);assert.ok(Math.abs(releasedHeight-shortHeight)<4,'Scrolling up should return the report to its natural height');
  assert.equal(snapshots.length,after,'Releasing scroll reservation must not fetch another report');largeInfrastructure=false;
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
 await check('signing out clears the project route before a different account signs in',async()=>{
  assert.equal(new URL(page.url()).pathname,'/projects/phone/overview');
  await page.getByRole('button',{name:'Sign out',exact:true}).click();await page.getByRole('heading',{name:'Email and password',exact:true}).waitFor();
  assert.equal(new URL(page.url()).pathname,'/');assert.equal(await page.getByRole('button',{name:'Phone App',exact:true}).count(),0);
  await page.getByLabel('Email',{exact:true}).fill('alternate@example.com');await page.getByLabel('Password',{exact:true}).fill('synthetic-password');
  await page.getByRole('button',{name:'Sign in',exact:true}).click();await summary().waitFor();
  assert.equal(new URL(page.url()).pathname,'/projects/alternate/overview');assert.equal(await page.locator('main h1').innerText(),'Another project');
  assert.equal(await page.getByRole('heading',{name:'Page unavailable',exact:true}).count(),0);assert.equal(await page.getByRole('button',{name:'Phone App',exact:true}).count(),0);
 });
 await check('no browser exceptions',async()=>assert.deepEqual(errors,[]));
}catch(error){console.error(error);process.exitCode=1;}
finally{if((nativeFailureOnly||operationsOnly)&&results.length!==focusedChecks.size){results.push({name:'focused presentation checks completed',passed:false,error:'Unexpected focused check count'});process.exitCode=1;}writeFileSync(path.join(output,'results.json'),JSON.stringify({browser:engine,results,pageErrors:errors,syntheticAPIs:true},null,2)+'\n');console.log(JSON.stringify({browser:engine,projectUXPassed:results.filter(result=>result.passed).length,projectUXTotal:results.length,output}));await context.close();await browser.close();server.closeAllConnections();await new Promise<void>(resolve=>server.close(()=>resolve()));}
