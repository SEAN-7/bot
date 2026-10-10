import { createInterface } from 'node:readline';
import { randomUUID } from 'node:crypto';
import { spawn } from 'node:child_process';
import { startServices } from './services.mjs';

function invoke(argv,input={}) {
  if(!Array.isArray(argv)||!argv.length||argv.some(arg=>typeof arg!=='string'||!arg))throw new Error('Hook must be a nonempty argv array');
  return new Promise((resolve,reject)=>{
    const child=spawn(argv[0],argv.slice(1),{stdio:['pipe','pipe','pipe']});let stdout='',stderr='';
    child.once('error',reject);child.stdin.on('error',error=>{if(error.code!=='EPIPE')reject(error);});
    child.stdout.on('data',chunk=>{stdout+=chunk;if(stdout.length>20_000_000)child.kill('SIGKILL');});
    child.stderr.on('data',chunk=>{stderr=(stderr+chunk).slice(-100000);});
    child.once('close',code=>{try{
      if(code!==0){let detail;try{detail=JSON.parse(stderr.trim().split('\n').at(-1));}catch{}throw Object.assign(new Error(detail?.error||stderr||`Hook exited (${code})`),{retryable:!!detail?.retryable,blocked:!!detail?.blocked});}
      resolve(JSON.parse(stdout));
    }catch(error){reject(error);}});
    child.stdin.end(JSON.stringify(input));
  });
}

const mode = process.argv[2];
if (mode === '--preflight' || mode === '--help') {
  if(mode==='--preflight') {
    const snapshot=JSON.parse(await (await import('node:fs/promises')).readFile(process.env.BOT_PREFLIGHT_FILE,'utf8'));
    const options=snapshot.config.apps[snapshot.config.controller];
    if(!Array.isArray(options.hooks?.complete)||!options.hooks.complete.length)throw new Error('Controller needs a completion hook');
  }
  console.log(JSON.stringify({protocol:2,usage:'agent --serve',description:'Replaceable serialized conversation controller'}));
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
    while(true) {
      const initial=await rpc('history');
      const prepared=options.hooks.prepare?await invoke(options.hooks.prepare,{messages:initial,tools}):{messages:initial,tools};
      const latestInputId=initial.findLast(m=>m.role==='user'||m.metadata?.resumedInput)?.id;
      const {append=[],latestInputId:ignored,...request}=prepared;
      for(const message of append)request.messages.push(await rpc('append',{message}));
      // Contributions, projections and compaction can take time. Incorporate
      // notifications received during that work before starting a model call.
      const history=await rpc('history');
      if(history.findLast(m=>m.role==='user'||m.metadata?.resumedInput)?.id===latestInputId)return request;
    }
  }
  async function run(event) {
    if(active||closing)throw new Error('Concurrent turns are prohibited');active=true;let committed=false;
    try {
      while(!closing) {
        progress('context');const request=await prepare();progress('model');
        const response=await invoke(options.hooks.complete,request);
        if(!response||typeof response.text!=='string'||!Array.isArray(response.toolCalls||[]))throw new Error('Invalid model result');
        const calls=response.toolCalls||[], ids=new Set();
        if(!calls.length&&response.usage)await rpc('append',{message:{role:'context',content:'',metadata:{modelObservation:true},usage:response.usage}});
        if(!calls.length&&response.text.trim()) {
          await rpc('append',{message:{role:'context',content:'The previous model response contained plain assistant text without tool calls and was rejected. Use communication tools for human replies; return no text when there is no action to take.',metadata:{modelValidation:true}}});
          throw Object.assign(new Error('Tool-only model returned a plain assistant response'),{retryable:true});
        }
        for(const call of calls){call.id||=randomUUID();if(ids.has(call.id)||typeof call.name!=='string'||!call.arguments||typeof call.arguments!=='object')throw new Error('Invalid model tool request');ids.add(call.id);}
        // Assistant protocol records hold tool requests and reasoning only.
        // Human replies are sent through communication apps; plain model text
        // is never a final reply or a declaration that the objective is done.
        const metadata={...response.metadata,modelOutput:true};
        if(metadata.nativeAssistant)metadata.nativeAssistant={...metadata.nativeAssistant,content:metadata.nativeAssistant.content.filter(block=>block.type!=='text')};
        const assistant={role:'assistant',content:'',toolCalls:calls,metadata,usage:response.usage};
        if(!calls.length){
          await rpc('input.acknowledge',{eventId:event.id});committed=true;
        }
        else await rpc('append',{message:assistant});
        for(const call of calls){progress('tool');await rpc('action.run',{eventId:event.id,call});}
        if(!calls.length){send({type:'turnDone',eventId:event.id});return;}
      }
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
        if(config||message.protocol!==2)throw new Error('Invalid controller initialization');
        config=message.config;tools=message.tools;options=config.apps[config.controller];
        if(!Array.isArray(options.hooks?.complete)||!options.hooks.complete.length)throw new Error('Controller needs a completion hook');
        stopServices=startServices(options.services||[]);
        send({type:'ready',protocol:2});
      } else if(message.type==='turn')run(message.event).catch(fatal);
      else if(message.type==='stop'){closing=true;stopServices?.();process.exit(0);}
      else throw new Error('Unknown controller input');
    } catch(error){fatal(error);}
  });
}
