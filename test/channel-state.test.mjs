import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, readFile, writeFile, rename } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { TelegramStore } from '../apps/telegram/database.mjs';
import { receive, send, project } from '../apps/telegram/behavior.mjs';
import { telegramAPI } from '../apps/telegram/api.mjs';
import { ChatStore, notification } from '../apps/chat/database.mjs';
import { commands as chatCommands } from '../apps/chat/commands.mjs';
import { commands as telegramCommands } from '../apps/telegram/commands.mjs';
import { watch as chatWatch } from '../apps/chat/behavior.mjs';
import { initialize, stageDrafts } from '../src/releases.mjs';
import { createConfig } from '../apps/setup/config.mjs';
import { startSupervisor } from '../src/supervisor.mjs';

async function folder(t) {const dir=await mkdtemp(join(tmpdir(),'bot-channel-state-'));t.after(()=>rm(dir,{recursive:true,force:true}));return dir;}
async function telegram(t) {const store=new TelegramStore(await folder(t));store.set('identity',{id:123});t.after(()=>store.close());return store;}
const message=(id,text='human message',user=42,type='private')=>({update_id:id,message:{message_id:id,date:1,chat:{id:user,type},from:{id:user,is_bot:false},text}});
async function until(check) {const end=Date.now()+20000;while(Date.now()<end){if(await check())return;await new Promise(resolve=>setTimeout(resolve,25));}assert.fail('Channel check timed out');}

test('communication reads include new arrivals while chat replies retain their input association', async t => {
  const workspace = await folder(t);
  const chat = new ChatStore(workspace);t.after(() => chat.close());
  chat.accept('chat:first', 'first');chat.accept('chat:second', 'second');
  const history = [
    {role:'user',content:notification('chat:first'),metadata:{eventId:'chat:first',source:'chat'}},
    {role:'user',content:notification('chat:second'),metadata:{eventId:'chat:second',source:'chat'}},
    {role:'context',content:'Resuming first',metadata:{eventId:'chat:first',source:'chat',resumedInput:true,input:true}},
  ];
  const commands = chatCommands({paths:{workspace},history:async()=>history});
  assert.deepEqual((await commands.read.execute({})).messages.map(m=>m.text), ['first','second']);
  await commands.send.execute({text:'reply to first'},{callId:'resumed-reply'});
  assert.equal(chat.db.prepare("SELECT event_id FROM messages WHERE sender='bot'").get().event_id, 'chat:first');

  const telegram = new TelegramStore(join(workspace,'telegram'));t.after(() => telegram.close());
  telegram.set('binding',{chatId:42});
  telegram.message(message(1,'first').message,'human');
  telegram.message(message(2,'second').message,'human');
  const telegramHistory = [
    {role:'user',metadata:{source:'telegram',messageId:2}},
    {role:'context',metadata:{source:'telegram',messageId:1,resumedInput:true,input:true}},
  ];
  const reads = telegramCommands({paths:{workspace},history:async()=>telegramHistory});
  assert.deepEqual((await reads.read.execute({})).messages.map(m=>m.text), ['first','second']);
});

test('Telegram pairing authorizes one private human and enqueues references without copying conversation text',async t=>{
  const store=await telegram(t),events=[];store.set('pairing',{code:'private-code',expires:Date.now()+10000});
  const updates=[message(1,'/start wrong',99),message(2,'/start private-code',99,'group'),message(3,'/start private-code'),message(4,'other person',99),message(5,'actual human text')];
  await receive(store,async()=>updates,event=>events.push(event));
  assert.deepEqual(store.get('binding'),{chatId:42,userId:42});assert.equal(store.get('pairing'),null);
  assert.equal(events.length,2);assert.ok(events.every(event=>!event.text.includes('actual human text')));
  assert.deepEqual(store.read().messages.map(m=>m.text),['/start','actual human text']);assert.equal(store.get('offset'),6);
  const input={id:1,role:'user',content:events[1].text,metadata:{source:'telegram'}};
  assert.equal(project([input])[0].role,'context');assert.equal(project([input])[0].metadata.input,true);
});
test('Telegram retains an unacknowledged update and retries the same notification after enqueue failure',async t=>{
  const store=await telegram(t);store.set('binding',{chatId:42,userId:42});let first;
  await assert.rejects(receive(store,async()=>[message(7)],event=>{first=event;throw new Error('bridge lost');}),/bridge lost/);
  assert.equal(store.get('offset',0),0);assert.equal(store.read().messages.length,1);
  const events=[];await receive(store,async()=>[message(7)],event=>events.push(event));
  assert.deepEqual(events,[first]);assert.equal(store.get('offset'),8);assert.equal(store.read().messages.length,1);
});
test('operator-authorized Telegram username pairs without a copied command and never authorizes another user',async t=>{
  const store=await telegram(t),events=[];store.set('authorizedUsername','human_owner');
  const stranger=message(1,'hello',99);stranger.message.from.username='other_person';
  const owner=message(2,'hello from my phone');owner.message.from.username='Human_Owner';
  await receive(store,async()=>[stranger,owner],event=>events.push(event));
  assert.deepEqual(store.get('binding'),{chatId:42,userId:42,username:'Human_Owner'});assert.equal(store.get('authorizedUsername'),null);
  assert.equal(events.length,1);assert.equal(store.read().messages[0].text,'hello from my phone');
});
test('Telegram send receipts deduplicate successful actions and prevent repeating uncertain deliveries',async t=>{
  const store=await telegram(t);store.set('binding',{chatId:42,userId:42});let sends=0;
  const api=async(_method,args)=>{sends++;return {message_id:20,date:2,chat:{id:42},text:args.text};};
  assert.deepEqual(await send(store,api,{text:'reply',replyTo:7},'call'),{messageId:20});
  assert.deepEqual(await send(store,api,{text:'reply',replyTo:7},'call'),{messageId:20,duplicate:true});assert.equal(sends,1);
  await assert.rejects(send(store,api,{text:'different'},'call'),/different content/);
  await assert.rejects(send(store,async()=>{sends++;throw new Error('lost response');},{text:'uncertain'},'unknown'),/lost response/);
  await assert.rejects(send(store,api,{text:'uncertain'},'unknown'),/UNKNOWN/);assert.equal(sends,2);
  store.startDelivery('interrupted','before crash');await assert.rejects(send(store,api,{text:'before crash'},'interrupted'),/UNKNOWN/);
  assert.equal(store.read().messages.at(-1).text,'reply');
});
test('Telegram API errors redact secrets and distinguish explicit rejections from lost responses',async()=>{
  const token='123:private-token';
  const failure=telegramAPI(token,{fetchImpl:async()=>{throw new Error('https://api.telegram.org/bot'+token);}});
  await assert.rejects(failure('sendMessage'),error=>error.unknown&&!error.message.includes(token));
  const rejected=telegramAPI(token,{fetchImpl:async()=>({json:async()=>({ok:false,error_code:429,parameters:{retry_after:5},description:token})})});
  await assert.rejects(rejected('sendMessage'),error=>error.rejected&&error.retryAfter===5&&!error.message.includes(token));
});
test('terminal app owns durable history, scopes active input and recovers notification delivery after frontend exit',async t=>{
  const workspace=await folder(t),store=new ChatStore(workspace);t.after(()=>store.close());
  store.accept('chat:first','first actual text');store.accept('chat:second','second actual text');
  assert.deepEqual(store.read({eventId:'chat:first'}).messages.map(m=>m.text),['first actual text']);
  const saved=store.send('reply','reply to first','chat:first');
  assert.deepEqual(store.send('reply','reply to first','chat:first'),{...saved,duplicate:true});
  assert.deepEqual(store.read({eventId:'chat:first'}).messages.map(m=>m.text),['first actual text','reply to first']);
  const controller=new AbortController(),events=[];
  await chatWatch({paths:{workspace},history:async()=>[],enqueue:async event=>{events.push(event);if(events.length===2)controller.abort();}},{signal:controller.signal});
  assert.deepEqual(events.map(e=>e.text),[notification('chat:first'),notification('chat:second')]);assert.equal(store.pending().length,0);
});
test('background Telegram notifications steer the next model call during active work and survive restart', {timeout:30000},async t=>{
  const home=await folder(t);await initialize(home,createConfig({disable:'chat,memory,context,scheduler,skills,web-search'}));
  const draft=join(home,'drafts','apps','telegram');
  await writeFile(join(draft,'main.mjs'),`import {runCLI} from '../_lib/cli.mjs';import {commands} from './commands.mjs';import {receive,project} from './behavior.mjs';import {TelegramStore} from './database.mjs';import {readFile} from 'node:fs/promises';import {join} from 'node:path';
await runCLI({commands,operations:{project:async(_api,input)=>{if(input.messages.some(m=>m.role==='tool')){await import('node:fs/promises').then(fs=>fs.writeFile(join(process.env.BOT_WORKSPACE,'preparing-context'),''));while(true){try{await readFile(join(process.env.BOT_WORKSPACE,'continue-context'));break;}catch{await new Promise(r=>setTimeout(r,20));}}}return project(input.messages);},watch:async api=>{const store=new TelegramStore(join(api.paths.workspace,'telegram'));while(true){const updates=JSON.parse(await readFile(join(api.paths.workspace,'updates.json')));await receive(store,async()=>updates.filter(u=>u.update_id>=store.get('offset',0)),event=>api.enqueue(event));await new Promise(r=>setTimeout(r,50));}}}});`);
  const configPath=join(home,'drafts','config.json'),config=JSON.parse(await readFile(configPath));
  config.apps.telegram={enabled:true};config.apps.agent.services=[['bot-app','telegram','watch']];config.apps.context={enabled:true,compaction:false,contributors:[],projections:['telegram']};config.apps.agent.hooks.prepare=['bot-app','context','prepare','--stdin'];await writeFile(configPath,JSON.stringify(config));
  await writeFile(join(home,'workspace','updates.json'),'[]');
  const store=new TelegramStore(join(home,'workspace','telegram'));store.set('identity',{id:123});store.set('binding',{chatId:42,userId:42});store.close();
  await writeFile(join(home,'drafts','apps','demo-model','behavior.mjs'),`export const createModel=()=>({async complete({messages}){if(messages.some(m=>m.role==='tool')){const inputs=messages.filter(m=>m.metadata?.input);if(inputs.length!==3)throw new Error('Follow-up notification missing from next model call');return {text:'',toolCalls:[]};}return {text:'',toolCalls:[{id:'read-after-followup',name:'shell',arguments:{command:"touch tool-running; while [ ! -f continue-tool ]; do sleep 0.02; done; bot-app telegram read '{}'",yieldTimeMs:30000}}]};}});`);
  await stageDrafts(home);let server=await startSupervisor({home,port:0,unsafe:true});t.after(()=>server.stop());
  const api=async path=>fetch(server.url+path,{headers:{authorization:'Bearer '+server.token}}).then(r=>r.json());
  await until(async()=>(await api('/api/status')).state==='idle');
  const updates=async entries=>{const target=join(home,'workspace','updates.json');await writeFile(target+'.tmp',JSON.stringify(entries));await rename(target+'.tmp',target);};
  await updates([message(1,'first telegram text')]);
  await until(async()=>(await api('/api/events')).events.some(e=>e.kind==='tool.started'));
  await updates([message(1,'first telegram text'),message(2,'second telegram text')]);
  await until(async()=>(await api('/api/events')).events.some(event=>event.kind==='input.accepted'&&event.payload.metadata?.source==='telegram'&&event.payload.metadata.updateId===2));
  await writeFile(join(home,'workspace','continue-tool'),'');
  await until(async()=>{try{await readFile(join(home,'workspace','preparing-context'));return true;}catch{return false;}});
  await updates([message(1,'first telegram text'),message(2,'second telegram text'),message(3,'latest direction')]);
  // Shell completion may also be queued while context preparation is paused.
  // Observe the actual Telegram receipt instead of an unstable total count.
  await until(async()=>(await api('/api/events')).events.some(event=>event.kind==='input.accepted'&&event.payload.metadata?.source==='telegram'&&event.payload.metadata.updateId===3));
  await writeFile(join(home,'workspace','continue-context'),'');
  await until(async()=>(await api('/api/status')).pending===0);
  let history=(await api('/api/messages')).messages;
  assert.deepEqual(history.filter(m=>m.role==='user'&&m.metadata.source==='telegram').map(m=>m.metadata.source),['telegram','telegram','telegram']);
  assert.ok(history.filter(m=>m.metadata?.source==='telegram').every(m=>!m.content.includes('telegram text')));
  const firstRead=JSON.parse(history.find(m=>m.role==='tool').content);assert.equal(firstRead.outcome,'running');
  const completion=history.find(m=>m.metadata?.source==='shell');assert.ok(completion.content.includes('first telegram text'));assert.ok(completion.content.includes('second telegram text'));
  assert.equal(history.filter(m=>m.role==='tool').length,1);
  assert.equal(history.filter(m=>m.role==='assistant'&&!m.toolCalls?.length).length,0);
  assert.ok(history.every(m=>m.content!=='steered by all messages'));
  assert.deepEqual((await api('/api/status')).tools.map(tool=>tool.name),['shell']);
  await server.stop();server=await startSupervisor({home,port:0,unsafe:true});await until(async()=>(await api('/api/status')).state==='idle');
  history=(await api('/api/messages')).messages;assert.equal(history.filter(m=>m.role==='user'&&m.metadata.source==='telegram').length,3);assert.equal(history.filter(m=>m.metadata?.source==='shell').length,1);
  const persisted=new TelegramStore(join(home,'workspace','telegram'));assert.equal(persisted.read().messages.length,3);assert.equal(persisted.get('offset'),4);persisted.close();
});
