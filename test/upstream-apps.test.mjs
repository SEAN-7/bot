import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, readFile, rm, access, realpath } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { initialize, readCurrent } from '../src/releases.mjs';
import { createConfig } from '../src/cli.mjs';
import { startSupervisor } from '../src/supervisor.mjs';

const exec=promisify(execFile), launcher=await realpath(new URL('../src/app-cli.mjs',import.meta.url));
async function folder(t){const root=await realpath(await mkdtemp(join(tmpdir(),'bot-upstream-')));t.after(()=>rm(root,{recursive:true,force:true}));return root;}
async function until(check){for(let i=0;i<400;i++){if(await check())return;await new Promise(r=>setTimeout(r,25));}assert.fail('Upstream inspection timed out');}

test('native upstream discovery reads metadata without invoking apps or changing the bot fork',async t=>{
  const root=await folder(t), upstream=join(root,'upstream'),working=join(root,'working');
  await mkdir(join(upstream,'apps/telegram'),{recursive:true});await mkdir(join(upstream,'apps/_lib'));
  await mkdir(join(working,'telegram'),{recursive:true});
  await writeFile(join(upstream,'package.json'),JSON.stringify({version:'upstream-release'}));
  await writeFile(join(upstream,'apps/telegram/app.json'),JSON.stringify({id:'telegram',version:'app-version',command:['node','main.mjs']}));
  await writeFile(join(upstream,'apps/telegram/SKILL.md'),'upstream guide');
  const upstreamProgram=`import {writeFileSync} from 'node:fs';writeFileSync(${JSON.stringify(join(root,'executed'))},'unexpected');`;
  await writeFile(join(upstream,'apps/telegram/main.mjs'),upstreamProgram);
  await writeFile(join(working,'telegram/main.mjs'),'bot-owned fork with reactions');
  const env={...process.env,BOT_APPS:working,BOT_UPSTREAM_ROOT:upstream};
  const run=(...args)=>exec(process.execPath,[launcher,...args],{env});
  const result=JSON.parse((await run('--upstream','telegram')).stdout);
  assert.deepEqual(result,{root:upstream,version:'upstream-release',apps:[{id:'telegram',version:'app-version',sourcePath:join(upstream,'apps/telegram'),workingPath:join(working,'telegram')}]});
  assert.deepEqual(JSON.parse((await run('--upstream')).stdout),result);
  assert.equal(await readFile(join(working,'telegram/main.mjs'),'utf8'),'bot-owned fork with reactions');
  assert.equal(await readFile(join(upstream,'apps/telegram/main.mjs'),'utf8'),upstreamProgram);
  await assert.rejects(access(join(root,'executed')),{code:'ENOENT'});
  await assert.rejects(run('--upstream','../telegram'),/Usage: bot-app --upstream/);
  await assert.rejects(run('--upstream','telegram','extra'),/Usage: bot-app --upstream/);
  await assert.rejects(run('--upstream','custom-fork'),/No bundled upstream app/);
});

test('core-only sources have an empty upstream catalog without inventing an app baseline',async t=>{
  const root=await folder(t);await writeFile(join(root,'package.json'),JSON.stringify({version:'core-only'}));
  const {stdout}=await exec(process.execPath,[launcher,'--upstream'],{env:{...process.env,BOT_APPS:join(root,'working'),BOT_UPSTREAM_ROOT:root}});
  assert.deepEqual(JSON.parse(stdout),{root,version:'core-only',apps:[]});
});

test('supervised shells inspect the running core snapshot while forked app copies remain independent',async t=>{
  let server;
  t.after(()=>server?.stop());
  const home=await folder(t);
  await initialize(home,createConfig({disable:'chat,context,memory,scheduler,skills,web-search'}));
  const current=await readCurrent(home), source=join(current.coreRoot,'apps/telegram/commands.mjs');
  const original=await readFile(source,'utf8'),working=join(home,'drafts/apps/telegram/commands.mjs');
  await writeFile(working,original+'\n// bot-owned reaction customization\n');
  await writeFile(join(home,'drafts/apps/demo-model/behavior.mjs'),`export const createModel=()=>({async complete({messages}){
    return messages.some(m=>m.role==='tool')?{text:'',toolCalls:[]}:{text:'',toolCalls:[{id:'inspect-upstream',name:'shell',arguments:{command:'bot-app --upstream telegram',yieldTimeMs:30000}}]};
  }});`);
  const previous=process.env.BOT_UPSTREAM_ROOT;process.env.BOT_UPSTREAM_ROOT='/ignored-inherited-source';
  try{server=await startSupervisor({home,port:0,unsafe:true});}finally{if(previous===undefined)delete process.env.BOT_UPSTREAM_ROOT;else process.env.BOT_UPSTREAM_ROOT=previous;}
  const api=(path,body)=>fetch(server.url+path,{method:body?'POST':'GET',headers:{authorization:'Bearer '+server.token,'content-type':'application/json'},body:body?JSON.stringify(body):undefined}).then(r=>r.json());
  await until(async()=>(await api('/api/status')).state==='idle');
  await api('/api/messages',{text:'isolated upstream inspection'});
  await until(async()=>(await api('/api/status')).pending===0);
  const history=(await api('/api/messages')).messages, tool=history.find(m=>m.role==='tool');
  const result=JSON.parse(tool.content);assert.equal(result.exitCode,0,result.stderr);
  const catalog=JSON.parse(result.stdout);
  assert.equal(resolve(catalog.root),resolve(current.coreRoot));
  assert.equal(catalog.version,JSON.parse(await readFile(join(current.coreRoot,'package.json'))).version);
  assert.equal(catalog.apps[0].sourcePath,join(current.coreRoot,'apps/telegram'));
  assert.equal(catalog.apps[0].workingPath,join(home,'drafts/apps/telegram'));
  assert.equal(await readFile(source,'utf8'),original);
  assert.equal(await readFile(working,'utf8'),original+'\n// bot-owned reaction customization\n');
});
