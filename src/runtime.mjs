import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { randomUUID } from 'node:crypto';
import { PluginHost } from './host.mjs';

const [home,revision,mode='run']=process.argv.slice(2);
const revisionDir=join(home,'revisions',revision);
const config=JSON.parse(await readFile(join(revisionDir,'config.json'),'utf8'));
const pending=new Map(); let serial=0, active=false, closing=false;
function send(message) { if(process.connected) process.send(message); }
function rpc(method,args={}) {
  return new Promise((resolve,reject)=>{
    const id=++serial;
    const timer=setTimeout(()=>{pending.delete(id);reject(new Error(`Supervisor request timed out: ${method}`));},15_000);
    pending.set(id,{resolve,reject,timer}); send({type:'rpc',id,method,args});
  });
}
function fatal(error) { send({type:'fatal',error:error?.stack||String(error)}); setTimeout(()=>process.exit(1),20); }
process.on('uncaughtException',fatal); process.on('unhandledRejection',fatal);
const host=new PluginHost({config,revisionDir,workspace:join(home,'workspace'),drafts:join(home,'drafts'),rpc});
let phase='loading';
function progress(next) { phase=next; send({type:'progress',phase}); }
const heartbeat=setInterval(()=>send({type:'heartbeat',phase}),500); heartbeat.unref();

async function run(event) {
  if(active||closing) throw new Error('Concurrent turns are prohibited');
  active=true;
  let committed=false;
  const controller=new AbortController();
  try {
    for(let step=0;step<(config.runtime?.maxSteps||100);step++) {
      progress('context');
      const request=await host.prepare(controller.signal);
      progress('model');
      const response=await host.model.complete(request);
      if(!response||typeof response.text!=='string'||!Array.isArray(response.toolCalls||[])) throw new Error('Model returned an invalid result');
      const calls=response.toolCalls||[];
      const ids=new Set();
      for(const call of calls) {
        if(!call.id) call.id=randomUUID();
        if(ids.has(call.id)||typeof call.name!=='string'||!call.arguments||typeof call.arguments!=='object') throw new Error('Invalid model tool request');
        ids.add(call.id);
      }
      const assistant={role:'assistant',content:response.text,toolCalls:calls,metadata:response.metadata,usage:response.usage};
      if(!calls.length){await rpc('turn.commit',{eventId:event.id,message:assistant});committed=true;}
      else await rpc('append',{message:assistant});
      for(const call of calls) {
        const tool=host.tools.get(call.name);
        await rpc('tool.start',{id:call.id,eventId:event.id,name:call.name,args:call.arguments});
        progress('tool');
        let result,isError=false;
        if(!tool) {result=`Unknown tool: ${call.name}`;isError=true;}
        else if(!tool.validate(call.arguments)) {result=`Invalid arguments: ${host.ajv.errorsText(tool.validate.errors)}`;isError=true;}
        else {
          try { result=await tool.execute(call.arguments,{signal:controller.signal,callId:call.id}); }
          catch(error) { result=String(error.message||error);isError=true; }
        }
        await rpc('tool.finish',{id:call.id,result:result??null,isError});
      }
      if(!calls.length) {
        progress('afterTurn'); await host.emit('afterTurn',{signal:controller.signal});
        send({type:'turnDone',eventId:event.id}); return;
      }
    }
    await rpc('turn.commit',{eventId:event.id,message:{role:'context',content:'Turn reached the configured tool-step limit. Work is paused until the next input.',metadata:{recovery:true}}});
    committed=true;
    send({type:'turnDone',eventId:event.id});
  } catch(error) {
    if(!committed&&(error?.retryable||error?.blocked))send({type:'turnError',eventId:event.id,error:String(error.message||error),retryable:!!error.retryable,blocked:!!error.blocked});
    else throw error;
  } finally { active=false;progress('idle'); }
}
process.on('message',message=>{
  if(message.type==='rpc.result') {
    const req=pending.get(message.id);if(!req)return;
    pending.delete(message.id);clearTimeout(req.timer);
    if(message.error)req.reject(new Error(message.error));else req.resolve(message.result);
  } else if(message.type==='turn') run(message.event).catch(fatal);
  else if(message.type==='tick'&&!active&&!closing) {
    active=true;progress('tick');host.emit('tick',{signal:new AbortController().signal}).then(()=>send({type:'tickDone'})).catch(fatal).finally(()=>{active=false;progress('idle');});
  } else if(message.type==='stop') {
    closing=true;host.emit('stop').catch(()=>{}).finally(()=>process.exit(0));
  }
});
try {
  await host.load();
  if(mode==='preflight') {send({type:'ready',apiVersion:1,tools:host.inventory(),preflight:true});setTimeout(()=>process.exit(0),20);}
  else {await host.emit('start',{signal:new AbortController().signal});progress('idle');send({type:'ready',apiVersion:1,tools:host.inventory()});}
} catch(error) {fatal(error);}
