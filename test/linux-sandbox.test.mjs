import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, rm, readFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { launchRuntime, runtimeProcessGroup, stopRuntime, sandboxAvailable } from '../src/sandbox.mjs';

test('Linux worker isolates guardian PIDs, preserves IPC, and stops tracked descendants', {skip:process.platform!=='linux'}, async t=>{
  if(await sandboxAvailable()!=='bwrap'){t.skip('bubblewrap is not installed');return;}
  const home=await mkdtemp(join(tmpdir(),'bot-sandbox-'));
  let child;
  try {
    for(const path of ['workspace','drafts','control','core/src'])await mkdir(join(home,path),{recursive:true});
    await writeFile(join(home,'control','secret'),'guardian secret');
    await writeFile(join(home,'core/src/runtime.mjs'),`
      import { spawn } from 'node:child_process';
      import { readFileSync, writeFileSync } from 'node:fs';
      const guardian=${process.pid};
      let signalBlocked=false, secretBlocked=false, guardianProcBlocked=false;
      try { process.kill(guardian,0); } catch { signalBlocked=true; }
      try { readFileSync('/proc/'+guardian+'/environ'); } catch { guardianProcBlocked=true; }
      try { readFileSync(${JSON.stringify(join(home,'control','secret'))}); } catch { secretBlocked=true; }
      writeFileSync(${JSON.stringify(join(home,'workspace','written'))},'ok');
      const descendant=spawn(process.execPath,['-e','setInterval(()=>{},1000)'],{detached:true,stdio:'ignore'});
      process.on('message',message=>process.send({echo:message,pid:process.pid,descendant:descendant.pid,signalBlocked,secretBlocked,guardianProcBlocked,proc:readFileSync('/proc/self/status','utf8')}));
      process.send({ready:true});
      setInterval(()=>{},1000);
    `);
    ({child}=await launchRuntime({home,revision:'test',coreRoot:join(home,'core')}));
    const next=()=>new Promise((resolve,reject)=>{
      const timer=setTimeout(()=>reject(new Error('IPC timed out')),5000);
      child.once('message',message=>{clearTimeout(timer);resolve(message);});
      child.once('exit',code=>{clearTimeout(timer);reject(new Error('Runtime exited '+code));});
    });
    let stderr='';child.stderr.on('data',data=>stderr+=data);child.stdout.resume();
    try {assert.equal((await next()).ready,true);}catch(error){throw new Error(error.message+': '+stderr);}
    const response=next();child.send('ping');const message=await response;
    assert.equal(message.echo,'ping');assert.equal(message.signalBlocked,true);assert.equal(message.secretBlocked,true);assert.equal(message.guardianProcBlocked,true);
    assert.equal(await readFile(join(home,'workspace','written'),'utf8'),'ok');
    const hostPid=await runtimeProcessGroup(child,message.descendant);
    assert.notEqual(hostPid,message.descendant);
    await assert.rejects(runtimeProcessGroup(child,process.pid),/not a runtime descendant/);
    await stopRuntime(child,new Set([hostPid]));
    // PID namespace teardown kills every descendant, including detached groups.
    await new Promise(resolve=>setTimeout(resolve,100));
    let live=false;try {live=!/^State:\s+Z/m.test(await readFile('/proc/'+hostPid+'/status','utf8'));}catch{}
    assert.equal(live,false);
  } finally {await stopRuntime(child);await rm(home,{recursive:true,force:true});}
});

test('Linux preflight keeps workspace read only and network isolated', {skip:process.platform!=='linux'}, async t=>{
  if(await sandboxAvailable()!=='bwrap'){t.skip('bubblewrap is not installed');return;}
  const home=await mkdtemp(join(tmpdir(),'bot-preflight-'));let child;
  try {
    for(const path of ['workspace','drafts','control','core/src'])await mkdir(join(home,path),{recursive:true});
    await writeFile(join(home,'core/src/runtime.mjs'),`
      import { writeFileSync, readFileSync } from 'node:fs';
      let writeBlocked=false;
      try {writeFileSync(${JSON.stringify(join(home,'workspace','write'))},'unsafe');}catch {writeBlocked=true;}
      process.send({writeBlocked,routes:readFileSync('/proc/net/route','utf8')});
      setInterval(()=>{},1000);
    `);
    ({child}=await launchRuntime({home,revision:'test',coreRoot:join(home,'core'),preflight:true}));
    child.stdout.resume();let stderr='';child.stderr.on('data',data=>stderr+=data);
    const message=await new Promise((resolve,reject)=>{
      const timer=setTimeout(()=>reject(new Error('Preflight IPC timed out: '+stderr)),5000);
      child.once('message',message=>{clearTimeout(timer);resolve(message);});
      child.once('exit',code=>{clearTimeout(timer);reject(new Error('Preflight exited '+code+': '+stderr));});
    });
    assert.equal(message.writeBlocked,true);
    assert.equal(message.routes.trim().split('\n').length,1,'private network has no routes');
  } finally {await stopRuntime(child);await rm(home,{recursive:true,force:true});}
});
