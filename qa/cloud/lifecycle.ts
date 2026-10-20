// Exercise the shipped portal bundle against synthetic same-origin APIs only.
// Persisted PageTransitionEvents qualify the restore handler, not browser BFCache eligibility.
import {chromium, firefox, webkit, expect} from '@playwright/test';
import assert from 'node:assert/strict';
import {createServer, type ServerResponse} from 'node:http';
import {readFileSync, mkdirSync, mkdtempSync, writeFileSync} from 'node:fs';
import path from 'node:path';
declare global {interface Window {lifecyclePendingReady?:boolean;lifecycleReleaseOld?:()=>void;}}

const engine=process.env.DASHBOARD_BROWSER||'chromium';
if(engine!=='chromium'&&engine!=='firefox'&&engine!=='webkit')throw new Error('Invalid DASHBOARD_BROWSER');
const observationDelay=Number(process.env.QA_LIFECYCLE_OBSERVATION_DELAY_MS||'0');
if(!Number.isInteger(observationDelay)||observationDelay<0||observationDelay>1000)throw new Error('Invalid QA_LIFECYCLE_OBSERVATION_DELAY_MS');
const output=process.env.QA_OUTPUT?path.join(process.env.QA_OUTPUT,'lifecycle'):mkdtempSync('/tmp/layercache-cloud-lifecycle-');mkdirSync(output,{recursive:true});
const projects=Array.from({length:5},(_,index)=>({id:`project-${index+1}`,teamId:'team-fixture',name:`Lifecycle project ${index+1}`,repository:`example/project-${index+1}`,role:'reader',operatorManaged:true}));
let origin='',signedIn=true,holdSnapshots=false,holdSession=false,httpFailure=false;
const heldSnapshots:ServerResponse[]=[],heldSessions:ServerResponse[]=[];
const publicConfig={githubClientId:'',passwordLogin:true};
const session=()=>({user:{id:'fixture',login:'lifecycle-user'},csrfToken:'synthetic-csrf',cloudOrigin:origin,authMethod:'password',passwordChangeRequired:false,canChangePassword:true,canManageGitHub:false,canCreateTeam:false,teams:[{id:'team-fixture',name:'Lifecycle team',role:'reader',kind:'team',operatorManaged:true}],projects,invitations:[]});
function json(response:ServerResponse,value:unknown,status=200){if(response.destroyed)return;response.writeHead(status,{'Content-Type':'application/json'});response.end(JSON.stringify(value));}
const snapshot={cache:{state:'unavailable',reportState:'unavailable'},scope:'team',updatedAt:'2026-10-20T12:00:00Z',executions:{version:1,from:'2026-10-19T12:00:00Z',to:'2026-10-20T12:00:00Z',updatedAt:'2026-10-20T12:00:00Z',retentionDays:30,staleAfterSeconds:86400,historyTruncated:false,installations:[],jobs:[]}};
const server=createServer((request,response)=>{
 const url=new URL(request.url!,origin||'http://fixture');
 if(url.pathname==='/'||/^\/projects\/[^/]+\/(overview|activity|infrastructure|storage|settings)$/.test(url.pathname)||url.pathname==='/app.js'||url.pathname==='/style.css'){
  const document=url.pathname==='/'||url.pathname.startsWith('/projects/');if(document)holdSnapshots=false;
  const name=document?'index.html':url.pathname.slice(1);
  response.writeHead(200,{'Content-Type':name.endsWith('.js')?'text/javascript':name.endsWith('.css')?'text/css':'text/html'});
  response.end(readFileSync(path.resolve('internal/portal/assets',name)));return;
 }
 if(url.pathname==='/api/session'){if(holdSession){heldSessions.push(response);return;}json(response,signedIn?session():{error:'Unauthorized'},signedIn?200:401);return;}
 if(url.pathname==='/api/public-config'){json(response,publicConfig);return;}
 if(url.pathname==='/api/teams/team-fixture/usage'){json(response,{projectCount:5,allocatedCacheBytes:null,allocationComplete:false});return;}
 if(projects.some(project=>url.pathname===`/api/projects/${project.id}/snapshot`)){
  if(holdSnapshots){heldSnapshots.push(response);return;}
  if(httpFailure&&url.pathname==='/api/projects/project-1/snapshot'){json(response,{error:'fixture unavailable'},503);return;}
  json(response,snapshot);return;
 }
 json(response,{error:'unexpected fixture request'},404);
});
await new Promise<void>(resolve=>server.listen(0,'127.0.0.1',resolve));const address=server.address();assert.ok(address&&typeof address!=='string');origin=`http://127.0.0.1:${address.port}`;
const browser=await {chromium,firefox,webkit}[engine].launch({headless:true,...(engine==='chromium'?{executablePath:process.env.CHROMIUM_PATH||undefined,args:['--no-sandbox']}:{})});
const context=await browser.newContext();const page=await context.newPage();
const errors:string[]=[],fetches:{document:string;path:string}[]=[],results:{name:string;passed:boolean;error?:string}[]=[];
page.on('pageerror',error=>errors.push(error.message));
page.on('console',message=>{if(message.type()==='debug'&&message.text().startsWith('lifecycle-fetch:')){const record=JSON.parse(message.text().slice('lifecycle-fetch:'.length));if(observationDelay)setTimeout(()=>fetches.push(record),observationDelay);else fetches.push(record);}});
await page.addInitScript(()=>{
 const identity=crypto.randomUUID(),original=window.fetch.bind(window);
 window.fetch=(...args)=>{console.debug('lifecycle-fetch:'+JSON.stringify({document:identity,path:new URL(String(args[0]),location.href).pathname}));return original(...args);};
});
const settled=async()=>{await page.locator('.page-content:not([aria-busy="true"])').waitFor();await page.locator('.infrastructure-panel').waitFor();};
const snapshotsFor=(records:typeof fetches)=>records.filter(item=>item.path.endsWith('/snapshot'));
async function check(name:string,run:()=>Promise<void>){try{await run();results.push({name,passed:true});}catch(error){results.push({name,passed:false,error:error instanceof Error?error.message:String(error)});throw error;}}
try{
 await page.goto(origin+'/projects/project-1/infrastructure');await settled();
 // HTTP arrival and rendered DOM do not order Playwright console delivery.
 // Drain the exact initial document observations before taking any offsets.
 await expect.poll(()=>snapshotsFor(fetches).length).toBe(1);
 await check('in-flight reload leaves only the selected project request and no page errors',async()=>{
  const start=fetches.length;holdSnapshots=true;
  await page.getByRole('button',{name:'Refresh',exact:true}).click();
  await expect.poll(()=>heldSnapshots.length).toBe(1);
  await expect.poll(()=>snapshotsFor(fetches.slice(start)).length).toBe(1);
  const document=snapshotsFor(fetches.slice(start))[0]!.document;
  await page.reload({waitUntil:'networkidle'});await settled();
  await expect.poll(()=>snapshotsFor(fetches.slice(start)).filter(item=>item.document!==document).length).toBe(1);
  assert.equal(snapshotsFor(fetches.slice(start)).filter(item=>item.document===document).length,1,'The unloading document requested other projects');
  assert.deepEqual(errors,[]);
 });
 await check('explicit HTTP failure stays in the selected project and refresh recovers',async()=>{
  httpFailure=true;const start=fetches.length;
  await page.getByRole('button',{name:'Refresh',exact:true}).click();
  await page.getByText('fixture unavailable',{exact:false}).waitFor();
  await expect.poll(()=>snapshotsFor(fetches.slice(start)).length).toBe(1);
  assert.equal(snapshotsFor(fetches.slice(start))[0]!.path,'/api/projects/project-1/snapshot');httpFailure=false;
  await page.getByRole('button',{name:'Refresh',exact:true}).click();await settled();
 });
 await check('transport loss never fetches unrelated projects or raises a page exception',async()=>{
  const start=fetches.length,pattern='**/api/projects/*/snapshot?*';await page.route(pattern,route=>route.abort());
  await page.getByRole('button',{name:'Refresh',exact:true}).click();
  await expect.poll(()=>snapshotsFor(fetches.slice(start)).length).toBe(1);
  assert.equal(snapshotsFor(fetches.slice(start))[0]!.path,'/api/projects/project-1/snapshot');assert.deepEqual(errors,[]);await page.unroute(pattern);
  await page.getByRole('button',{name:'Refresh',exact:true}).click();await settled();
 });
 await check('persisted restore clears old private content and revalidates authorization',async()=>{
  holdSession=true;
  await page.evaluate(()=>{dispatchEvent(new PageTransitionEvent('pagehide',{persisted:true}));dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}));});
  await expect.poll(()=>heldSessions.length).toBe(1);
  await page.getByText('Restoring your workspace…',{exact:true}).waitFor();
  assert.equal(await page.getByRole('navigation',{name:'Teams and projects'}).count(),0);
  signedIn=false;holdSession=false;json(heldSessions[0]!,{error:'Unauthorized'},401);
  await page.getByRole('heading',{name:'Email and password',exact:true}).waitFor();
  assert.equal(await page.getByRole('heading',{name:projects[0]!.name,exact:true}).count(),0);
  signedIn=true;
  await page.evaluate(()=>{dispatchEvent(new PageTransitionEvent('pagehide',{persisted:true}));dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}));});
  await settled();
 });
 for(const status of [401,200])await check(`a late old ${status} cannot replace a restored session`,async()=>{
  // Hold only this old response in the browser. Two identical pending HTTP GETs
  // can be coalesced by the engine, which would also hold the fresh restore.
  const body=JSON.stringify(status===401?{error:'Unauthorized'}:{...session(),user:{id:'obsolete',login:'obsolete-user'},projects:[{...projects[0],name:'Obsolete project'}]});
  await page.evaluate(({status,body})=>{
   const original=window.fetch;let held=false;window.lifecyclePendingReady=false;
   window.fetch=(...args)=>{if(!held&&new URL(String(args[0]),location.href).pathname==='/api/session'){held=true;return new Promise<Response>(resolve=>{window.lifecyclePendingReady=true;window.lifecycleReleaseOld=()=>resolve(new Response(body,{status,headers:{'Content-Type':'application/json'}}));});}return original(...args);};
  },{status,body});
  await page.getByRole('button',{name:'Refresh',exact:true}).click();await page.waitForFunction(()=>window.lifecyclePendingReady);
  await page.evaluate(()=>{dispatchEvent(new PageTransitionEvent('pagehide',{persisted:true}));dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}));});
  await settled();
  await page.evaluate(()=>window.lifecycleReleaseOld!());
  await page.evaluate(()=>new Promise<void>(resolve=>requestAnimationFrame(()=>requestAnimationFrame(()=>resolve()))));
  assert.equal(await page.getByRole('navigation',{name:'Teams and projects'}).count(),1);
  assert.equal(await page.getByRole('heading',{name:'Email and password',exact:true}).count(),0);
  await page.getByText('@lifecycle-user',{exact:true}).waitFor();assert.equal(await page.getByText('@obsolete-user',{exact:true}).count(),0);
  assert.equal(await page.locator('#notice').innerText(),'','The old refresh changed the restored view notice');
 });
 await check('current expiration explains sign-in while a fresh anonymous page stays neutral',async()=>{
  signedIn=false;await page.getByRole('button',{name:'Refresh',exact:true}).click();
  await page.getByRole('heading',{name:'Email and password',exact:true}).waitFor();
  assert.equal(await page.locator('#notice').innerText(),'Your session expired. Sign in to continue.');
  await page.reload({waitUntil:'networkidle'});await page.getByRole('heading',{name:'Email and password',exact:true}).waitFor();
  assert.equal(await page.locator('#notice').innerText(),'');
 });
 await check('no page errors',async()=>assert.deepEqual(errors,[]));
}catch(error){console.error(error);process.exitCode=1;}
finally{
 writeFileSync(path.join(output,'results.json'),JSON.stringify({browser:engine,results,pageErrors:errors,syntheticAPIs:true,persistedEventsSimulated:true},null,2)+'\n');
 console.log(JSON.stringify({browser:engine,lifecyclePassed:results.filter(result=>result.passed).length,lifecycleTotal:results.length,output}));
 await context.close();await browser.close();server.closeAllConnections();await new Promise<void>(resolve=>server.close(()=>resolve()));
}
