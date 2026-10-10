// Explicit controller used by kernel/recovery tests. It intentionally supports
// final replies; production agent behavior is tested separately.
import { createInterface } from 'node:readline';
import { readFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawn } from 'node:child_process';
const folder=dirname(fileURLToPath(import.meta.url));
function command(argv,input={}) {
  return new Promise((resolve,reject)=>{
    const child=spawn(argv[0],argv.slice(1),{stdio:['pipe','pipe','pipe']});let out='',err='';
    child.stdout.on('data',value=>out+=value);child.stderr.on('data',value=>err+=value);
    child.once('error',reject);child.stdin.on('error',()=>{});
    child.once('close',code=>{try {if(code){let detail;try{detail=JSON.parse(err.trim().split('\n').at(-1));}catch{}throw Object.assign(new Error(detail?.error||err),detail);}resolve(out.trim()?JSON.parse(out):null);}catch(error){reject(error);}});
    child.stdin.end(JSON.stringify(input));
  });
}
let config,options,tools,sequence=0;
const pending=new Map(),send=value=>process.stdout.write(JSON.stringify(value)+'\n');
const rpc=(method,args={})=>new Promise((resolve,reject)=>{const id=++sequence;pending.set(id,{resolve,reject});send({type:'request',id,method,args});});
const invoke=(app,operation,input)=>command(['bot-app',app,operation,'--stdin'],input);
async function ownedModel(input,preflight=false) {
  const bridge=(method,args={})=>command([process.execPath,process.env.BOT_CORE_CLI,config.controller,method,'--stdin'],args);
  let model,context;const hooks=new Map();
  const factory=(await import('./model.mjs')).default;
  await factory({config:config.apps[options.modelApp],paths:{module:folder,workspace:process.env.BOT_WORKSPACE,drafts:process.env.BOT_DRAFTS},
    state:{get:(key,fallback)=>bridge('state.get',{key,fallback}),set:(key,value)=>bridge('state.set',{key,value}),delete:key=>bridge('state.delete',{key})},
    history:()=>bridge('history'),enqueue:event=>bridge('enqueue',{event}),
    registerModel:value=>model=value,registerContext:value=>context=value,on:(name,fn)=>hooks.set(name,fn)});
  if(preflight)return;
  await hooks.get('beforeModel')?.(input);if(context)await context({...input,model});
  const result=await model.complete(input);await hooks.get('afterTurn')?.();return result;
}
if(process.argv[2]==='--preflight') {
  config=JSON.parse(await readFile(process.env.BOT_PREFLIGHT_FILE,'utf8')).config;options=config.apps[config.controller];
  if(options.fixtureOwnedModel)await ownedModel({},true);
} else {
  async function run(event) {
    let committed=false;
    try {
      while(true) {
        send({type:'progress',phase:'model'});
        let messages=await rpc('history');
        for(const app of options.projections||[])messages=await invoke(app,'project',{messages});
        const input={messages,tools,settings:config.model||{}};
        const response=options.fixtureOwnedModel?await ownedModel(input):await invoke(options.modelApp,'complete',input);
        const calls=response.toolCalls||[],assistant={role:'assistant',content:response.text,toolCalls:calls,metadata:{...response.metadata,modelOutput:true},usage:response.usage};
        if(!calls.length){await rpc('turn.commit',{eventId:event.id,message:assistant});committed=true;
          if(options.crashAfterCommit){const {existsSync,writeFileSync}=await import('node:fs');const marker=join(process.env.BOT_WORKSPACE,'committed-crash');if(!existsSync(marker)){writeFileSync(marker,'yes');process.exit(63);}}
          send({type:'turnDone',eventId:event.id});return;}
        await rpc('append',{message:assistant});
        for(const call of calls){send({type:'progress',phase:'tool'});await rpc('action.run',{eventId:event.id,call});}
      }
    } catch(error){if(!committed&&(error.retryable||error.blocked))send({type:'turnError',eventId:event.id,error:error.message,retryable:!!error.retryable,blocked:!!error.blocked});else send({type:'fatal',error:error.stack});}
  }
  createInterface({input:process.stdin}).on('line',line=>{
    const message=JSON.parse(line);
    if(message.type==='result'){const request=pending.get(message.id);pending.delete(message.id);if(message.error)request.reject(Object.assign(new Error(message.error.message),message.error));else request.resolve(message.result);}
    else if(message.type==='init'){config=message.config;options=config.apps[config.controller];tools=message.tools;send({type:'ready',protocol:1});}
    else if(message.type==='turn')void run(message.event);
    else if(message.type==='stop')process.exit(0);
  });
}
