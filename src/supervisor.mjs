import { createServer } from 'node:http';
import { mkdir, rm, open, realpath } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { randomBytes, timingSafeEqual, randomUUID } from 'node:crypto';
import { Store } from './store.mjs';
import { readCurrent, readJSON, atomicJSON, metadataPath, stageDrafts, beginTrial, promote, rollback, stageCore } from './releases.mjs';
import { launchRuntime, stopRuntime, runtimeProcessGroup } from './sandbox.mjs';

const defaults={port:4317,startupTimeoutMs:10_000,heartbeatTimeoutMs:10_000,operationTimeoutMs:120_000,trialMs:3000,maxRestarts:2,retryMs:3000};
function limits(config) {
  const value={...defaults,...config.runtime};
  for(const key of Object.keys(defaults)) if(!Number.isFinite(value[key])||value[key]<0) throw new Error(`Invalid runtime setting: ${key}`);
  for(const key of ['startupTimeoutMs','heartbeatTimeoutMs','operationTimeoutMs']) if(value[key]<100||value[key]>3_600_000) throw new Error(`Unsafe runtime limit: ${key}`);
  if(value.maxRestarts>10||value.retryMs<50||value.port>65535) throw new Error('Invalid recovery limits');
  return value;
}
function equalToken(a,b) {return typeof a==='string'&&a.length===b.length&&timingSafeEqual(Buffer.from(a),Buffer.from(b));}
async function bodyOf(request) {
  request.setEncoding('utf8');
  let text='';for await(const chunk of request){text+=chunk;if(Buffer.byteLength(text)>150_000)throw new Error('Request is too large');}
  return JSON.parse(text||'{}');
}
async function claim(home) {
  const path=join(home,'control','supervisor.lock');const owner=randomUUID();
  await mkdir(join(home,'control'),{recursive:true,mode:0o700});
  for(let attempt=0;attempt<2;attempt++) {
    try {const file=await open(path,'wx',0o600);await file.writeFile(JSON.stringify({pid:process.pid,owner}));await file.close();return async()=>{try{const current=await readJSON(path);if(current.owner===owner)await rm(path);}catch{}};}
    catch(error) {
      if(error.code!=='EEXIST')throw error;
      const lock=await readJSON(path);let alive=true;try{process.kill(lock.pid,0);}catch(e){if(e.code==='ESRCH')alive=false;}
      if(alive)throw new Error('A supervisor is already running for this bot home');
      await rm(path);
    }
  }
  throw new Error('Could not claim supervisor lock');
}

export async function startSupervisor({home,port,unsafe=false,fixedCoreRoot}) {
  home=await realpath(resolve(home));
  const installedCore=fixedCoreRoot ? await realpath(resolve(fixedCoreRoot)) : null;
  let current=await readCurrent(home),settings=limits(current.config);
  const unlock=await claim(home);
  const store=new Store(home);
  const token=randomBytes(32).toString('hex'),uiToken=randomBytes(32).toString('hex');
  let worker=null,ready=false,busy=false,stopped=false,transitioning=false,restarts=0;
  let state='starting',error=null,lastHeartbeat=0,phaseStarted=0,workerStarted=0;
  let groups=new Map(),activeEvent=null,completedTrialTurn=false,tickAt=0;
  let updateRequested=false,protectedMode=!unsafe,queue=Promise.resolve();
  const retryCounts=new Map();
  const serialize=fn=>{const result=queue.then(fn);queue=result.catch(()=>{});return result;};
  const status=()=>({state,revision:current.revision,coreRoot:installedCore||current.coreRoot,pending:store.pending(),error,model:current.config.model,protected:protectedMode,canRecover:!!(current.previous||current.trial),trial:current.trial||null});
  const diagnose=(kind,payload)=>store.record(kind,payload);
  function reply(child,id,result,failure) {if(child.connected)child.send({type:'rpc.result',id,result,error:failure});}

  async function handleRpc(child,message,{preflight=false,scratch,allowedModules=current.config.modules}={}) {
    const {id,method,args={}}=message;
    try {
      if(!preflight&&child!==worker)throw new Error('Stale runtime request');
      if(JSON.stringify(args).length>2_000_000)throw new Error('Runtime request too large');
      const module=args.module;
      if(module&&!allowedModules[module]?.enabled)throw new Error('Unknown or disabled module');
      let result;
      if(method.startsWith('state.')) {
        if(typeof module!=='string'||typeof args.key!=='string'||args.key.length>256)throw new Error('Invalid state key');
        if(preflight) {
          const key=`${module}:${args.key}`;
          if(method==='state.get')result=scratch.has(key)?scratch.get(key):args.fallback??null;
          else if(method==='state.set')scratch.set(key,args.value);
          else if(method==='state.delete')scratch.delete(key);
          else throw new Error('Unknown state operation');
        } else if(method==='state.get')result=store.getState(module,args.key,args.fallback);
        else if(method==='state.set')store.setState(module,args.key,args.value);
        else if(method==='state.delete')store.deleteState(module,args.key);
        else throw new Error('Unknown state operation');
      } else if(method==='history')result=preflight?[]:store.history();
      else if(method==='log') {if(!preflight)diagnose('module.log',{module,message:String(args.message).slice(0,8000)});}
      else if(preflight)throw new Error(`External action prohibited during preflight: ${method}`);
      else if(method==='enqueue')result=store.enqueue({...args.event,id:args.event.id?`${module}:${args.event.id}`:undefined,metadata:{...args.event.metadata,source:module}});
      else if(method==='contribute')result=store.contribute(args.key,args.content,module);
      else if(method==='append') {
        if(args.message?.role!=='assistant'&&args.message?.role!=='context')throw new Error('Runtime cannot forge user/tool results');
        result=store.append(args.message);
      }
      else if(method==='turn.commit') {
        if(args.eventId!==activeEvent||!['assistant','context'].includes(args.message?.role))throw new Error('Invalid final response');
        result=store.commitReply(args.eventId,args.message);
      }
      else if(method==='tool.start') {if(args.eventId!==activeEvent)throw new Error('Tool belongs to inactive event');store.startAction(args.id,args.eventId,args.name,args.args);}
      else if(method==='tool.finish')result=store.finishAction(args.id,args.result,args.isError);
      else if(method==='update.request') {updateRequested=true;result={queued:true,message:'Drafts will be validated after this turn finishes.'};}
      else if(method==='process.track') {
        const hostPid=await runtimeProcessGroup(child,args.pid);
        groups.set(args.pid,hostPid);result=true;
      } else if(method==='process.release')groups.delete(args.pid);
      else throw new Error(`Unknown runtime operation: ${method}`);
      reply(child,id,result);
    } catch(e) {reply(child,id,undefined,e.message);}
  }

  async function killWorker() {
    const previous=worker;worker=null;ready=false;busy=false;activeEvent=null;
    if(previous)await stopRuntime(previous,new Set(groups.values()));
    groups=new Map();
  }
  async function preflight(revision) {
    const config=await readJSON(join(home,'revisions',revision,'config.json'));
    const scratch=new Map(store.snapshot().map(row=>[`${row.module}:${row.key}`,JSON.parse(row.value)]));
    const launched=await launchRuntime({home,revision,coreRoot:installedCore||config.coreRoot,unsafe,preflight:true});
    const child=launched.child;let stderr='';
    child.stderr.on('data',chunk=>{stderr=(stderr+chunk).slice(-8000);});child.stdout.resume();
    try {
      await new Promise((accept,reject)=>{
        const timer=setTimeout(()=>reject(new Error('Candidate preflight timed out')),settings.startupTimeoutMs);
        const finish=(fn,value)=>{clearTimeout(timer);fn(value);};
        child.on('error',e=>finish(reject,e));
        child.on('exit',code=>finish(reject,new Error(`Candidate preflight exited (${code}): ${stderr}`)));
        child.on('message',message=>{
          if(message.type==='rpc')handleRpc(child,message,{preflight:true,scratch,allowedModules:config.modules});
          else if(message.type==='ready')message.apiVersion===1?finish(accept):finish(reject,new Error('Incompatible runtime protocol'));
          else if(message.type==='fatal')finish(reject,new Error(message.error));
        });
      });
    } finally {await stopRuntime(child);}
  }
  async function activatePending() {
    if(stopped||transitioning||busy)return;
    const meta=await readJSON(metadataPath(home));
    if(!meta.pending)return;
    transitioning=true;state='validating';
    try {
      await preflight(meta.pending);
      await killWorker();
      await beginTrial(home,store);
      current=await readCurrent(home);completedTrialTurn=false;restarts=0;error=null;
      await boot();
    } catch(e) {
      const latest=await readJSON(metadataPath(home));
      if(latest.trial) {
        await rollback(home,store);current=await readCurrent(home);
        store.recover(`Candidate activation failed; restored working revision. ${e.message}`);
        await boot();
      } else {
        await atomicJSON(metadataPath(home),{...latest,pending:null});
        error=`Update rejected: ${e.message}`;diagnose('update.rejected',{reason:error});
        store.append({role:'context',content:error,metadata:{recovery:true}});state=ready?'idle':'failed';
      }
    } finally {transitioning=false;}
  }
  async function fail(reason) {
    if(stopped||transitioning)return;
    transitioning=true;state='recovering';error=reason;
    try {
      await killWorker();
      current=await readCurrent(home);
      if(current.trial) {
        await rollback(home,store);current=await readCurrent(home);restarts=0;
        store.recover(`Candidate failed and was rolled back: ${reason}`);
        await boot();
      } else if(restarts<settings.maxRestarts) {
        restarts++;store.recover(`Runtime restarted (${restarts}/${settings.maxRestarts}): ${reason}`);await boot();
      } else if(current.previous) {
        await rollback(home,store);current=await readCurrent(home);restarts=0;
        store.recover(`Repeated failures; restored previous configuration: ${reason}`);await boot();
      } else {
        store.recover(`Automatic recovery stopped after repeated failures: ${reason}`);state='failed';
      }
    } catch(e) {state='failed';error=`Recovery failed: ${e.message}`;diagnose('recovery.failed',{error});}
    finally {transitioning=false;}
  }
  async function boot() {
    settings=limits(current.config);state='starting';ready=false;busy=false;
    store.resume();
    workerStarted=lastHeartbeat=phaseStarted=Date.now();
    const launched=await launchRuntime({home,revision:current.revision,coreRoot:installedCore||current.coreRoot,unsafe});
    const child=launched.child;worker=child;protectedMode=launched.protected;
    let stderr='';child.stderr.on('data',chunk=>{stderr=(stderr+chunk).slice(-8000);});
    child.stdout.on('data',chunk=>{if(!stopped&&child===worker)diagnose('runtime.stdout',{text:chunk.toString().slice(0,8000)});});
    child.on('error',e=>{if(child===worker)serialize(()=>fail(e.message));});
    child.on('exit',(code,signal)=>{if(child===worker&&!stopped)serialize(()=>fail(`Runtime exited (${code??signal}): ${stderr}`));});
    child.on('message',message=>{
      if(child!==worker||stopped)return;
      if(message.type==='heartbeat'){lastHeartbeat=Date.now();return;}
      if(message.type==='progress'){phaseStarted=Date.now();return;}
      if(message.type==='rpc'){serialize(()=>handleRpc(child,message));return;}
      serialize(async()=>{
        if(child!==worker||stopped)return;
        if(message.type==='ready') {
          if(message.apiVersion!==1){await fail('Incompatible runtime protocol');return;}
          ready=true;state='idle';diagnose('runtime.ready',{revision:current.revision});
        } else if(message.type==='tickDone'){busy=false;if(state==='working')state='idle';}
        else if(message.type==='turnDone') {
          if(message.eventId!==activeEvent)throw new Error('Mismatched completed event');
          activeEvent=null;busy=false;state='idle';error=null;restarts=0;
          retryCounts.delete(message.eventId);
          completedTrialTurn=true;
        } else if(message.type==='turnError') {
          const attempts=retryCounts.get(message.eventId)||0;
          retryCounts.set(message.eventId,attempts+1);
          store.retry(message.eventId,message.blocked?86_400_000:Math.min(60_000,settings.retryMs*2**Math.min(attempts,5)));
          busy=false;activeEvent=null;state=message.blocked?'blocked':'retrying';error=message.error;
          diagnose('model.unavailable',{error,retryable:message.retryable,blocked:message.blocked});
        } else if(message.type==='fatal') await fail(message.error);
      }).catch(e=>serialize(()=>fail(e.message)));
    });
  }
  async function pump() {
    if(stopped||transitioning)return;
    const now=Date.now();
    if(worker) {
      if(!ready&&now-workerStarted>settings.startupTimeoutMs){await fail('Runtime startup timed out');return;}
      if(now-lastHeartbeat>settings.heartbeatTimeoutMs){await fail('Runtime stopped answering health checks');return;}
      if(busy&&now-phaseStarted>settings.operationTimeoutMs){await fail('Runtime operation exceeded its deadline');return;}
    }
    if(busy)return;
    if(current.trial&&completedTrialTurn&&now-current.trial.startedAt>=settings.trialMs) {
      await promote(home,store);current=await readCurrent(home);
      // Keep the selected trusted core path in drafts after an operator core update.
      const draft=await readJSON(join(home,'drafts','config.json'));draft.coreRoot=current.coreRoot;await atomicJSON(join(home,'drafts','config.json'),draft);
    }
    if(updateRequested) {
      updateRequested=false;
      try{await stageDrafts(home);}catch(e){error=`Update rejected: ${e.message}`;store.append({role:'context',content:error,metadata:{recovery:true}});}
    }
    if((await readJSON(metadataPath(home))).pending){await activatePending();return;}
    if(!ready)return;
    const event=store.next();
    if(event) {busy=true;activeEvent=event.id;state='working';phaseStarted=now;worker.send({type:'turn',event});}
    else if(now-tickAt>=1000){busy=true;tickAt=phaseStarted=now;worker.send({type:'tick'});}
  }

  const server=createServer((request,response)=>{
    const respond=(code,data)=>{response.writeHead(code,{'Content-Type':'application/json','Cache-Control':'no-store','X-Content-Type-Options':'nosniff'});response.end(JSON.stringify(data));};
    (async()=>{
      const url=new URL(request.url,'http://127.0.0.1');
      if(request.headers.origin&&request.headers.origin!==`http://${request.headers.host}`){respond(403,{error:'Cross-origin requests are not allowed'});return;}
      if(url.pathname.startsWith('/api/')) {
        const credential=(request.headers.authorization||'').replace(/^Bearer /,'');
        const operator=equalToken(credential,token),browser=equalToken(credential,uiToken);
        if(!operator&&!browser){respond(401,{error:'Authentication required'});return;}
        if(request.method==='GET') {
          if(url.pathname==='/api/status')respond(200,status());
          else if(url.pathname==='/api/messages')respond(200,{messages:store.history()});
          else if(url.pathname==='/api/chat-snapshot')respond(200,store.chatSnapshot());
          else if(url.pathname==='/api/events')respond(200,{events:store.events(Number(url.searchParams.get('after'))||0)});
          else if(url.pathname==='/api/modules')respond(200,{modules:current.config.modules});
          else respond(404,{error:'Unknown endpoint'});
          return;
        }
        if(request.method!=='POST'){respond(405,{error:'Method not allowed'});return;}
        const body=await bodyOf(request);
        if(url.pathname==='/api/messages'){respond(202,store.enqueue({...body,metadata:{source:'chat'}}));return;}
        if(url.pathname==='/api/cancel') {
          const result=await serialize(async()=>{
            const cancelled=store.cancel(body.id);
            if(!cancelled.cancelled)return cancelled;
            retryCounts.delete(body.id);
            if(activeEvent===body.id) {
              transitioning=true;
              try {
                await killWorker();
                store.recover('User cancelled the active input. Interrupted tool outcomes may be unknown; do not resume the cancelled work.');
                await boot();
              } finally {transitioning=false;}
            }
            return cancelled;
          });respond(200,result);return;
        }
        if(url.pathname==='/api/recover') {
          const result=await serialize(async()=>{
            transitioning=true;
            try {
              current=await readCurrent(home);if(!current.trial&&!current.previous)throw new Error('No previous working version is available');
              await killWorker();await rollback(home,store);current=await readCurrent(home);restarts=0;completedTrialTurn=false;
              store.recover('Operator requested restoration of the previous working configuration');await boot();return {revision:current.revision};
            }finally{transitioning=false;}
          });respond(200,result);return;
        }
        if(!operator){respond(403,{error:'This operation requires the operator CLI'});return;}
        if(url.pathname==='/api/update') {
          const result=await serialize(async()=>{if(busy){updateRequested=true;return {queued:true};}return {revision:await stageDrafts(home),queued:true};});respond(202,result);
        } else if(url.pathname==='/api/core-update') {
          if(installedCore){respond(403,{error:'Installed core is managed by the administrator; use bot update'});return;}
          const result=await serialize(async()=>({revision:await stageCore(home,body.source),queued:true}));respond(202,result);
        } else respond(404,{error:'Unknown endpoint'});
        return;
      }
      respond(404,{error:'Use bot chat to open the terminal frontend'});
    })().catch(e=>respond(e.code==='ENOENT'?404:400,{error:e.message}));
  });
  let interval;
  const stop=async()=>{
    if(stopped)return;stopped=true;clearInterval(interval);
    await queue;await killWorker();store.set('cleanShutdown',true);store.close();
    server.closeAllConnections();await new Promise(resolve=>server.close(resolve));
    await rm(join(home,'control','endpoint.json'),{force:true});await unlock();
  };
  try {
    await new Promise((accept,reject)=>{server.once('error',reject);server.listen(port??settings.port,'127.0.0.1',accept);});
    const url=`http://127.0.0.1:${server.address().port}`;
    await atomicJSON(join(home,'control','endpoint.json'),{url,token,uiToken,pid:process.pid});
    // A supervisor interrupted during a trial must restore, never bless the unproven candidate.
    if(current.trial){await rollback(home,store);current=await readCurrent(home);store.recover('Supervisor restarted during an unproven update; restored checkpoint');}
    else store.recover(store.get('hasStarted')?'Supervisor restarted; resumed the durable conversation':null);
    store.set('hasStarted',true);store.set('cleanShutdown',false);
    await boot();
    interval=setInterval(()=>serialize(pump).catch(e=>serialize(()=>fail(e.message))),100);
    return {url,token,uiToken,stop};
  } catch(e) {await stop();throw e;}
}
