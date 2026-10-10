import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, writeFile, readFile, access } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import execution from '../src/execution.mjs';
import { initialize, stageDrafts } from '../src/releases.mjs';
import { createConfig } from '../src/cli.mjs';
import { startSupervisor } from '../src/supervisor.mjs';
import { Store } from '../src/store.mjs';
import { createModel } from '../apps/demo-model/behavior.mjs';

const delay=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function until(check) {
  for(let i=0;i<500;i++){const result=await check();if(result)return result;await delay(20);}
  assert.fail('Async shell check timed out');
}
async function shellFixture(t,config={}) {
  const workspace=await mkdtemp(join(tmpdir(),'bot-async-shell-'));
  const completed=[],processes=new Set();let stop,inputPending=false;
  const shell=execution({config,paths:{workspace},trackProcess:async pid=>processes.add(pid),releaseProcess:async pid=>processes.delete(pid),
    on:(name,fn)=>{if(name==='stop')stop=fn;},shouldYield:()=>inputPending,
    completed:async(sessionId,result)=>completed.push({sessionId,result}),onError:error=>assert.fail(error.message)});
  t.after(async()=>{await stop();await rm(workspace,{recursive:true,force:true});});
  return {shell,workspace,completed,processes,stop,
    interrupt:()=>{inputPending=true;shell.interruptWaits();},resume:()=>{inputPending=false;},
    call:args=>shell.execute(args,{signal:new AbortController().signal,callId:'isolated-call'})};
}

test('incoming input interrupts launch and polling waits without killing the command',async t=>{
  const f=await shellFixture(t);
  const launch=f.call({command:'printf started > started; read line; printf "%s" "$line"',yieldTimeMs:30_000});
  await until(()=>access(join(f.workspace,'started')).then(()=>true,()=>false));
  const start=Date.now();f.interrupt();const first=await launch;
  assert.ok(Date.now()-start<1000);assert.equal(first.outcome,'running');assert.equal(f.processes.size,1);
  // Input received before a following call starts must also prevent a long wait.
  const pendingStart=Date.now();const pending=await f.call({sessionId:first.sessionId,yieldTimeMs:30_000});
  assert.ok(Date.now()-pendingStart<1000);assert.equal(pending.outcome,'running');
  f.resume();const poll=f.call({sessionId:first.sessionId,yieldTimeMs:30_000});
  await delay(30);f.interrupt();assert.equal((await poll).outcome,'running');
  f.resume();const final=await f.call({sessionId:first.sessionId,chars:'finished\n',yieldTimeMs:1000});
  assert.equal(final.stdout,'finished');assert.equal(final.exitCode,0);assert.equal(f.processes.size,0);
  assert.equal(f.completed.length,0,'a waiting caller already receives completion');
});

test('yielded commands notify once with unread UTF-8 output and keep the final result available',async t=>{
  const f=await shellFixture(t);
  const command=`'${process.execPath}' -e 'process.stdout.write(Buffer.from([0xf0,0x9f]));setTimeout(()=>{process.stdout.write(Buffer.from([0x9a,0x80]));process.stderr.write("detail");process.exit(7)},200)'`;
  const first=await f.call({command,yieldTimeMs:80});assert.equal(first.outcome,'running');
  await until(()=>f.completed.length);
  const [{sessionId,result}]=f.completed;
  assert.equal(sessionId,first.sessionId);assert.equal(result.stdout,'🚀');assert.equal(result.stderr,'detail');assert.equal(result.exitCode,7);
  assert.deepEqual(await f.call({sessionId,yieldTimeMs:0}),result);
  await assert.rejects(f.call({sessionId}),/not found/);
  await delay(30);assert.equal(f.completed.length,1);
});

test('completion notifications preserve output bounds and uncertain timeout outcomes',async t=>{
  const f=await shellFixture(t,{outputBytes:1024});
  await f.call({command:'sleep 0.1; yes x | head -c 4000',yieldTimeMs:0});
  await until(()=>f.completed.length===1);
  assert.equal(f.completed[0].result.truncated,true);assert.equal(Buffer.byteLength(f.completed[0].result.stdout),1024);
  await f.call({command:'sleep 10',timeoutMs:100,yieldTimeMs:0});
  await until(()=>f.completed.length===2);
  assert.equal(f.completed[1].result.outcome,'unknown');assert.equal(f.completed[1].result.timedOut,true);
  await f.call({command:'sleep 10',yieldTimeMs:0});await f.stop();
  assert.equal(f.completed.length,2,'shutdown must not enqueue a spurious completed command');
});

test('recovery marks yielded sessions unknown once, without duplicating tool results or misclassifying durable completions',async t=>{
  const home=await mkdtemp(join(tmpdir(),'bot-async-recovery-')),store=new Store(home);
  t.after(async()=>{store.close();await rm(home,{recursive:true,force:true});});
  const launch=(id,sessionId)=>{
    store.append({role:'assistant',content:'',toolCalls:[{id,name:'shell',arguments:{command:'long work'}}]});
    store.startAction(id,'input','shell',{command:'long work'});
    store.finishAction(id,{outcome:'running',sessionId,stdout:'',stderr:''});
  };
  launch('interrupted','interrupted-session');
  assert.equal(store.recover()[0].outcome,'unknown');assert.equal(store.history().filter(m=>m.role==='tool').length,1);
  assert.equal(store.history().filter(m=>m.metadata?.shellSessionUnknown).length,1);assert.deepEqual(store.recover(),[]);
  launch('polled','polled-session');
  store.append({role:'assistant',content:'',toolCalls:[{id:'poll',name:'shell',arguments:{sessionId:'polled-session'}}]});
  store.startAction('poll','input','shell',{sessionId:'polled-session'});store.finishAction('poll',{outcome:'completed',exitCode:0,stdout:'finished'});
  launch('notified','notified-session');
  store.enqueue({id:'shell:notified-session',text:'Already finished, not yet admitted',metadata:{source:'shell',sessionId:'notified-session'}});
  assert.deepEqual(store.recover(),[]);
});

test('the offline demo waits for notifications and polls read/send receipts once instead of duplicating replies',async()=>{
  const model=createModel({});
  const messages=[{role:'context',content:'Incoming chat',metadata:{input:true}}];
  const call=(id,args,result)=>{messages.push({role:'assistant',content:'',toolCalls:[{id,name:'shell',arguments:args}]},{role:'tool',toolName:'shell',toolCallId:id,content:JSON.stringify(result)});};
  call('read',{command:"bot-app chat read '{}'"},{outcome:'running',sessionId:'read-session',stdout:''});
  assert.deepEqual((await model.complete({messages})).toolCalls,[]);
  messages.push({role:'user',content:'Shell completion',metadata:{source:'shell',sessionId:'read-session'}});
  const poll=(await model.complete({messages})).toolCalls[0];assert.equal(poll.arguments.sessionId,'read-session');
  call(poll.id,poll.arguments,{outcome:'completed',stdout:JSON.stringify({messages:[{sender:'human',text:'hello'}]})});
  const send=(await model.complete({messages})).toolCalls[0];assert.match(send.arguments.command,/chat send/);
  call(send.id,send.arguments,{outcome:'running',sessionId:'send-session',stdout:''});
  assert.deepEqual((await model.complete({messages})).toolCalls,[]);
  messages.push({role:'user',content:'Shell completion',metadata:{source:'shell',sessionId:'send-session'}});
  const finish=(await model.complete({messages})).toolCalls[0];assert.equal(finish.arguments.sessionId,'send-session');
  call(finish.id,finish.arguments,{outcome:'completed',stdout:'sent'});
  assert.deepEqual((await model.complete({messages})).toolCalls,[]);
});

test('a new message is handled during a long command; completion wakes the isolated conversation without polling',async t=>{
  const home=await mkdtemp(join(tmpdir(),'bot-async-conversation-'));let server;
  t.after(async()=>{await server?.stop();await rm(home,{recursive:true,force:true});});
  const config=createConfig({disable:'chat,memory,context,scheduler,skills,web-search'});
  await initialize(home,config);
  await writeFile(join(home,'drafts/apps/demo-model/behavior.mjs'),`import {appendFile} from 'node:fs/promises';import {join} from 'node:path';
export const createModel=()=>({async complete({messages}){
  await appendFile(join(process.env.BOT_WORKSPACE,'calls'),'call\\n');
  const input=messages.findLast(m=>m.role==='user');const recent=messages.slice(messages.indexOf(input)+1);
  if(recent.some(m=>m.role==='tool'))return {text:'',toolCalls:[]};
  let command;
  if(input.metadata?.source==='shell'){
    if(!input.content.includes('result-payload'))throw new Error('Missing completion output');
    command='printf observed > completion-seen';
  }else if(input.content==='follow-up'){
    const running=messages.findLast(m=>m.role==='tool'&&m.content.includes('sessionId'));
    if(!running||JSON.parse(running.content).outcome!=='running')throw new Error('No running receipt before follow-up');
    command='printf responsive > reply-seen';
  }else command='printf started > started; while [ ! -e finish-command ]; do sleep 0.02; done; printf result-payload';
  return {text:'',toolCalls:[{id:crypto.randomUUID(),name:'shell',arguments:{command,yieldTimeMs:30000}}]};
}});`);
  await stageDrafts(home);server=await startSupervisor({home,port:0,unsafe:true});
  const api=(path,body)=>fetch(server.url+path,{method:body?'POST':'GET',headers:{authorization:'Bearer '+server.token,'content-type':'application/json'},body:body?JSON.stringify(body):undefined}).then(r=>r.json());
  const exists=name=>access(join(home,'workspace',name)).then(()=>true,()=>false);
  await until(async()=>(await api('/api/status')).state==='idle');
  await api('/api/messages',{id:'launch',text:'launch'});await until(()=>exists('started'));
  const arrival=Date.now();await api('/api/messages',{id:'follow-up',text:'follow-up'});
  await until(()=>exists('reply-seen'));assert.ok(Date.now()-arrival<3000,'follow-up must not wait for the 30-second tool yield');
  assert.equal(await exists('finish-command'),false);await until(async()=>(await api('/api/status')).pending===0);
  await writeFile(join(home,'workspace/finish-command'),'finish');
  await until(()=>exists('completion-seen'));await until(async()=>(await api('/api/status')).pending===0);
  const history=(await api('/api/messages')).messages;
  assert.equal(history.filter(m=>m.metadata?.source==='shell').length,1);
  assert.ok(history.filter(m=>m.role==='assistant').every(m=>m.toolCalls.every(call=>!call.arguments.sessionId)),'completion requires no polling call');
  const before=await readFile(join(home,'workspace/calls'),'utf8');await delay(200);
  assert.equal(await readFile(join(home,'workspace/calls'),'utf8'),before,'idle completion must not spin the model loop');
  await server.stop();server=await startSupervisor({home,port:0,unsafe:true});
  await until(async()=>(await api('/api/status')).state==='idle');
  assert.equal((await api('/api/messages')).messages.filter(m=>m.metadata?.source==='shell').length,1,'completion remains durable across restart');
  assert.equal(await readFile(join(home,'workspace/calls'),'utf8'),before,'restart must not replay completed work');
});
