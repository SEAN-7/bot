import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp,mkdir,writeFile,readFile,rm } from 'node:fs/promises';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { migrateApps,markCandidate,restoreApps } from '../install/migrate-apps.mjs';
import { CORE_ROOT } from '../src/releases.mjs';
import { Store } from '../src/store.mjs';
async function legacy(t) {
  const home=await mkdtemp(join(tmpdir(),'bot-process-migration-'));t.after(()=>rm(home,{recursive:true,force:true}));
  for(const dir of ['drafts','workspace','control'])await mkdir(join(home,dir));
  await promisify(execFile)('tar',['-xzf',new URL('./fixtures/api2-drafts.tar.gz',import.meta.url).pathname,'-C',join(home,'drafts')]);
  await writeFile(join(home,'drafts','config.json'),JSON.stringify({apiVersion:2,modules:{chat:{enabled:true},memory:{enabled:true},scheduler:{enabled:true},context:{enabled:true},skills:{enabled:true},'pi-model':{enabled:false},'demo-model':{enabled:true}},apps:Object.fromEntries(['chat','memory','scheduler','context','skills','web-search','modules'].map(id=>[id,{enabled:true}])),model:{provider:'demo'},shell:{outputBytes:2048},coreRoot:'old-core'}));
  await writeFile(join(home,'workspace','BOT-API.md'),'legacy reference');return home;
}
test('process migration preserves the journal, state namespaces, credentials and exact rollback drafts',async t=>{
  const home=await legacy(t),store=new Store(home);store.setState('memory','memory',{revision:7,USER:'retained',notes:'retained'});store.setState('scheduler','schedules',[{id:'retained'}]);store.append({role:'user',content:'existing human history'});
  const history=store.history(),state=store.snapshot();store.close();await writeFile(join(home,'control','credentials-fixture'),'unchanged credential');
  const original=await readFile(join(home,'drafts','config.json'),'utf8');
  assert.equal(await migrateApps(home,CORE_ROOT),true);await markCandidate(home,'candidate');
  const config=JSON.parse(await readFile(join(home,'drafts','config.json')));assert.equal(config.apiVersion,3);assert.equal(config.modules,undefined);assert.equal(config.apps.updates.enabled,true);assert.equal(config.apps.memory.stateNamespace,'memory');assert.equal(config.apps.agent.modelApp,'demo-model');assert.equal(config.shell.outputBytes,2048);
  const unchanged=new Store(home);assert.deepEqual(unchanged.history(),history);assert.deepEqual(unchanged.snapshot(),state);unchanged.close();assert.equal(await readFile(join(home,'control','credentials-fixture'),'utf8'),'unchanged credential');
  await assert.rejects(readFile(join(home,'drafts','memory','module.json')),{code:'ENOENT'});
  await restoreApps(home,'another-candidate');assert.equal(JSON.parse(await readFile(join(home,'drafts','config.json'))).apiVersion,3);
  await restoreApps(home,'candidate');assert.equal(await readFile(join(home,'drafts','config.json'),'utf8'),original);assert.equal(await readFile(join(home,'workspace','BOT-API.md'),'utf8'),'legacy reference');assert.ok(await readFile(join(home,'drafts','memory','module.json')));
});
test('edited behavior and app packages block migration before any drafts are replaced',async t=>{
  const home=await legacy(t),path=join(home,'drafts','memory','index.mjs');await writeFile(path,'custom behavior');
  const before=await readFile(join(home,'drafts','config.json'),'utf8');await assert.rejects(migrateApps(home,CORE_ROOT),/memory\/index.mjs has edits/);
  assert.equal(await readFile(join(home,'drafts','config.json'),'utf8'),before);assert.equal(await readFile(path,'utf8'),'custom behavior');await assert.rejects(readFile(join(home,'control','process-app-migration.json')),{code:'ENOENT'});
});
test('custom legacy modules cannot accidentally survive as loadable plugins',async t=>{
  const home=await legacy(t);await mkdir(join(home,'drafts','custom'));await writeFile(join(home,'drafts','custom','index.mjs'),'custom behavior');await assert.rejects(migrateApps(home,CORE_ROOT),/custom\/index.mjs has edits/);
});
