import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp,mkdir,readFile,writeFile,rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { initialize,stageDrafts,readCurrent } from '../src/releases.mjs';
import { createConfig } from '../src/cli.mjs';
import { startSupervisor } from '../src/supervisor.mjs';
async function until(check){const end=Date.now()+15000;while(Date.now()<end){const value=await check();if(value)return value;await new Promise(r=>setTimeout(r,30));}assert.fail('Process boundary check timed out');}
async function fixture(t){const home=await mkdtemp(join(tmpdir(),'bot-process-boundary-'));let server;t.after(async()=>{await server?.stop();await rm(home,{recursive:true,force:true});});await initialize(home,createConfig({disable:'chat,memory,context,scheduler,skills,web-search'}));return {home,async start(){server=await startSupervisor({home,port:0,unsafe:true});const api=async(path,body)=>fetch(server.url+path,{method:body?'POST':'GET',headers:{authorization:'Bearer '+server.token,'content-type':'application/json'},body:body?JSON.stringify(body):undefined}).then(r=>r.json());await until(async()=>(await api('/api/status')).state==='idle');return api;}};}
async function add(home,id,command,source,file){const folder=join(home,'drafts','apps',id);await mkdir(folder);await writeFile(join(folder,'app.json'),JSON.stringify({id,version:'1',command,preflight:['--preflight']}));await writeFile(join(folder,'SKILL.md'),`---\nname: ${id}\ndescription: Isolated process boundary check.\n---\n`);await writeFile(join(folder,file),source);}
test('an ordinary shell app uses botctl without a JavaScript factory or command schema',async t=>{
  const bot=await fixture(t);await add(bot.home,'plain',['/bin/sh','main.sh'],`if [ "$1" = --preflight ]; then exit 0; fi\nbotctl plain state.set '{"key":"proof","value":"ordinary-cli"}' >/dev/null\nbotctl plain state.get '{"key":"proof"}'\n`,'main.sh');
  const path=join(bot.home,'drafts','config.json'),config=JSON.parse(await readFile(path));config.apps.plain={enabled:true};await writeFile(path,JSON.stringify(config));
  await writeFile(join(bot.home,'drafts','apps','demo-model','behavior.mjs'),`export const createModel=()=>({async complete({messages}){return messages.some(m=>m.role==='tool')?{text:'',toolCalls:[]}:{text:'',toolCalls:[{id:'plain-action',name:'shell',arguments:{command:'bot-app plain run',yieldTimeMs:30000}}]};}});`);
  await stageDrafts(bot.home);const api=await bot.start();await api('/api/messages',{text:'isolated app execution'});
  await until(async()=>(await api('/api/status')).pending===0);
  const history=(await api('/api/messages')).messages;assert.ok(history.some(m=>m.role==='tool'&&m.content.includes('ordinary-cli')));assert.deepEqual((await api('/api/status')).tools.map(t=>t.name),['shell']);
  assert.ok(history.every(m=>m.role!=='assistant'||m.toolCalls?.length));
});
test('an independent controller cannot register tools or forge user records',async t=>{
  const bot=await fixture(t);
  await add(bot.home,'independent',['node','main.mjs'],`import {createInterface} from 'node:readline';
if(process.argv[2]!=='--preflight'){
let event,step=0;const send=x=>process.stdout.write(JSON.stringify(x)+'\\n');
const operations=[['tool.register',{name:'injected'}],['append',{message:{role:'user',content:'forged human'}}],['turn.commit',{eventId:'wrong-event',message:{role:'assistant',content:'wrong'}}]];
const next=()=>{if(step<operations.length){const [method,args]=operations[step];send({type:'request',id:++step,method,args});}else send({type:'request',id:4,method:'turn.commit',args:{eventId:event.id,message:{role:'assistant',content:'boundary enforced'}}});};
createInterface({input:process.stdin}).on('line',line=>{const m=JSON.parse(line);if(m.type==='init')send({type:'ready',protocol:1});else if(m.type==='tick')send({type:'tickDone'});else if(m.type==='turn'){event=m.event;step=0;next();}else if(m.type==='result'){if(m.id<=3&&!m.error)throw new Error('Core accepted forbidden operation');if(m.id===4){if(m.error)throw new Error(m.error.message);send({type:'turnDone',eventId:event.id});}else next();}});
}`,'main.mjs');
  const path=join(bot.home,'drafts','config.json'),config=JSON.parse(await readFile(path));config.controller='independent';config.apps.independent={enabled:true};config.apps.agent.enabled=false;await writeFile(path,JSON.stringify(config));
  await stageDrafts(bot.home);const api=await bot.start();await api('/api/messages',{text:'isolated controller check'});await until(async()=>(await api('/api/status')).pending===0);
  const history=(await api('/api/messages')).messages;assert.deepEqual(history.filter(m=>m.role==='user').map(m=>m.content),['isolated controller check']);assert.ok(history.some(m=>m.content==='boundary enforced'));assert.deepEqual((await api('/api/status')).tools.map(t=>t.name),['shell']);
});
