import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, readFile, rm, cp, access, rename } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { initialize, readCurrent, installApp, stageDrafts, beginTrial, rollback, CORE_ROOT } from '../src/releases.mjs';
import { Store } from '../src/store.mjs';
import { createConfig } from '../src/cli.mjs';
import { startSupervisor } from '../src/supervisor.mjs';
import { migrateLiveApps, markLiveCandidate, restoreLiveApps } from '../install/live-apps.mjs';

async function until(check) {
  const deadline=Date.now()+10000;
  while(Date.now()<deadline) { if(await check())return;await new Promise(resolve=>setTimeout(resolve,25)); }
  assert.fail('Live app check timed out');
}
async function packageAt(folder,id,source) {
  await mkdir(folder,{recursive:true});
  await writeFile(join(folder,'app.json'),JSON.stringify({id,version:'1',command:['/bin/sh','main.sh'],preflight:['--preflight']}));
  await writeFile(join(folder,'SKILL.md'),`---\nname: ${id}\ndescription: Isolated live CLI check.\n---\n`);
  await writeFile(join(folder,'main.sh'),source);
}

test('ordinary apps can be installed and edited within a turn without checks, activation or a controller restart',async t=>{
  const home=await mkdtemp(join(tmpdir(),'bot-live-cli-'));let server;
  t.after(async()=>{await server?.stop();await rm(home,{recursive:true,force:true});});
  await initialize(home,createConfig({disable:'chat,context,memory,scheduler,skills,web-search'}));
  const original=await readCurrent(home);
  // A malformed optional app cannot prevent startup or block discovery of others.
  await mkdir(join(home,'drafts/apps/broken'));await writeFile(join(home,'drafts/apps/broken/app.json'),'invalid JSON');
  const configPath=join(home,'drafts/config.json'),config=JSON.parse(await readFile(configPath));
  config.apps.broken={enabled:true};await writeFile(configPath,JSON.stringify(config));
  await writeFile(join(home,'drafts/apps/demo-model/behavior.mjs'),`export const createModel=()=>({async complete({messages}){
    const input=messages.findLast(m=>m.metadata?.input),recent=messages.slice(messages.indexOf(input)+1),results=recent.filter(m=>m.role==='tool');
    const commands=["bot-app ordinary", "printf 'if [ \\\"$1\\\" = --preflight ]; then exit 99; fi\\nprintf new' > \\\"$BOT_APPS/ordinary/main.sh\\\"; bot-app ordinary", "bot-app --help", "bot-app broken", "botctl ordinary state.set '{\\\"key\\\":\\\"live\\\",\\\"value\\\":\\\"bridge works\\\"}'; botctl ordinary state.get '{\\\"key\\\":\\\"live\\\"}'"];
    return results.length<commands.length?{text:'',toolCalls:[{id:crypto.randomUUID(),name:'shell',arguments:{command:commands[results.length]}}]}:{text:'done',toolCalls:[]};
  }});`);
  server=await startSupervisor({home,port:0,unsafe:true});
  const api=async(path,body)=>fetch(server.url+path,{method:body?'POST':'GET',headers:{authorization:'Bearer '+server.token,'content-type':'application/json'},body:body?JSON.stringify(body):undefined}).then(r=>r.json());
  await until(async()=>(await api('/api/status')).state==='idle');
  const source=join(home,'workspace/ordinary-source');
  await packageAt(source,'ordinary',`if [ "$1" = --preflight ]; then printf forbidden > "$BOT_WORKSPACE/preflight-called"; exit 99; fi\nprintf old`);
  await installApp(home,source);
  assert.equal((await api('/api/apps')).apps.ordinary.enabled,true);
  await api('/api/messages',{text:'isolated live edit'});
  await until(async()=>(await api('/api/status')).pending===0);
  const history=(await api('/api/messages')).messages,results=history.filter(m=>m.role==='tool').map(m=>JSON.parse(m.content));
  assert.equal(results[0].stdout,'old');assert.equal(results[1].stdout,'new');
  assert.match(results[2].stdout,/ordinary:/);assert.match(results[2].stdout,/broken: unavailable/);
  assert.equal(results[3].exitCode,1);assert.match(results[3].stderr,/JSON/);assert.ok(results.filter((_,index)=>index!==3).every(result=>result.exitCode===0));assert.match(results[4].stdout,/bridge works/);
  await assert.rejects(access(join(home,'workspace/preflight-called')),{code:'ENOENT'});
  const status=await api('/api/status');assert.equal(status.revision,original.revision);assert.equal(status.trial,null);
  const events=(await api('/api/events')).events;
  assert.equal(events.filter(event=>event.kind==='runtime.ready').length,1);
  assert.equal(events.some(event=>event.kind==='update.trial'),false);
});

test('direct-app migration refreshes unchanged bundled guidance, preserves custom programs and restores exact working copies',async t=>{
  const home=await mkdtemp(join(tmpdir(),'bot-live-migration-'));t.after(()=>rm(home,{recursive:true,force:true}));
  await initialize(home,createConfig({}),{fixedCoreRoot:CORE_ROOT});
  const previous=join(home,'workspace/old-core');await mkdir(previous);await cp(join(CORE_ROOT,'apps'),join(previous,'apps'),{recursive:true});
  const oldGuide='old activation instructions',guide='updates/SKILL.md';
  await writeFile(join(previous,'apps',guide),oldGuide);await writeFile(join(home,'drafts/apps',guide),oldGuide);
  await writeFile(join(home,'drafts/apps/telegram/commands.mjs'),'custom reaction command');
  const configPath=join(home,'drafts/config.json'),config=JSON.parse(await readFile(configPath));delete config.appUpdates;config.coreRoot=previous;await writeFile(configPath,JSON.stringify(config));
  await writeFile(join(home,'workspace/BOT-API.md'),'previous operator contract');
  const before=await readFile(configPath);
  assert.equal(await migrateLiveApps(home,CORE_ROOT),true);await markLiveCandidate(home,'new-core');
  assert.equal(await readFile(join(home,'drafts/apps',guide),'utf8'),await readFile(join(CORE_ROOT,'apps',guide),'utf8'));
  assert.equal(await readFile(join(home,'drafts/apps/telegram/commands.mjs'),'utf8'),'custom reaction command');
  assert.equal(JSON.parse(await readFile(configPath)).appUpdates,'direct');
  assert.equal(await migrateLiveApps(home,CORE_ROOT),false);
  await restoreLiveApps(home,'unrelated');assert.equal(JSON.parse(await readFile(configPath)).appUpdates,'direct');
  await restoreLiveApps(home,'new-core');assert.deepEqual(await readFile(configPath),before);
  assert.equal(await readFile(join(home,'drafts/apps',guide),'utf8'),oldGuide);
  assert.equal(await readFile(join(home,'drafts/apps/telegram/commands.mjs'),'utf8'),'custom reaction command');
  assert.equal(await readFile(join(home,'workspace/BOT-API.md'),'utf8'),'previous operator contract');
});


test('controller rollback preserves independently installed app code and settings',async t=>{
  const home=await mkdtemp(join(tmpdir(),'bot-controller-live-rollback-'));t.after(()=>rm(home,{recursive:true,force:true}));
  await initialize(home,createConfig({}),{fixedCoreRoot:CORE_ROOT});
  await stageDrafts(home);const store=new Store(home);t.after(()=>store.close());await beginTrial(home,store);
  const source=join(home,'workspace/new-app');await packageAt(source,'new-app','printf independent');await installApp(home,source);
  const path=join(home,'drafts/config.json'),config=JSON.parse(await readFile(path));config.apps.memory.maxChars=12345;await writeFile(path,JSON.stringify(config));
  await rollback(home,store);
  const restored=JSON.parse(await readFile(path));assert.equal(restored.apps['new-app'].enabled,true);assert.equal(restored.apps.memory.maxChars,12345);
  assert.equal(await readFile(join(home,'drafts/apps/new-app/main.sh'),'utf8'),'printf independent');
});

async function oldModelApp(t) {
  const home=await mkdtemp(join(tmpdir(),'bot-model-rename-'));t.after(()=>rm(home,{recursive:true,force:true}));
  await initialize(home,createConfig({}),{fixedCoreRoot:CORE_ROOT});
  const previous=join(home,'workspace/old-core');await mkdir(previous);await cp(join(CORE_ROOT,'apps'),join(previous,'apps'),{recursive:true});
  for(const apps of [join(previous,'apps'),join(home,'drafts/apps')]) {
    await rename(join(apps,'model-provider'),join(apps,'pi-model'));
    const manifestPath=join(apps,'pi-model/app.json'),manifest=JSON.parse(await readFile(manifestPath));
    manifest.id='pi-model';manifest.description='pi-model behavior application';await writeFile(manifestPath,JSON.stringify(manifest));
    await writeFile(join(apps,'pi-model/SKILL.md'),'---\nname: pi-model\ndescription: pi-model behavior application.\n---\nRun `bot-app pi-model --help`.\n');
  }
  const configPath=join(home,'drafts/config.json'),config=JSON.parse(await readFile(configPath));
  config.coreRoot=previous;config.apps['pi-model']={enabled:true,provider:'anthropic',model:'retained-model',systemPrompt:'retained prompt'};
  delete config.apps['model-provider'];config.apps.agent.modelApp='pi-model';await writeFile(configPath,JSON.stringify(config));
  return home;
}

test('model app rename preserves custom code, settings and history and rolls back exactly',async t=>{
  const home=await oldModelApp(t),configPath=join(home,'drafts/config.json'),before=await readFile(configPath);
  const behavior='export const customModel=true;';await writeFile(join(home,'drafts/apps/pi-model/behavior.mjs'),behavior);
  const store=new Store(home);store.append({role:'user',content:'existing real conversation'});store.setState('app:pi-model','retained',{value:7});
  const history=store.history(),state=store.snapshot();store.close();
  await writeFile(join(home,'control/credential-fixture'),'retained credential');
  assert.equal(await migrateLiveApps(home,CORE_ROOT),true);await markLiveCandidate(home,'renamed-core');
  const config=JSON.parse(await readFile(configPath));
  assert.equal(config.apps.agent.modelApp,'model-provider');assert.equal(config.apps['pi-model'],undefined);
  assert.deepEqual(config.apps['model-provider'],{enabled:true,provider:'anthropic',model:'retained-model',systemPrompt:'retained prompt',stateNamespace:'app:pi-model'});
  assert.equal(await readFile(join(home,'drafts/apps/model-provider/behavior.mjs'),'utf8'),behavior);
  assert.equal(JSON.parse(await readFile(join(home,'drafts/apps/model-provider/app.json'))).id,'model-provider');
  assert.match(await readFile(join(home,'drafts/apps/model-provider/SKILL.md'),'utf8'),/internal model/);
  await assert.rejects(access(join(home,'drafts/apps/pi-model')),{code:'ENOENT'});
  const after=new Store(home);assert.deepEqual(after.history(),history);assert.deepEqual(after.snapshot(),state);after.close();
  assert.equal(await readFile(join(home,'control/credential-fixture'),'utf8'),'retained credential');
  assert.equal(await migrateLiveApps(home,CORE_ROOT),false);
  await restoreLiveApps(home,'renamed-core');assert.deepEqual(await readFile(configPath),before);
  assert.equal(await readFile(join(home,'drafts/apps/pi-model/behavior.mjs'),'utf8'),behavior);
  await assert.rejects(access(join(home,'drafts/apps/model-provider')),{code:'ENOENT'});
});

test('model app rename rejects an existing replacement before changing drafts',async t=>{
  const home=await oldModelApp(t),configPath=join(home,'drafts/config.json'),before=await readFile(configPath);
  await cp(join(CORE_ROOT,'apps/model-provider'),join(home,'drafts/apps/model-provider'),{recursive:true});
  await assert.rejects(migrateLiveApps(home,CORE_ROOT),/Both pi-model and model-provider exist/);
  assert.deepEqual(await readFile(configPath),before);
  await assert.rejects(access(join(home,'control/live-apps-migration.json')),{code:'ENOENT'});
});
