import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { initialize, stageDrafts } from '../src/releases.mjs';
import { createConfig } from '../apps/setup/config.mjs';
import { startSupervisor } from '../src/supervisor.mjs';

const delay=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function until(check){for(let i=0;i<500;i++){if(await check())return;await delay(20);}assert.fail('Tool-only loop timed out');}

test('only notifications and tool results drive model calls; plain assistant text cannot finish an objective', async t=>{
  const home=await mkdtemp(join(tmpdir(),'bot-tool-only-'));let server;
  t.after(async()=>{await server?.stop();await rm(home,{recursive:true,force:true});});
  const config=createConfig({disable:'chat,memory,context,scheduler,skills,web-search'});
  config.apps.agent.maxSteps=1; // Historical task limits must not cut off stimuli.
  await initialize(home,config);
  await writeFile(join(home,'drafts/apps/demo-model/behavior.mjs'),`import {appendFile} from 'node:fs/promises';import {join} from 'node:path';
export const createModel=()=>({async complete({messages,tools}){
  await appendFile(join(process.env.BOT_WORKSPACE,'model-calls'),'call\\n');
  const input=messages.findLast(m=>m.role==='user');const results=messages.slice(messages.indexOf(input)+1).filter(m=>m.role==='tool');
  if(tools.length!==1||tools[0].name!=='shell'||tools[0].inputSchema.properties.wait)throw new Error('Unexpected wait mechanism');
  if(results.length===2)return {text:messages.slice(messages.indexOf(input)+1).some(m=>m.metadata?.modelValidation)?'':'OBJECTIVE FINISHED',toolCalls:[],usage:{input:3,cacheRead:0,cacheWrite:0}};
  const call={id:crypto.randomUUID(),name:'shell',arguments:{command:'printf tool-result-'+results.length}};
  return {text:'DISCARD MODEL TEXT',toolCalls:[call],metadata:{nativeAssistant:{role:'assistant',content:[{type:'text',text:'DISCARD MODEL TEXT'},{type:'toolCall',...call}]}}};
}});`);
  await stageDrafts(home);server=await startSupervisor({home,port:0,unsafe:true});
  const api=(path,body)=>fetch(server.url+path,{method:body?'POST':'GET',headers:{authorization:'Bearer '+server.token,'content-type':'application/json'},body:body?JSON.stringify(body):undefined}).then(r=>r.json());
  await until(async()=>(await api('/api/status')).state==='idle');
  await api('/api/messages',{text:'first notification'});
  await until(async()=>(await api('/api/status')).pending===0);
  const history=(await api('/api/messages')).messages;
  assert.equal(history.filter(m=>m.role==='tool').length,2);
  assert.ok(history.filter(m=>m.role==='assistant').every(m=>m.content===''&&m.toolCalls.length&&m.metadata.nativeAssistant.content.every(block=>block.type!=='text')));
  assert.ok(!JSON.stringify(history).includes('OBJECTIVE FINISHED'));
  assert.equal((await readFile(join(home,'workspace/model-calls'),'utf8')).trim().split('\n').length,4);
  await delay(300);
  assert.equal((await readFile(join(home,'workspace/model-calls'),'utf8')).trim().split('\n').length,4);
  assert.equal((await api('/api/events')).events.filter(e=>e.kind==='input.delivered').length,1);
  await api('/api/messages',{text:'second notification'});
  await until(async()=>(await api('/api/status')).pending===0);
  assert.equal((await readFile(join(home,'workspace/model-calls'),'utf8')).trim().split('\n').length,8);
});
