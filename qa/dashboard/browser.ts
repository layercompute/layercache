// Run with DASHBOARD_LINK_FILE and DASHBOARD_BROWSER (defaults to chromium).
// QA_DASHBOARD_NATIVE_ONLY=1 limits acceptance to the four native display cases.
// A running dashboard with at least one project is required. No runtime credentials
// are read or emitted; synthetic response cases exercise the browser projection.
import { chromium, firefox, webkit } from '@playwright/test';
import fs from 'node:fs';
import assert from 'node:assert/strict';
import path from 'node:path';
import type {NativeIntegrationReport} from '../../dashboard/cache-view.js';
import {operationFixture} from './operation-fixtures.ts';
(async () => {
  const link = fs.readFileSync(process.env.DASHBOARD_LINK_FILE!, 'utf8').trim();
  const output = process.env.QA_OUTPUT || fs.mkdtempSync('/tmp/layercache-browser-');
  fs.mkdirSync(output, {recursive:true});
  const browserName = process.env.DASHBOARD_BROWSER || 'chromium';
  if (browserName !== 'chromium' && browserName !== 'firefox' && browserName !== 'webkit') throw new Error('DASHBOARD_BROWSER must be chromium, firefox, or webkit');
  const browser = await {chromium, firefox, webkit}[browserName].launch({headless:true, ...(browserName === 'chromium' ? {executablePath:process.env.CHROMIUM_PATH || undefined,args:['--no-sandbox']} : {})});
  const context = await browser.newContext({viewport:{width:1440,height:1050}});
  const page = await context.newPage();
  const errors: string[] = []; const results: {name: string; passed: boolean; error?: string}[] = []; const requests: string[] = [];
  const nativeOnly=process.env.QA_DASHBOARD_NATIVE_ONLY==='1';
  const operationsOnly=process.env.QA_DASHBOARD_OPERATIONS_ONLY==='1';
  const operationChecks=new Set(['operation observations preserve unknown phases without a selected baseline','operation comparisons show complete elapsed and disjoint phases without changing native totals','negative differences and reused baselines never create an additive savings total','operation context mismatches hide difference credit and use fixed refusal text','operation history and reporting outages preserve other results']);
  const nativeChecks=new Set(['old servers without native reports keep the compact dashboard clean','native task results separate eligible hits from source and producer evidence','native Xcode RPC counts and zero phase timings never become compiler replay','complete scoped compiler rates preserve generic and native-task coverage']);
  page.on('request', r => requests.push(r.url()));
  page.on('pageerror', e => errors.push(e.message));
  async function check(name: string, fn: () => Promise<void>) { if(nativeOnly&&!nativeChecks.has(name)||operationsOnly&&!operationChecks.has(name))return; try { await fn(); results.push({name,passed:true}); } catch (e) { results.push({name,passed:false,error:e instanceof Error ? e.message : String(e)}); await page.screenshot({path:path.join(output,name.replace(/\W/g,'-')+'.png'),fullPage:true}); } }
  async function finish(){fs.writeFileSync(path.join(output,'results.json'),JSON.stringify(results,null,2));console.log(JSON.stringify({browser:browserName,output,results},null,2));await browser.close();if(results.some(r=>!r.passed))process.exitCode=1;}
  async function settled() { await page.waitForFunction(() => !document.querySelector<HTMLButtonElement>('#refresh')!.disabled); }
  async function refresh() { await page.locator('#refresh').click(); await settled(); }
  await page.goto(link); await page.locator('.project').first().waitFor(); await settled();
  const snapshot = await page.evaluate(async (token: string) => (await fetch('/api/snapshot', {headers:{Authorization:`Bearer ${token}`}})).json(), link.split('#')[1]!);
  await check('live projects render', async () => assert.equal(await page.locator('.project').count(),snapshot.projects.length));
  await check('default dashboard does not grant maintenance', async () => assert.equal(await page.getByRole('button',{name:/^Prune /}).count(),0));
  await check('access scopes are separate from placement', async () => {
    const scopes=page.getByRole('region',{name:'Cache access and placement'});
    const text=await scopes.innerText();
    for(const name of ['Personal Cache','Team Cache','Public Cache'])assert.ok(text.includes(name));
    assert.match(text,/Only trusted Public Builds publish/);
    assert.match(text,/stored bytes by access scope are unavailable/);
    assert.match(await page.locator('.cache').first().innerText(),/Placement · Local machine/);
  });
  await check('configured recorded and incomplete estimated costs', async () => {
    const recorded=page.locator('.project').first().getByRole('region',{name:'Project cost'});
    assert.match(await recorded.locator('.cost-state').innerText(),/Operator-recorded/);
    assert.match(await recorded.locator('.cost-total').innerText(),/1\.75.*USD/);
    assert.match(await recorded.locator('.cost-period').innerText(),/UTC/);
    const partial=page.locator('.project').nth(1).getByRole('region',{name:'Project cost'});
    assert.equal(await partial.locator('.cost-total').innerText(),'Unknown');
    assert.match(await partial.innerText(),/Operator estimate/);
    assert.match(await partial.innerText(),/Known subtotal[\s\S]*0\.80.*EUR/);
    assert.match(await partial.innerText(),/Incomplete coverage/);
    assert.match(await partial.innerText(),/not provider-verified invoices/);
  });
  await check('live storage matches projection',async()=>{for(let i=0;i<snapshot.projects.length;i++){for(const [j,key] of ['local','team'].entries()){const status=snapshot.projects[i][key].status;if(!status)continue;const card=page.locator('.project').nth(i).locator('.cache').nth(j);assert.equal(await card.locator('.metrics .value').first().innerText(),status.artifacts.toLocaleString());const meter=card.locator('progress');if(status.maxBytes>0){assert.equal(await meter.evaluate(e=>e instanceof HTMLProgressElement ? e.max : null),status.maxBytes);assert.equal(await meter.evaluate(e=>e instanceof HTMLProgressElement ? e.value : null),Math.min(status.usageBytes,status.maxBytes));}}}});
  await check('fragment and persistence', async () => assert.deepEqual(await page.evaluate(() => [location.hash,localStorage.length,sessionStorage.length,document.cookie]),['',0,0,'']));
  await page.screenshot({path:path.join(output,'desktop.png'),fullPage:true});
  await check('small text contrast', async () => {
    const failures = await page.evaluate(() => {
      function rgb(s: string) { return (s.match(/[\d.]+/g)||[]).slice(0,3).map(Number); }
      function lum(c: number[]) { return c.map(v=>{v/=255;return v<=.04045?v/12.92:((v+.055)/1.055)**2.4;}).reduce((v,n,i)=>v+n*[.2126,.7152,.0722][i],0); }
      return ['.label','.detail','.project-head p','.subtitle','.integration','.state.connected'].map(selector=>{
        const e=document.querySelector(selector); if(!e)return null;
        let ancestor: Element | null=e, bg='';
        while(ancestor){bg=getComputedStyle(ancestor).backgroundColor;if(bg!=='rgba(0, 0, 0, 0)'&&bg!=='transparent')break;ancestor=ancestor.parentElement;}
        const fgLum=lum(rgb(getComputedStyle(e).color)), bgLum=lum(rgb(bg||'rgb(255,255,255)'));
        const ratio=(Math.max(fgLum,bgLum)+.05)/(Math.min(fgLum,bgLum)+.05);
        return ratio<4.5?{selector,ratio:Number(ratio.toFixed(2))}:null;
      }).filter(Boolean);
    });
    assert.deepEqual(failures,[], 'Small text must meet 4.5:1 contrast: '+JSON.stringify(failures));
  });
  await check('dialog keyboard and accessible name', async () => {
    await page.locator('#connect').focus(); await page.keyboard.press('Enter');
    assert.equal(await page.locator('dialog[open]').count(),1);
    const accessible = await page.locator('dialog').ariaSnapshot();
    assert.match(accessible, /dialog "[^"\n]+"/,'Dialog must have accessible name');
  });
  await page.keyboard.press('Escape');
  await check('dialog focus restored', async () => assert.equal(await page.evaluate(() => document.activeElement?.id),'connect'));
  await check('dialog close button', async () => { await page.locator('#connect').click(); await page.getByRole('button',{name:'Close',exact:true}).click(); assert.equal(await page.locator('dialog[open]').count(),0); });
  await check('period request and busy state', async () => {
    const request = page.waitForRequest(r => r.url().includes('period=7d'));
    await page.selectOption('#period','7d'); await request; await settled();
  });
  await check('same session link during refresh',async()=>{
    await page.route('**/api/snapshot?*',async r=>{await new Promise(resolve=>setTimeout(resolve,300));await r.continue();});
    await page.locator('#refresh').click();await page.goto(link);await settled();
    assert.ok(await page.locator('.project').count());assert.match(await page.locator('#notice').innerText(),/Local and Team reports/);
    assert.equal(await page.evaluate(()=>location.hash),'');await page.unroute('**/api/snapshot?*');
  });
  for (const width of [320,390,600,768,1024,1440]) await check(`viewport ${width}`, async () => {
    await page.setViewportSize({width,height:844});
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth),false);
    await page.screenshot({path:path.join(output,`viewport-${width}.png`),fullPage:true});
  });
  await page.setViewportSize({width:390,height:844});
  await check('unauthorized refresh clears snapshot', async () => {
    await page.route('**/api/snapshot?*', r=>r.fulfill({status:401,body:'expired'})); await refresh();
    assert.equal(await page.locator('.project').count(),0); assert.match(await page.locator('#notice').innerText(),/Session expired/);
    await page.unroute('**/api/snapshot?*'); await refresh(); assert.ok(await page.locator('.project').count());
  });
  await check('network failure and recovery', async () => {
    await page.route('**/api/snapshot?*', r=>r.abort()); await refresh(); assert.equal(await page.locator('.project').count(),0);
    await page.unroute('**/api/snapshot?*'); await refresh(); assert.ok(await page.locator('.project').count());
    await page.route('**/api/snapshot?*', r=>r.fulfill({status:504,body:'timeout'})); await refresh();
    assert.equal(await page.locator('.project').count(),0); assert.match(await page.locator('#notice').innerText(),/timed out/);
    await page.unroute('**/api/snapshot?*'); await refresh(); assert.ok(await page.locator('.project').count());
  });
  let mocked: typeof snapshot;
  await page.route('**/api/snapshot?*', r=>r.fulfill({json:mocked}));
  const clone=()=>JSON.parse(JSON.stringify(snapshot));
  function native(integration:'gradle'|'xcode'):NativeIntegrationReport{return {integration,reportedInvocations:1,reportedTasks:0,outcomes:{executed:0,fromCache:0,upToDate:0,noSource:0,skipped:0,failed:0,unknown:0},knownEligible:0,eligibilityUnknown:0,eligible:null,hits:0,misses:0,hitRate:null,sources:[],producerTiming:{known:0,total:0},producerResources:{known:0,total:0},grossAvoidedTaskTime:{milliseconds:null,known:0,total:0,confidence:'unknown'},netEstimatedBuildTimeSaved:{milliseconds:null,known:0,total:0,confidence:'unknown'},allocatedCpuSecondsSaved:null,measuredCpuSecondsSaved:null,costSaved:null,historyTruncated:false};}

  await check('remote-only selection omits unselected local cache',async()=>{
    mocked=clone();mocked.projects=mocked.projects.slice(0,1);mocked.projects[0].local={state:'not-selected',reportState:'not-selected'};
    await refresh();assert.equal(await page.getByRole('region',{name:'Local Cache',exact:true}).count(),0);assert.equal(await page.locator('.project .cache').count(),1);
    assert.match(await page.locator('.cache').innerText(),/Connected/);
    for(const width of [390,1440]){await page.setViewportSize({width,height:844});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);}
    await page.setViewportSize({width:390,height:844});
  });
  await check('long valid project identity layout', async () => {
    mocked=clone(); mocked.projects=mocked.projects.slice(0,1); mocked.projects[0].id='github.com/'+'a'.repeat(39)+'/'+'b'.repeat(100);
    await refresh(); assert.equal(await page.evaluate(() => document.documentElement.scrollWidth>innerWidth),false);
    const head=await page.locator('.project-head').evaluate(el=>({scroll:el.scrollWidth,width:el.clientWidth})); assert.ok(head.scroll<=head.width,JSON.stringify(head));
  });
  await check('report zero negative partial evidence', async () => {
    mocked=clone(); mocked.projects=mocked.projects.slice(0,1);
    mocked.projects[0].local.report={eligible:0,hits:0,misses:0,runs:5,degraded:true,netEstimatedBuildTimeSaved:{milliseconds:-65000,known:1,total:2,confidence:'low'},integrations:[{integration:'turbo',hits:0,eligible:0}]};
    await refresh(); const text=await page.locator('.cache').first().innerText();
    assert.match(text,/No eligible outcomes/); assert.match(text,/−1.1 min/); assert.match(text,/1\/2 known · low confidence/); assert.match(text,/Partial or degraded evidence/);
    assert.equal(await page.locator('.cache').first().locator('.stats .value').nth(1).innerText(),'Unknown');
  });
  await check('eligible outcomes source attribution and metering coverage',async()=>{
    mocked=clone();mocked.projects=mocked.projects.slice(0,1);
    mocked.projects[0].local.report={eligible:10,hits:7,misses:3,runs:13,degraded:false,hitRate:0,netEstimatedBuildTimeSaved:{milliseconds:3000,known:2,total:4,confidence:'low'},bytes:{downloaded:7168,uploaded:2048},timing:{lookupMs:10,downloadMs:200,verificationMs:30,restoreMs:400,uploadMs:50},sources:[{source:'localCache',hits:2,downloadedBytes:2048,uploadedBytes:0},{source:'teamCache',hits:3,downloadedBytes:3072,uploadedBytes:1024},{source:'publicCache',hits:1,downloadedBytes:1024,uploadedBytes:1024},{source:'unattributed',hits:1,downloadedBytes:1024,uploadedBytes:0}],integrations:[{integration:'turbo',hits:5,eligible:7},{integration:'buildkit',hits:2,eligible:3}]};
    await refresh();const cache=page.locator('.cache').first();
    assert.equal(await cache.locator('.stats .value').nth(1).innerText(),'70%');
    for(const [source,hits] of [['localCache','2'],['teamCache','3'],['publicCache','1'],['unattributed','1']])assert.equal(await cache.locator(`.source-row[data-source="${source}"] td`).first().innerText(),hits);
    const text=await cache.innerText();assert.match(text,/7\.0 KiB/);assert.match(text,/2\.0 KiB/);assert.match(text,/13 observed runs/);
    assert.match(text,/Phase durations[\s\S]*can overlap/);assert.match(text,/Local source activity|Local activity cannot be split/);
    assert.match(text,/Native task outcomes and protocol activity have separate coverage/);
    assert.match(text,/turbo · 5\/7 hits/);assert.match(text,/buildkit · 2\/3 hits/);
  });
  if(nativeOnly){mocked=clone();mocked.projects=mocked.projects.slice(0,1);mocked.projects[0].local.report={eligible:10,hits:7,misses:3,runs:13,degraded:false,netEstimatedBuildTimeSaved:{milliseconds:null,known:0,total:0,confidence:'unknown'},integrations:[{integration:'turbo',hits:7,eligible:10}]};await refresh();}
  await check('old servers without native reports keep the compact dashboard clean',async()=>{
    const cache=page.locator('.cache').first();assert.equal(await cache.locator('details.native-results').count(),0);assert.equal(await cache.locator('.stats .value').nth(1).innerText(),'70%');
  });
  await check('native task results separate eligible hits from source and producer evidence',async()=>{
    const report:NativeIntegrationReport={...native('gradle'),reportedTasks:8,outcomes:{executed:2,fromCache:3,upToDate:1,noSource:1,skipped:1,failed:0,unknown:0},hits:3,knownEligible:4,eligibilityUnknown:1,misses:1,sources:[{source:'teamCache',hits:1},{source:'unattributed',hits:2}],producerTiming:{known:1,total:3},producerResources:{known:0,total:3},grossAvoidedTaskTime:{milliseconds:9000,known:1,total:3,confidence:'low'},netAvoidedTaskTime:{milliseconds:-4000,known:1,total:3,confidence:'low'}};
    mocked.projects[0].local.status.visibility='personal';mocked.projects[0].local.report.nativeReports=[report];await refresh();
    const cache=page.locator('.cache').first(),details=cache.locator('details.native-results');assert.equal(await details.getAttribute('open'),null);await details.locator(':scope > summary').click();
    const gradle=details.getByRole('region',{name:'Gradle native results',exact:true});const text=await gradle.innerText();
    assert.match(text,/3 native task cache hits[\s\S]*Not measured eligible-task hit rate[\s\S]*4 known eligible tasks[\s\S]*1 outcomes with unknown eligibility/);
    assert.match(text,/Personal Cache 1[\s\S]*Unattributed 2/);assert.match(text,/producer timing matched for 1 of 3[\s\S]*resources matched for 0 of 3/i);assert.match(text,/Net avoided task elapsed time [−-]4\.0 s/);
    assert.match(await details.innerText(),/CPU-hours and compute cost saved are not measured/);assert.equal(await cache.locator('.stats .value').nth(1).innerText(),'70%');
    for(const width of [320,1440]){await page.setViewportSize({width,height:844});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);}
    await page.setViewportSize({width:390,height:844});
  });
  await check('native Xcode RPC counts and zero phase timings never become compiler replay',async()=>{
    const report:NativeIntegrationReport={...native('xcode'),counters:{rpc:{actionReads:12,actionHits:9,actionWrites:2,casReads:30,casHits:24,casWrites:4,cacheErrors:0},compiler:{scope:'unknown',coverage:'unsupported',reason:'unsupported',hits:null,misses:null,skipped:null,unknown:null}},timingMs:{operation:5030,compile:5000,load:null,restore:0,close:20,snapshot:null}};
    mocked.projects[0].local.report.nativeReports=[report];await refresh();const cache=page.locator('.cache').first(),details=cache.locator('details.native-results');await details.locator(':scope > summary').click();
    const xcode=details.getByRole('region',{name:'Xcode native results',exact:true});assert.match(await xcode.locator('.native-rpc').innerText(),/9 hits \/ 12 reads[\s\S]*24 hits \/ 30 reads[\s\S]*not compiler replay/);
    assert.match(await xcode.locator('.native-compiler').innerText(),/Unknown hits[\s\S]*Unknown misses[\s\S]*Scoped compiler-verdict hit rate Not measured/);assert.match(await xcode.locator('.native-phases').innerText(),/Load Unknown[\s\S]*Restore 0\.0 s/);
    assert.equal(await cache.locator('.stats .value').nth(1).innerText(),'70%');
  });
  await check('complete scoped compiler rates preserve generic and native-task coverage',async()=>{
    const compiler={scope:'clang-pch',coverage:'complete',reason:'none',hits:8,misses:2,skipped:null,unknown:0} as const;
    const report:NativeIntegrationReport={...native('xcode'),counters:{compiler}};mocked.projects[0].local.report.nativeReports=[report];await refresh();
    const cache=page.locator('.cache').first(),details=cache.locator('details.native-results');await details.locator(':scope > summary').click();const xcode=details.getByRole('region',{name:'Xcode native results',exact:true});
    assert.match(await xcode.innerText(),/Clang \/ PCH[\s\S]*complete compiler verdict coverage[\s\S]*Scoped compiler-verdict hit rate 80%/);assert.match(await xcode.innerText(),/no native task denominator/);assert.equal(await cache.locator('.stats .value').nth(1).innerText(),'70%');
    mocked.projects[0].local.report.nativeReports=[{...report,counters:{compiler:{...compiler,reason:'truncated'}}}];await refresh();const partial=cache.locator('details.native-results');await partial.locator(':scope > summary').click();assert.match(await partial.innerText(),/Scoped compiler-verdict hit rate Not measured/);
    delete mocked.projects[0].local.report.nativeReports;mocked.projects[0].local.report.nativeReportsUnavailable=true;mocked.projects[0].local.report.netEstimatedBuildTimeSaved={milliseconds:9000,known:1,total:1,confidence:'high'};await refresh();
    const unavailable=cache.locator('details.native-results');assert.equal(await unavailable.locator(':scope > summary').innerText(),'Native reporting unavailable');await unavailable.locator(':scope > summary').click();
    for(const integration of ['Gradle','Xcode']){const native=unavailable.getByRole('region',{name:`${integration} native results`,exact:true});assert.match(await native.innerText(),/Reporting unavailable[\s\S]*could not be read[\s\S]*Refresh project activity/);assert.doesNotMatch(await native.innerText(),/No reports this period/);}
    assert.equal(await cache.locator('.stats .value').nth(1).innerText(),'70%');assert.equal(await cache.locator('.metrics .value').nth(1).innerText(),'9.0 s');assert.match(await cache.locator('.metrics .detail').nth(1).innerText(),/Turbo, Actions and BuildKit reported graphs/);
    delete mocked.projects[0].local.report.nativeReportsUnavailable;await refresh();assert.equal(await cache.locator('details.native-results').count(),0);
  });
  if(nativeOnly){assert.equal(results.length,nativeChecks.size);assert.deepEqual(errors,[]);await finish();return;}
  function operationSnapshot(){mocked=clone();mocked.projects=mocked.projects.slice(0,1);mocked.projects[0].local.report={eligible:10,hits:7,misses:3,runs:13,degraded:false,netEstimatedBuildTimeSaved:{milliseconds:null,known:0,total:0,confidence:'unknown'},integrations:[{integration:'turbo',hits:7,eligible:10}],operationReports:operationFixture()};return mocked.projects[0].local.report;}
  await check('operation observations preserve unknown phases without a selected baseline',async()=>{
    const report=operationSnapshot();delete report.operationReports;await refresh();assert.equal(await page.locator('.cache').first().locator('.operation-results').count(),0);
    report.operationReports={observations:[],comparisons:[],storedObservations:0,historyTruncated:false};await refresh();assert.match(await page.locator('.cache').first().locator('.operation-empty').innerText(),/No operation timings[\s\S]*hit alone does not measure/);assert.equal(await page.locator('.cache').first().locator('.stats .value').nth(1).innerText(),'70%');
    report.operationReports=operationFixture();const operations=report.operationReports;operations.observations=operations.observations.slice(0,1);operations.comparisons=[];operations.storedObservations=1;
    delete operations.observations[0].report.baselineReceiptId;operations.observations[0].report.phaseTimingMs.restore=null;await refresh();
    const section=page.locator('.cache').first().getByRole('region',{name:'Operation timings',exact:true});await section.locator('.operation-observation summary').click();
    assert.match(await section.innerText(),/No selected baseline[\s\S]*does not establish time saved/);assert.match(await section.innerText(),/Current complete operation[\s\S]*1\.3 min/);
    const phases=section.getByRole('table',{name:'Operation phase timings'});assert.deepEqual(await phases.locator('tr[data-phase="restore"] td').allTextContents(),['Unknown','Unknown']);assert.deepEqual(await phases.locator('.operation-overhead td').allTextContents(),['Unknown','Unknown']);
    assert.match(await section.innerText(),/Source dddddddddddd/);assert.match(await section.innerText(),/session does not attest to one physical machine/);
    for(const width of [320,1440]){await page.setViewportSize({width,height:844});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);}await page.setViewportSize({width:390,height:844});
  });
  await check('operation comparisons show complete elapsed and disjoint phases without changing native totals',async()=>{
    operationSnapshot();await refresh();const cache=page.locator('.cache').first(),section=cache.getByRole('region',{name:'Operation timings',exact:true});await section.locator('.operation-observation summary').first().click();
    const current=section.locator('.operation-observation').first();assert.match(await current.innerText(),/Baseline complete operation[\s\S]*1\.7 min[\s\S]*Current complete operation[\s\S]*1\.3 min[\s\S]*Observed elapsed difference[\s\S]*20\.0 s · shorter/);
    assert.match(await current.innerText(),/Observed work difference[\s\S]*25\.0 s · shorter[\s\S]*Observed cache-phase difference[\s\S]*−5\.0 s · longer/);
    const phases=current.getByRole('table',{name:'Operation phase timings'});assert.deepEqual(await phases.locator('tr[data-phase="restore"] td').allTextContents(),['0.0 s','6.0 s']);assert.deepEqual(await phases.locator('.operation-overhead td').allTextContents(),['5.0 s','10.0 s']);
    assert.equal(await cache.locator('.stats .value').nth(1).innerText(),'70%');assert.equal(await cache.locator('.metrics .value').nth(1).innerText(),'Unknown');assert.match(await section.innerText(),/do not measure CPU usage, provider charges/);
  });
  await check('negative differences and reused baselines never create an additive savings total',async()=>{
    const report=operationSnapshot(),operations=report.operationReports;const slow=structuredClone(operations.observations[0]);slow.receiptId='c'.repeat(64);slow.report.operationId='run-slower-operation';slow.report.elapsedMs=110000;slow.report.phaseTimingMs.work=100000;
    const comparison={...operations.comparisons[0],candidateReceiptId:slow.receiptId,candidateElapsedMs:110000,observedDifferenceMs:-10000,observedWorkDifferenceMs:-5000};operations.observations.unshift(slow);operations.comparisons.unshift(comparison);operations.storedObservations=3;await refresh();
    const cache=page.locator('.cache').first(),section=cache.getByRole('region',{name:'Operation timings',exact:true});assert.match(await section.innerText(),/3 recorded operations · 2 matched comparisons in returned observations/);assert.match(await section.innerText(),/−10\.0 s · longer/);
    assert.match(await section.locator('.operation-no-total').innerText(),/Pairs are not added together[\s\S]*Reused baselines and overlapping observations/);assert.equal(await cache.locator('.metrics .value').nth(1).innerText(),'Unknown');
  });
  await check('operation context mismatches hide difference credit and use fixed refusal text',async()=>{
    const report=operationSnapshot(),comparison=report.operationReports.comparisons[0];comparison.matched=false;comparison.method='unmatched';comparison.reasons=['sourceMismatch','environmentMismatch','<img src=x onerror=alert(1)>'];await refresh();
    const section=page.locator('.cache').first().getByRole('region',{name:'Operation timings',exact:true});await section.locator('.operation-observation summary').first().click();const text=await section.innerText();assert.match(text,/Comparison unavailable[\s\S]*source revisions differ[\s\S]*environments differ[\s\S]*context could not be verified/);assert.doesNotMatch(text,/20\.0 s · shorter|onerror/);assert.equal(await section.locator('img').count(),0);
    assert.equal(await section.locator('.operation-metrics').first().locator('dd').last().innerText(),'Unknown');
  });
  await check('operation history and reporting outages preserve other results',async()=>{
    const report=operationSnapshot();report.operationReports.observations=report.operationReports.observations.slice(0,1);report.operationReports.storedObservations=40;report.operationReports.historyTruncated=true;await refresh();
    const cache=page.locator('.cache').first();let section=cache.getByRole('region',{name:'Operation timings',exact:true});await section.locator('.operation-observation summary').click();assert.match(await section.innerText(),/outside the returned observations[\s\S]*phase details are unavailable/);assert.match(await section.innerText(),/Showing 1 recent observations[\s\S]*history is incomplete/);
    delete report.operationReports;report.operationReportsUnavailable=true;report.nativeReports=[native('gradle')];await refresh();section=cache.getByRole('region',{name:'Operation timings',exact:true});assert.match(await section.innerText(),/reporting unavailable[\s\S]*Other cache and native results remain available/);assert.equal(await cache.locator('details.native-results').count(),1);assert.equal(await cache.locator('.stats .value').nth(1).innerText(),'70%');
  });
  if(operationsOnly){assert.equal(results.length,operationChecks.size);assert.deepEqual(errors,[]);await finish();return;}
  await check('unavailable source evidence and missing cost stay unknown',async()=>{
    mocked=clone();mocked.projects=mocked.projects.slice(0,1);delete mocked.projects[0].local.report.sources;mocked.projects[0].cost={state:'unknown',complete:false,knownSubtotalMinor:0,totalMinor:null};
    await refresh();const cache=page.locator('.cache').first();
    assert.equal(await cache.locator('.source-row[data-source="publicCache"] td').first().innerText(),'Unknown');
    assert.equal(await page.locator('.cost-total').first().innerText(),'Unknown');
    assert.match(await page.getByRole('region',{name:'Project cost'}).innerText(),/No cost ledger configured/);
  });
  await check('miss publication uploads are not reported as zero by source',async()=>{
    mocked=clone();mocked.projects=mocked.projects.slice(0,1);
    mocked.projects[0].local.report={eligible:1,hits:0,misses:1,runs:1,degraded:false,netEstimatedBuildTimeSaved:{milliseconds:-5,known:1,total:1,confidence:'medium'},bytes:{downloaded:0,uploaded:123},sources:[],integrations:[{integration:'turbo',hits:0,eligible:1}]};
    await refresh();const cache=page.locator('.cache').first(),sources=cache.getByRole('table',{name:'Reported cache source activity'});
    assert.match(await cache.locator('.transfer-metrics').innerText(),/Uploaded[\s\S]*123 B/);
    assert.equal(await sources.getByRole('columnheader',{name:'Uploaded',exact:true}).count(),0);
    assert.match(await cache.innerText(),/destination is not attributed by restore source/);
  });
  await check('large minor-unit costs retain exact currency precision',async()=>{
    for(const [currency,expected] of [['USD','90,071,992,547,409.91'],['JPY','9,007,199,254,740,991'],['BHD','9,007,199,254,740.991']]){
      mocked=clone();mocked.projects=mocked.projects.slice(0,1);
      mocked.projects[0].cost={state:'recorded',currency,periodStart:'2026-10-01T00:00:00Z',periodEnd:'2026-11-01T00:00:00Z',complete:true,items:[],knownSubtotalMinor:9007199254740991,totalMinor:9007199254740991};
      await refresh();const displayed=await page.getByRole('region',{name:'Project cost',exact:true}).locator('.cost-total').innerText();
      assert.ok(displayed.includes(expected),`${currency} lost minor-unit precision: ${displayed}`);assert.ok(displayed.endsWith(currency),displayed);
    }
    mocked=clone();await refresh();
  });
  await check('public cache never renders customer maintenance',async()=>{
    mocked=clone();mocked.projects=mocked.projects.slice(0,1);mocked.projects[0].team.status.visibility='public';mocked.projects[0].team.status.canPrune=true;mocked.projects[0].team.pruneAllowed=true;
    await refresh();const remote=page.locator('.cache').nth(1);assert.match(await remote.innerText(),/Public Cache/);assert.equal(await remote.getByRole('button',{name:/^Prune /}).count(),0);
  });
  await check('project text is not interpreted as HTML',async()=>{ mocked=clone(); mocked.projects[0].id='<img src=x onerror=alert(1)>';await refresh();assert.equal(await page.locator('.project img').count(),0); });
  await check('report outage preserves storage', async () => { mocked=clone(); delete mocked.projects[0].local.report;mocked.projects[0].local.reportState='unavailable';await refresh();const text=await page.locator('.cache').first().innerText();assert.match(text,/Artifact storage/);assert.match(text,/Report unavailable/);mocked.projects[0].local.reportState='authentication-required';await refresh();assert.match(await page.locator('.cache').first().innerText(),/Sign in again to read reports/); });
  await check('maximum project count renders',async()=>{mocked=clone();mocked.projects=Array.from({length:32},(_,i)=>({...mocked.projects[0],id:'github.com/team/project-'+i}));await refresh();assert.equal(await page.locator('.project').count(),32);assert.equal(await page.locator('#count').innerText(),'32');assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);});
  await page.unroute('**/api/snapshot?*'); await refresh();
  await check('second authorized tab independent',async()=>{const other=await page.context().newPage();await other.goto(link);await other.locator('.project').first().waitFor();await other.close();});
  await check('brand navigation preserves session',async()=>{await page.locator('.brand').click();await settled();assert.ok(await page.locator('.project').count(),'Brand link logs out active dashboard session');});
  await check('reload requires original link',async()=>{await page.goto('about:blank');await page.goto(link);await page.locator('.project').first().waitFor();await page.reload();await page.getByText('Open the complete dashboard link printed by the CLI',{exact:false}).waitFor();assert.equal(await page.locator('.project').count(),0);});
  await check('same tab original link restores session',async()=>{await page.goto(link);await page.locator('.project').first().waitFor({timeout:5000});await settled();assert.ok(await page.locator('.project').count(),'Original CLI link fails to restore signed-out page');assert.equal(await page.evaluate(()=>location.hash),'');});
  await check('requests remain same origin and credential free URLs',async()=>{const base=new URL(link);for(const raw of requests){if(raw==='about:blank')continue;const u=new URL(raw);assert.equal(u.origin,base.origin);assert.equal(u.hash,'');assert.equal(u.search.includes(link.split('#')[1]!),false);}});
  const maintenanceFile=process.env.DASHBOARD_MAINTENANCE_LINK_FILE;
  if(!maintenanceFile)throw new Error('DASHBOARD_MAINTENANCE_LINK_FILE must identify the isolated opt-in fixture');
  const maintenanceLink=fs.readFileSync(maintenanceFile,'utf8').trim();
  const maintenance=await context.newPage();const maintenanceRequests:{url:string;method:string;body:string|null}[]=[];
  maintenance.on('pageerror',error=>errors.push(error.message));
  maintenance.on('request',request=>maintenanceRequests.push({url:request.url(),method:request.method(),body:request.postData()}));
  await maintenance.goto(maintenanceLink);await maintenance.getByRole('button',{name:'Prune Local Cache',exact:true}).first().waitFor();
  const posts=()=>maintenanceRequests.filter(request=>request.method==='POST');
  const prune=maintenance.getByRole('button',{name:'Prune Local Cache',exact:true}).first();
  await check('prune requires explicit target confirmation and can cancel',async()=>{
    await prune.click();const dialog=maintenance.getByRole('dialog',{name:'Prune Local Cache'});
    await dialog.getByRole('button',{name:'Prune cache',exact:true}).click();assert.match(await dialog.locator('.prune-result').innerText(),/Enter a whole number/);
    await dialog.getByLabel('Target storage in bytes').fill('0');await dialog.getByRole('button',{name:'Prune cache',exact:true}).click();assert.match(await dialog.locator('.prune-result').innerText(),/Confirm deletion/);
    await dialog.getByLabel('I confirm deletion of eligible cached artifacts.').check();await dialog.getByRole('button',{name:'Cancel',exact:true}).click();
    assert.equal(await maintenance.locator('dialog[open]').count(),0);assert.equal(posts().length,0);
  });
  await check('uncertain prune refreshes without automatically retrying',async()=>{
    let snapshots=0;maintenance.on('request',request=>{if(new URL(request.url()).pathname==='/api/snapshot')snapshots++;});
    await maintenance.route('**/api/prune',route=>route.fulfill({status:502,body:'cache maintenance is unavailable; refresh before trying again'}));
    await prune.click();const dialog=maintenance.getByRole('dialog',{name:'Prune Local Cache'});
    await dialog.getByLabel('Target storage in bytes').fill('0');await dialog.getByLabel('I confirm deletion of eligible cached artifacts.').check();await dialog.getByRole('button',{name:'Prune cache',exact:true}).click();
    await dialog.getByText('A snapshot refresh was requested.',{exact:false}).waitFor();
    assert.equal(posts().length,1);assert.ok(snapshots>0,'Uncertain maintenance must fetch current state');
    await maintenance.waitForTimeout(600);assert.equal(posts().length,1,'No automatic retry after an uncertain mutation');
    await dialog.getByRole('button',{name:'Cancel',exact:true}).click();await maintenance.unroute('**/api/prune');
  });
  await check('confirmed prune refreshes only selected fixture cache',async()=>{
    await prune.click();const dialog=maintenance.getByRole('dialog',{name:'Prune Local Cache'});
    await dialog.getByLabel('Target storage in bytes').fill('0');await dialog.getByLabel('I confirm deletion of eligible cached artifacts.').check();
    const response=maintenance.waitForResponse(response=>new URL(response.url()).pathname==='/api/prune');
    await dialog.getByRole('button',{name:'Prune cache',exact:true}).click();assert.equal((await response).status(),200);
    await maintenance.waitForFunction(()=>!document.querySelector('dialog[open]'));
    const project=maintenance.locator('.project').first();assert.equal(await project.locator('.cache').first().locator('.stats .value').first().innerText(),'0 B');
    assert.notEqual(await project.locator('.cache').nth(1).locator('.stats .value').first().innerText(),'0 B');
    assert.equal(posts().length,2);const body=JSON.parse(posts()[1].body!);assert.deepEqual(body,{projectId:snapshot.projects[0].id,cache:'local',targetBytes:0,confirm:true});
  });
  await check('maintenance requests remain same origin and credential free URLs',async()=>{
    const base=new URL(maintenanceLink);const secret=base.hash.slice(1);
    for(const request of maintenanceRequests){const url=new URL(request.url);assert.equal(url.origin,base.origin);assert.equal(url.hash,'');assert.equal(url.search.includes(secret),false);}
  });
  await maintenance.close();
  await check('no browser exceptions',async()=>assert.deepEqual(errors,[]));
  await finish();
})().catch(e=>{console.error(e);process.exit(1)});
