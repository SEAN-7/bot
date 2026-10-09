import { readFile, readdir } from 'node:fs/promises';
import { join } from 'node:path';
import { spawn } from 'node:child_process';
import Ajv from 'ajv';
import execution from './execution.mjs';
import { startAppServer } from './app-server.mjs';
import { appManifest, commandAt, APP_ID } from './app-package.mjs';

const [home,revision,mode='run']=process.argv.slice(2), revisionDir=join(home,'revisions',revision);
const config=JSON.parse(await readFile(join(revisionDir,'config.json'),'utf8'));
if(config.apiVersion!==3||config.modules)throw new Error('Only process applications are supported');
const pending=new Map();let serial=0,closing=false,activeEvent=null,committed=false,actionBusy=false,controllerReady=false,controller;
const send=message=>{if(process.connected)process.send(message);};
function rpc(method,args={}) {
  return new Promise((resolve,reject)=>{
    const id=++serial,timer=setTimeout(()=>{pending.delete(id);reject(new Error(`Supervisor request timed out: ${method}`));},15_000);
    pending.set(id,{resolve,reject,timer});send({type:'rpc',id,method,args});
  });
}
const fatal=error=>{send({type:'fatal',error:error.stack||String(error)});setTimeout(()=>process.exit(1),20);};
process.on('uncaughtException',fatal);process.on('unhandledRejection',fatal);
let phase='loading';const heartbeat=setInterval(()=>send({type:'heartbeat',phase}),500);heartbeat.unref();
const bridge=mode==='preflight'?null:await startAppServer({rpc});
const env={...process.env,...bridge?.env,BOT_NODE:process.execPath,BOT_APP_LAUNCHER:new URL('./app-cli.mjs',import.meta.url).pathname,BOT_CORE_CLI:new URL('./botctl.mjs',import.meta.url).pathname,BOT_APPS:join(home,'drafts','apps'),BOT_CONTROLLER_APPS:join(revisionDir,'apps'),BOT_WORKSPACE:join(home,'workspace'),BOT_DRAFTS:join(home,'drafts'),PATH:new URL('./bin',import.meta.url).pathname+':'+process.env.PATH};
let stopShell;
const shell=execution({config:config.shell||{},paths:{workspace:join(home,'workspace')},env,trackProcess:pid=>rpc('process.track',{pid}),releaseProcess:pid=>rpc('process.release',{pid}),on:(event,handler)=>{if(event==='stop')stopShell=handler;}});
const ajv=new Ajv({allErrors:true,strict:false}),validate=ajv.compile(shell.inputSchema);
const tools=[{name:shell.name,description:shell.description,inputSchema:shell.inputSchema}];
function progress(next){phase=next;send({type:'progress',phase});}
function write(message){if(controller?.stdin.writable)controller.stdin.write(JSON.stringify(message)+'\n');}
async function request(message) {
  const {id,method,args={}}=message;
  try {
    if(!Number.isSafeInteger(id)||JSON.stringify(args).length>20_000_000)throw new Error('Invalid controller request');
    let result;
    if(method==='history')result=await rpc(method);
    else if(method==='contribute') {
      if(!config.apps[args.module]?.enabled||typeof args.key!=='string'||typeof args.content!=='string'||args.content.length>100000)throw new Error('Invalid app contribution');
      result=await rpc(method,args);
    } else if(method==='append'||method==='turn.commit'||method==='input.acknowledge') {
      if(!activeEvent||committed||actionBusy)throw new Error('No writable active event');
      result=await rpc(method,args);if(method!=='append')committed=true;
    } else if(method==='action.run') {
      if(!activeEvent||args.eventId!==activeEvent||committed||actionBusy)throw new Error('Action belongs to inactive or busy event');
      const call=args.call;
      if(typeof call?.id!=='string'||!call.id||typeof call.name!=='string')throw new Error('Invalid action');
      actionBusy=true;
      try {
        await rpc('tool.start',{id:call.id,eventId:activeEvent,name:call.name,args:call.arguments});
        let output,isError=false;
        if(call.name!=='shell'){output=`Unknown tool: ${call.name}`;isError=true;}
        else if(!validate(call.arguments)){output=`Invalid arguments: ${ajv.errorsText(validate.errors)}`;isError=true;}
        else try{output=await shell.execute(call.arguments,{signal:new AbortController().signal,callId:call.id});}catch(error){output=error.message;isError=true;}
        result=await rpc('tool.finish',{id:call.id,result:output??null,isError});
      } finally {actionBusy=false;}
    } else throw new Error('Unsupported controller operation: '+method);
    write({type:'result',id,result});
  } catch(error){write({type:'result',id,error:{message:error.message,retryable:!!error.retryable,blocked:!!error.blocked}});}
}
process.on('message',message=>{
  if(message.type==='rpc.result') {
    const req=pending.get(message.id);if(!req)return;pending.delete(message.id);clearTimeout(req.timer);
    if(message.error)req.reject(new Error(message.error));else req.resolve(message.result);
  } else if(message.type==='stop') {
    closing=true;write({type:'stop'});stopShell?.().catch(()=>{}).then(()=>bridge?.close()).finally(()=>process.exit(0));
  } else if(message.type==='turn') {
    if(!controllerReady||activeEvent||phase==='tick')return fatal(new Error('Concurrent or premature turn'));
    activeEvent=message.event.id;committed=false;write(message);
  } else if(message.type==='tick') {
    if(!controllerReady||activeEvent||phase==='tick')return fatal(new Error('Concurrent or premature tick'));
    progress('tick');write(message);
  }
});
try {
  // Ordinary apps are editable programs, resolved only when invoked. A broken
  // optional app must not prevent the conversation controller from starting.
  const folder=join(revisionDir,'apps',config.controller);
  const manifest=await appManifest(folder);
  if(manifest.id!==config.controller||!config.apps[config.controller]?.enabled)throw new Error('Enable the configured controller app');
  if(mode==='preflight'&&manifest.preflight) {
    const launch=commandAt(folder,manifest.command,env);
    await new Promise((resolve,reject)=>{
      const child=spawn(launch.command,[...launch.args,...manifest.preflight],{cwd:folder,env,stdio:['ignore','pipe','pipe']});let output='';
      for(const stream of [child.stdout,child.stderr])stream.on('data',chunk=>{output+=chunk;if(output.length>100000){child.kill('SIGKILL');reject(new Error('Preflight output exceeded limit'));}});
      child.once('error',reject);child.once('exit',code=>code===0?resolve():reject(new Error(`Controller ${config.controller} preflight failed (${code}): ${output.slice(-8000)}`)));
    });
  }
  if(mode==='preflight'){send({type:'ready',apiVersion:1,tools,preflight:true});await bridge?.close();setTimeout(()=>process.exit(0),20);}
  else {
    const launch=commandAt(folder,manifest.command,env);
    controller=spawn(launch.command,[...launch.args,'--serve'],{cwd:folder,env,stdio:['pipe','pipe','pipe']});
    controller.stdin.on('error',error=>{if(!closing)fatal(error);});controller.stderr.pipe(process.stderr);
    controller.once('error',fatal);controller.once('exit',(code,signal)=>{if(!closing)fatal(new Error(`Controller exited (${code??signal})`));});
    let buffer='';controller.stdout.setEncoding('utf8').on('data',chunk=>{
      buffer+=chunk;if(buffer.length>20_000_000)return fatal(new Error('Controller output exceeded limit'));
      let newline;
      while((newline=buffer.indexOf('\n'))>=0){const line=buffer.slice(0,newline);buffer=buffer.slice(newline+1);
        try {
          const message=JSON.parse(line);
          if(message.type==='request')request(message).catch(fatal);
          else if(message.type==='ready'){
            if(controllerReady||message.protocol!==1)throw new Error('Invalid controller protocol');controllerReady=true;progress('idle');send({type:'ready',apiVersion:1,tools});
          } else if(message.type==='progress')progress(String(message.phase));
          else if(message.type==='turnDone'||message.type==='turnError'){
            if(!activeEvent||message.eventId!==activeEvent||actionBusy||(message.type==='turnDone'&&!committed)||(message.type==='turnError'&&committed))throw new Error('Invalid event completion');
            activeEvent=null;progress('idle');send(message);
          } else if(message.type==='tickDone'){
            if(activeEvent||phase!=='tick')throw new Error('Invalid idle completion');progress('idle');send(message);
          } else if(message.type==='fatal')throw new Error(message.error);
          else throw new Error('Unsupported controller message');
        }catch(error){fatal(error);}
      }
    });
    write({type:'init',protocol:1,config,tools});
  }
} catch(error){fatal(error);}
