import { ChatStore } from '../apps/chat/database.mjs';
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, cp, writeFile, readFile, mkdir } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { initialize, readCurrent, stageDrafts } from '../src/releases.mjs';
import { createConfig } from '../apps/setup/config.mjs';
import { appManifest } from '../src/app-package.mjs';
import { startSupervisor } from '../src/supervisor.mjs';

async function fixture(t) {
  const home=await mkdtemp(join(tmpdir(),'bot-apps-'));
  t.after(()=>rm(home,{recursive:true,force:true}));
  await initialize(home,createConfig({}));return home;
}
async function until(check) { for(let i=0;i<1800;i++){const value=await check();if(value)return value;await new Promise(r=>setTimeout(r,25));}assert.fail('Isolated app check timed out'); }
test('plugin configuration and JavaScript entry manifests are rejected',async t=>{
  const home=await fixture(t),path=join(home,'drafts','config.json');const config=JSON.parse(await readFile(path));config.modules={injected:{enabled:true}};await writeFile(path,JSON.stringify(config));
  await assert.rejects(stageDrafts(home),/Plugin modules are unsupported/);
  const app=join(home,'drafts','apps','demo-model'),manifest=JSON.parse(await readFile(join(app,'app.json')));manifest.entry='commands.mjs';await writeFile(join(app,'app.json'),JSON.stringify(manifest));
  await assert.rejects(appManifest(app),/JavaScript module entries are unsupported/);
  delete manifest.entry;manifest.preflight='--preflight';await writeFile(join(app,'app.json'),JSON.stringify(manifest));
  await assert.rejects(appManifest(app),/preflight must be an argv array/);
});

test('new app packages activate with a skill, preserve versions, and never add model tools',async t=>{
  const home=await fixture(t);await cp(new URL('../examples/local-time',import.meta.url),join(home,'drafts','apps','local-time'),{recursive:true});
  const configPath=join(home,'drafts','config.json');const config=JSON.parse(await readFile(configPath));config.apps['local-time']={enabled:true};await writeFile(configPath,JSON.stringify(config));
  const candidate=await stageDrafts(home);const current=await readCurrent(home);await assert.rejects(appManifest(join(home,'revisions',candidate,'apps','local-time')),{code:'ENOENT'});
  assert.match(await readFile(join(home,'drafts','apps','local-time','SKILL.md'),'utf8'),/local-time/);
  await assert.rejects(readFile(join(home,'revisions',current.revision,'apps','local-time','app.json')),{code:'ENOENT'});
  await writeFile(join(home,'drafts','apps','local-time','commands.mjs'),'invalid updated draft');
  assert.equal(await readFile(join(home,'drafts','apps','local-time','commands.mjs'),'utf8'),'invalid updated draft');
});

test('isolated conversation uses shell apps and shared durable state without native capability tools',async t=>{
  const home=await fixture(t);
  // The offline fixture proves app commands through actual supervised shell processes.
  const path=join(home,'drafts','apps','demo-model','behavior.mjs');
  await writeFile(path,`export const createModel=api=>({async complete({messages,tools}){
    if(tools.length!==1||tools[0].name!=='shell')throw new Error('Unexpected tools');
    const input=messages.findLast(m=>m.metadata?.input);const recent=messages.slice(messages.indexOf(input)+1);
    const results=recent.filter(m=>m.role==='tool');const steps=[
      "bot-app memory update '{\\\"section\\\":\\\"notes\\\",\\\"value\\\":\\\"persisted through app\\\"}'",
      "bot-app memory get '{}'", "bot-app skills list '{}'", "bot-app scheduler create '{\\\"text\\\":\\\"future isolated check\\\",\\\"at\\\":\\\"2099-01-01T00:00:00Z\\\"}'",
      "bot-app scheduler list '{}'", "bot-app context compact '{}'", "bot-app chat read '{}'", "bot-app chat send '{\\\"text\\\":\\\"isolated app success\\\"}'"
    ];
    if(results.some(m=>m.isError||JSON.parse(m.content).exitCode!==0))throw new Error('App failed: '+JSON.stringify(results));
    return results.length<steps.length?{text:'',toolCalls:[{id:crypto.randomUUID(),name:'shell',arguments:{command:steps[results.length],yieldTimeMs:30000}}]}:{text:'',toolCalls:[]};
  }});`);
  await stageDrafts(home);const server=await startSupervisor({home,port:0,unsafe:true});t.after(()=>server.stop());
  const api=async(path,body)=>fetch(server.url+path,{method:body?'POST':'GET',headers:{authorization:'Bearer '+server.token,'content-type':'application/json'},body:body?JSON.stringify(body):undefined}).then(r=>r.json());
  await until(async()=>(await api('/api/status')).state==='idle');
  await api('/api/messages',{text:'isolated app fixture'});
  await until(()=>{const store=new ChatStore(join(home,'workspace'));try{return store.read().messages.some(m=>m.text==='isolated app success');}finally{store.close();}});
  await until(async()=>(await api('/api/status')).pending===0);
  const history=(await api('/api/messages')).messages;
  assert.ok(history.filter(m=>m.role==='tool').every(m=>m.toolName==='shell'));
  assert.ok(history.some(m=>m.role==='tool'&&m.content.includes('persisted through app')));
  assert.deepEqual((await api('/api/status')).tools.map(x=>x.name),['shell']);
});
