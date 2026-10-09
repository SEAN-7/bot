import { fixtureApps, fixtureSource } from './process-fixtures.mjs';
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, cp, symlink, writeFile, readFile, readdir, rm } from 'node:fs/promises';
import { join, relative } from 'node:path';
import { tmpdir } from 'node:os';
import { CORE_ROOT, initialize, stageCore, stageDrafts, readCurrent, readJSON, metadataPath } from '../src/releases.mjs';

test('operator core staging validates the API and snapshots trusted installed source', async t => {
  const directory = await mkdtemp(join(tmpdir(), 'bot-core-update-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const home = join(directory, 'home');
  const defaultsDir = join(directory, 'defaults');
  await mkdir(defaultsDir);
  await initialize(home, fixtureSource({ modules: {}, runtime: { sandbox: 'off' } }), { appsDir: await fixtureApps(defaultsDir) });
  const original = await readCurrent(home);
  const source = join(directory, 'installed-core');
  await mkdir(source);
  await cp(join(CORE_ROOT, 'src'), join(source, 'src'), { recursive: true });
  const pkg = await readJSON(join(CORE_ROOT, 'package.json'));
  await writeFile(join(source, 'package.json'), JSON.stringify({ ...pkg, botCoreApi: 999 }));
  await symlink(join(CORE_ROOT, 'node_modules'), join(source, 'node_modules'), 'dir');
  const beforeCores = await readdir(join(home, 'cores'));
  await assert.rejects(stageCore(home, source), /API is incompatible/);
  assert.deepEqual(await readdir(join(home, 'cores')), beforeCores);
  assert.equal((await readJSON(metadataPath(home))).pending, null);

  await writeFile(join(source, 'package.json'), JSON.stringify(pkg));
  const runtime = await readFile(join(source, 'src', 'runtime.mjs'), 'utf8');
  const revision = await stageCore(home, source);
  const candidate = await readJSON(join(home, 'revisions', revision, 'config.json'));
  assert.notEqual(candidate.coreRoot, original.coreRoot);
  assert.ok(!relative(join(home, 'cores'), candidate.coreRoot).startsWith('..'));
  await writeFile(join(source, 'src', 'runtime.mjs'), '// changed operator installation\n');
  assert.equal(await readFile(join(candidate.coreRoot, 'src', 'runtime.mjs'), 'utf8'), runtime);
  assert.equal((await readCurrent(home)).revision, original.revision);

  const metadata = await readJSON(metadataPath(home));
  await writeFile(metadataPath(home), JSON.stringify({ ...metadata, pending: null }));
  const writableSource = join(home, 'workspace', 'untrusted-core');
  await mkdir(writableSource);
  await cp(join(source, 'src'), join(writableSource, 'src'), { recursive: true });
  await writeFile(join(writableSource, 'package.json'), JSON.stringify(pkg));
  await symlink(join(CORE_ROOT, 'node_modules'), join(writableSource, 'node_modules'), 'dir');
  await assert.rejects(stageCore(home, writableSource), /agent-writable/);
  const draftPath = join(home, 'drafts', 'config.json');
  const draft = await readJSON(draftPath);
  await writeFile(draftPath, JSON.stringify({ ...draft, coreRoot: writableSource }));
  await assert.rejects(stageDrafts(home), /Only the operator/);
});

test('operator core update completes health probation without model calls or invented chat input', { timeout: 10000 }, async t => {
  const { startSupervisor } = await import('../src/supervisor.mjs');
  const directory = await mkdtemp(join(tmpdir(), 'bot-core-idle-validation-'));
  const home = join(directory, 'home'), defaultsDir = join(directory, 'defaults'), model = join(defaultsDir, 'offline-model');
  let server;
  t.after(async () => { await server?.stop(); await rm(directory, { recursive: true, force: true }); });
  await mkdir(model, { recursive: true });
  await writeFile(join(model, 'module.json'), JSON.stringify({ id: 'offline-model', name: 'Offline', version: '1', apiVersion: 2, entry: 'index.mjs' }));
  await writeFile(join(model, 'index.mjs'), 'export default api => api.registerModel({complete(){throw new Error("No model calls allowed during core validation")}});');
  await initialize(home, fixtureSource({ modules: { 'offline-model': { enabled: true } }, runtime: { port: 0, sandbox: 'off', trialMs: 500 } }), { appsDir: await fixtureApps(defaultsDir), fixedCoreRoot: CORE_ROOT });
  const original = await readCurrent(home);
  const source = join(directory, 'candidate-core');
  await mkdir(source);
  await cp(join(CORE_ROOT, 'src'), join(source, 'src'), { recursive: true });
  await cp(join(CORE_ROOT, 'package.json'), join(source, 'package.json'));
  await symlink(join(CORE_ROOT, 'node_modules'), join(source, 'node_modules'), 'dir');
  await stageCore(home, source, { fixed: true });
  server = await startSupervisor({ home, port: 0, unsafe: true });
  let status;
  for (let index=0;index<120;index++) {
    status=await fetch(server.url+'/api/status', {headers:{authorization:'Bearer '+server.token}}).then(r=>r.json());
    if(status.state==='idle'&&!status.trial)break;
    await new Promise(resolve=>setTimeout(resolve,25));
  }
  assert.equal(status.state,'idle');
  assert.equal(status.trial,null);
  assert.equal(status.pending,0);
  assert.equal(status.canRecover,true);
  assert.equal((await readJSON(metadataPath(home))).previous,original.revision);
  const messages=await fetch(server.url+'/api/messages',{headers:{authorization:'Bearer '+server.token}}).then(r=>r.json());
  assert.deepEqual(messages.messages,[]);
});

test('cross-API recovery requires the retained operator executable and leaves the current service intact',async t=>{
  const {startSupervisor}=await import('../src/supervisor.mjs');
  const home=await mkdtemp(join(tmpdir(),'bot-api-recovery-'));let server;
  t.after(async()=>{await server?.stop();await rm(home,{recursive:true,force:true});});
  const {createConfig}=await import('../src/cli.mjs');await initialize(home,createConfig({}));
  const current=await readCurrent(home);await mkdir(join(home,'revisions','legacy-api1'));
  await writeFile(join(home,'revisions','legacy-api1','config.json'),JSON.stringify({...current.config,apiVersion:1}));
  await writeFile(metadataPath(home),JSON.stringify({...await readJSON(metadataPath(home)),previous:'legacy-api1'}));
  server=await startSupervisor({home,port:0,unsafe:true});
  const headers={authorization:'Bearer '+server.token};let status;
  for(let i=0;i<200;i++){status=await fetch(server.url+'/api/status',{headers}).then(r=>r.json());if(status.state==='idle')break;await new Promise(r=>setTimeout(r,25));}
  assert.equal(status.state,'idle');assert.equal(status.canRecover,false);
  const response=await fetch(server.url+'/api/recover',{method:'POST',headers});assert.equal(response.status,400);assert.match((await response.json()).error,/operator must restore/);
  assert.equal((await fetch(server.url+'/api/status',{headers}).then(r=>r.json())).state,'idle');
  assert.equal((await readCurrent(home)).revision,current.revision);
});
