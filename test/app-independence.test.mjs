import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, readdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { initialize, stageDrafts, beginTrial, rollback, readCurrent, CORE_ROOT } from '../src/releases.mjs';
import { Store } from '../src/store.mjs';
import { createConfig } from '../apps/setup/config.mjs';
import { migrateConfig } from '../apps/setup/migrate.mjs';
import { startSupervisor } from '../src/supervisor.mjs';

test('controller snapshots and rollback preserve independent app files, settings, state and contributions',async t=>{
  const home=await mkdtemp(join(tmpdir(),'bot-independent-'));t.after(()=>rm(home,{recursive:true,force:true}));
  await initialize(home,createConfig({}),{fixedCoreRoot:CORE_ROOT});
  const store=new Store(home);t.after(()=>store.close());
  store.setState('app:agent','revision','before');store.setState('app:memory','memory','before');
  const candidate=await stageDrafts(home);
  assert.deepEqual(await readdir(join(home,'revisions',candidate,'apps')),['agent']);
  await beginTrial(home,store);
  store.setState('app:agent','revision','candidate');store.setState('app:memory','memory','updated independently');store.setState('app:new-app','value','new state');
  store.contribute('independent','new reference','new-app');
  await rollback(home,store);
  assert.equal(store.getState('app:agent','revision'),'before');assert.equal(store.getState('app:memory','memory'),'updated independently');assert.equal(store.getState('app:new-app','value'),'new state');
  assert.equal(store.contribute('independent','new reference','new-app'),null);
});

test('configuration migration preserves custom provider and context settings and is idempotent',()=>{
  const old={controller:'agent',model:{provider:'fixture',model:'selected'},apps:{agent:{enabled:true,modelApp:'provider',contextApp:'context',contributors:['memory'],projections:['chat','telegram'],services:['chat','telegram'],tickApps:['scheduler']},provider:{enabled:true,systemPrompt:'custom'},context:{enabled:true,maxInputChars:12345},chat:{enabled:true},telegram:{enabled:true}}};
  const next=migrateConfig(old);
  assert.equal(next.apps.provider.systemPrompt,'custom');assert.equal(next.apps.provider.model,'selected');assert.equal(next.apps.context.maxInputChars,12345);
  assert.deepEqual(next.apps.agent.services,[['bot-app','chat','watch'],['bot-app','telegram','watch'],['bot-app','scheduler','watch']]);
  assert.equal(next.model,undefined);assert.deepEqual(migrateConfig(next),next);assert.equal(old.model.model,'selected');
});

test('scheduler enqueues due work while a model hook is still active', {timeout:15000},async t=>{
  const home=await mkdtemp(join(tmpdir(),'bot-scheduler-active-'));let server;
  t.after(async()=>{await server?.stop();await rm(home,{recursive:true,force:true});});
  const config=createConfig({disable:'chat,context,memory,skills,web-search'});
  await initialize(home,config,{fixedCoreRoot:CORE_ROOT});
  await writeFile(join(home,'drafts/apps/demo-model/behavior.mjs'),`import {readFile,writeFile} from 'node:fs/promises';import {join} from 'node:path';export const createModel=()=>({async complete(){await writeFile(join(process.env.BOT_WORKSPACE,'model-running'),'');while(true){try{await readFile(join(process.env.BOT_WORKSPACE,'release-model'));break;}catch{await new Promise(r=>setTimeout(r,20));}}return {text:'',toolCalls:[]};}});`);
  const store=new Store(home);
  store.setState('app:scheduler','schedules',[{id:'during-model',text:'isolated scheduled work',status:'active',nextAt:new Date(Date.now()+2000).toISOString()}]);store.close();
  server=await startSupervisor({home,port:0,unsafe:true});
  const api=(path,body)=>fetch(server.url+path,{method:body?'POST':'GET',headers:{authorization:'Bearer '+server.token,'content-type':'application/json'},body:body?JSON.stringify(body):undefined}).then(r=>r.json());
  await api('/api/messages',{text:'isolated active work'});
  const until=async check=>{for(let n=0;n<200;n++){if(await check())return;await new Promise(r=>setTimeout(r,30));}assert.fail('Scheduling check timed out');};
  await until(async()=>(await api('/api/status')).state==='working');
  await until(async()=>(await api('/api/events')).events.some(e=>e.kind==='input.accepted'&&e.payload.metadata?.source==='scheduler'));
  assert.equal((await api('/api/status')).state,'working');
  await writeFile(join(home,'workspace/release-model'),'');
  await until(async()=>(await api('/api/status')).pending===0);
});

test('scheduler transactions retain concurrent CLI writes and import legacy state only once',async t=>{
  const {spawn}=await import('node:child_process');
  const {transaction}=await import('../apps/scheduler/database.mjs');
  const workspace=await mkdtemp(join(tmpdir(),'bot-scheduler-lock-'));t.after(()=>rm(workspace,{recursive:true,force:true}));
  const api={paths:{workspace},state:{get:async()=>[{id:'legacy'}]}};
  await transaction(api,async locked=>assert.deepEqual(await locked.state.get(),[{id:'legacy'}]));
  const program=`import {transaction} from ${JSON.stringify(new URL('../apps/scheduler/database.mjs',import.meta.url).href)};const [workspace,id]=process.argv.slice(1);await transaction({paths:{workspace},state:{get:async()=>{throw Error('Already imported');}}},async api=>{const items=await api.state.get();console.log('locked');await new Promise(r=>setTimeout(r,250));await api.state.set('schedules',[...items,{id}]);});`;
  const start=id=>{const child=spawn(process.execPath,['--input-type=module','-e',program,workspace,id],{stdio:['ignore','pipe','pipe']});let stderr='';child.stderr.on('data',data=>stderr+=data);return {child,done:new Promise((resolve,reject)=>{child.once('error',reject);child.once('exit',code=>code===0?resolve():reject(new Error(stderr)));})};};
  const first=start('one');await new Promise(resolve=>first.child.stdout.once('data',resolve));
  const second=start('two');await Promise.all([first.done,second.done]);
  await transaction(api,async locked=>assert.deepEqual(await locked.state.get(),[{id:'legacy'},{id:'one'},{id:'two'}]));
});
