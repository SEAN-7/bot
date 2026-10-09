import { createInterface } from 'node:readline';
import { randomUUID } from 'node:crypto';
import { invoke } from '../_lib/invoke.mjs';
import { appClient } from '../_lib/client.mjs';
import { startServices } from './services.mjs';

const mode = process.argv[2];
if (mode === '--preflight' || mode === '--help') {
  if(mode==='--preflight'){const options=await appClient('agent')('config');if(!options.modelApp)throw new Error('Controller needs a model app');for(const app of [options.modelApp,options.contextApp,...options.contributors||[],...options.projections||[],...options.tickApps||[],...options.services||[]].filter(Boolean))await appClient(app)('config');}
  console.log(JSON.stringify({protocol:1,usage:'agent --serve',description:'Replaceable serialized conversation controller'}));
} else if (mode !== '--serve') throw new Error('Use --serve or --help');
else {
  let config, tools, options, active=false, closing=false, sequence=0, stopServices;
  const pending = new Map();
  const send = message => process.stdout.write(JSON.stringify(message)+'\n');
  const rpc = (method,args={}) => new Promise((resolve,reject)=>{
    const id=++sequence;pending.set(id,{resolve,reject});send({type:'request',id,method,args});
  });
  const progress = phase => send({type:'progress',phase});
  async function prepare() {
    const settings=config.model||{};
    let messages=await rpc('history');
    for(const app of options.contributors||[]) {
      let result;
      try { result=await invoke(app,'contribute',{}); }
      catch(error) { console.error(`Contributor ${app} failed: ${error.message}`);continue; }
      if(!Array.isArray(result))throw new Error('Invalid context contributions from '+app);
      for(const item of result)await rpc('contribute',{module:app,...item});
    }
    messages=await rpc('history');
    const latest=messages.findLast(m=>m.role==='user');
    for(const app of options.projections||[])messages=await invoke(app,'project',{messages});
    if(options.contextApp)messages=(await invoke(options.contextApp,'prepare',{messages,tools,settings,modelApp:options.modelApp})).messages;
    if(!Array.isArray(messages)||messages.some(m=>typeof m.content!=='string'))throw new Error('Invalid prepared context');
    if(latest&&!messages.some(m=>m.id===latest.id))throw new Error('Context removed the current input');
    return {messages,tools,settings};
  }
  async function run(event) {
    if(active||closing)throw new Error('Concurrent turns are prohibited');active=true;let committed=false;
    try {
      for(let step=0;step<(options.maxSteps||100);step++) {
        progress('context');const request=await prepare();progress('model');
        const response=await invoke(options.modelApp,'complete',request);
        if(!response||typeof response.text!=='string'||!Array.isArray(response.toolCalls||[]))throw new Error('Invalid model result');
        const calls=response.toolCalls||[], ids=new Set();
        for(const call of calls){call.id||=randomUUID();if(ids.has(call.id)||typeof call.name!=='string'||!call.arguments||typeof call.arguments!=='object')throw new Error('Invalid model tool request');ids.add(call.id);}
        const assistant={role:'assistant',content:response.text,toolCalls:calls,metadata:{...response.metadata,modelOutput:true},usage:response.usage};
        if(!calls.length){await rpc('turn.commit',{eventId:event.id,message:assistant});committed=true;}
        else await rpc('append',{message:assistant});
        for(const call of calls){progress('tool');await rpc('action.run',{eventId:event.id,call});}
        if(!calls.length){send({type:'turnDone',eventId:event.id});return;}
      }
      await rpc('turn.commit',{eventId:event.id,message:{role:'context',content:'Turn reached the configured tool-step limit. Work is paused until the next input.',metadata:{recovery:true}}});committed=true;
      send({type:'turnDone',eventId:event.id});
    } catch(error) {
      if(!committed&&(error.retryable||error.blocked))send({type:'turnError',eventId:event.id,error:error.message,retryable:!!error.retryable,blocked:!!error.blocked});
      else throw error;
    } finally {active=false;progress('idle');}
  }
  const fatal = error => {send({type:'fatal',error:error.stack||String(error)});process.exitCode=1;process.stdin.destroy();};
  createInterface({input:process.stdin,crlfDelay:Infinity}).on('line', line=>{
    try {
      if(line.length>20_000_000)throw new Error('Controller input exceeded limit');
      const message=JSON.parse(line);
      if(message.type==='result') {
        const request=pending.get(message.id);if(!request)return;pending.delete(message.id);
        if(message.error)request.reject(Object.assign(new Error(message.error.message),message.error));else request.resolve(message.result);
      } else if(message.type==='init') {
        if(config||message.protocol!==1)throw new Error('Invalid controller initialization');
        config=message.config;tools=message.tools;options=config.apps[config.controller];
        for(const app of [options.modelApp,options.contextApp,...options.contributors||[],...options.projections||[],...options.tickApps||[],...options.services||[]].filter(Boolean))if(!config.apps[app]?.enabled)throw new Error('Disabled behavior app: '+app);
        if(!options.modelApp)throw new Error('Controller needs a model app');
        stopServices=startServices(options.services||[]);
        send({type:'ready',protocol:1});
      } else if(message.type==='turn')run(message.event).catch(fatal);
      else if(message.type==='tick') {
        if(active||closing)throw new Error('Concurrent idle work');active=true;progress('tick');
        Promise.allSettled((options.tickApps||[]).map(app=>invoke(app,'tick',{}))).then(results=>{for(const result of results)if(result.status==='rejected')console.error(`Idle app failed: ${result.reason.message}`);send({type:'tickDone'});}).catch(fatal).finally(()=>{active=false;progress('idle');});
      } else if(message.type==='stop'){closing=true;stopServices?.();process.exit(0);}
      else throw new Error('Unknown controller input');
    } catch(error){fatal(error);}
  });
}
