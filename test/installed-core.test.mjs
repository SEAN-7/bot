import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, rm, access, symlink, realpath } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { CORE_ROOT, initialize, stageCore, readCurrent, atomicJSON, readJSON } from '../src/releases.mjs';
import { startSupervisor } from '../src/supervisor.mjs';

const model = label => `export default async function(api) { api.registerModel({async complete(){return {text:${JSON.stringify(label)},toolCalls:[]};}}); }`;
async function fixture(t) {
  const directory = await realpath(await mkdtemp(join(tmpdir(), 'bot-installed-core-')));
  t.after(() => rm(directory, {recursive:true,force:true}));
  const home = join(directory,'home'), defaultsDir = join(directory,'defaults');
  await mkdir(join(defaultsDir,'test-model'),{recursive:true});
  await writeFile(join(defaultsDir,'test-model/module.json'),JSON.stringify({id:'test-model',apiVersion:1,version:'1',entry:'index.mjs'}));
  await writeFile(join(defaultsDir,'test-model/index.mjs'),model('original'));
  await initialize(home,{coreRoot:join(directory,'ignored-untrusted-core'),modules:{'test-model':{enabled:true}},runtime:{port:0,trialMs:50,retryMs:100}}, {defaultsDir,fixedCoreRoot:CORE_ROOT});
  return {directory,home};
}
async function until(check) {
  const deadline=Date.now()+10000;
  while(Date.now()<deadline){const value=await check();if(value)return value;await new Promise(resolve=>setTimeout(resolve,40));}
  assert.fail('Timed out waiting for installed runtime');
}

test('installed initialization and core staging reference trusted releases without writable core copies', async t => {
  const {directory,home}=await fixture(t);
  assert.equal((await readCurrent(home)).coreRoot,await realpath(CORE_ROOT));
  await assert.rejects(access(join(home,'cores')),{code:'ENOENT'});
  const installed=join(directory,'installed-core');
  await symlink(CORE_ROOT,installed,'dir');
  const revision=await stageCore(home,installed,{fixed:true});
  assert.equal((await readJSON(join(home,'revisions',revision,'config.json'))).coreRoot,await realpath(CORE_ROOT));
  await assert.rejects(access(join(home,'cores')),{code:'ENOENT'});
});

test('fixed supervisor ignores writable core paths during startup and plugin preflight and refuses API core replacement', async t => {
  const {directory,home}=await fixture(t);
  const malicious=join(home,'workspace','malicious-core');
  await mkdir(join(malicious,'src'),{recursive:true});
  await writeFile(join(malicious,'src/runtime.mjs'),"throw new Error('Untrusted core was executed');\n");
  const current=await readCurrent(home);
  const poisoned={...current.config,coreRoot:malicious};
  await atomicJSON(join(home,'revisions',current.revision,'config.json'),poisoned);
  await atomicJSON(join(home,'drafts','config.json'),poisoned);
  const pointer=join(directory,'installed-pointer');
  await symlink(CORE_ROOT,pointer,'dir');
  const server=await startSupervisor({home,port:0,unsafe:true,fixedCoreRoot:pointer});
  t.after(()=>server.stop());
  const request=(path,body)=>fetch(new URL(path,server.url),{method:body===undefined?'GET':'POST',headers:{authorization:`Bearer ${server.token}`,'content-type':'application/json'},...(body===undefined?{}:{body:JSON.stringify(body)})});
  const status=async()=> (await request('/api/status')).json();
  await until(async()=> (await status()).state==='idle');
  assert.equal((await status()).coreRoot,await realpath(CORE_ROOT));
  // Repointing the installation symlink after launch cannot redirect preflight.
  await rm(pointer);await symlink(malicious,pointer,'dir');
  const denied=await request('/api/core-update',{source:malicious});
  assert.equal(denied.status,403);
  assert.match((await denied.json()).error,/bot update/);
  await writeFile(join(home,'drafts/test-model/index.mjs'),model('updated plugin'));
  const staged=await request('/api/update',{});
  assert.equal(staged.status,202);
  const revision=(await staged.json()).revision;
  await until(async()=>{const value=await status();return value.revision===revision&&value.state==='idle';});
  assert.equal((await status()).coreRoot,await realpath(CORE_ROOT));
  await request('/api/messages',{text:'test plugin update'});
  await until(async()=>{const value=await (await request('/api/messages')).json();return value.messages.some(message=>message.role==='assistant'&&message.content==='updated plugin');});
});
