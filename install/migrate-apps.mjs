// Operator-only transition from in-process modules to executable applications.
// Back up all drafts and reject unrecognized edits before replacing any code.
import { readFile, writeFile, mkdir, cp, rm, readdir } from 'node:fs/promises';
import { join } from 'node:path';
import { createHash, randomUUID } from 'node:crypto';
const read=async path=>JSON.parse(await readFile(path,'utf8'));
const write=async(path,value)=>writeFile(path,JSON.stringify(value,null,2)+'\n',{mode:0o600});
const marker=home=>join(home,'control','process-app-migration.json');
async function verify(folder, expected, prefix='') {
  for(const item of await readdir(folder,{withFileTypes:true})) {
    const path=join(folder,item.name),key=prefix+item.name;
    if(item.isSymbolicLink())throw new Error(`Legacy package contains a symlink: ${key}`);
    if(item.isDirectory()){await verify(path,expected,key+'/');continue;}
    const hash=createHash('sha256').update(await readFile(path)).digest('hex');
    if(hash!==expected[key])throw new Error(`Legacy package ${key} has edits; migrate its source explicitly before updating. All drafts are unchanged.`);
  }
  for(const key of Object.keys(expected).filter(key=>key.startsWith(prefix)&&!key.slice(prefix.length).includes('/')))await readFile(join(folder,key.slice(prefix.length)));
}
export async function migrateApps(home,core) {
  const configPath=join(home,'drafts','config.json'),config=await read(configPath);
  if(config.apiVersion===3)return false;
  if(config.apiVersion!==2)throw new Error('Upgrade legacy API 1 installations to 0.3.0 before migrating to process apps');
  const expected=await read(new URL('./api2-package-hashes.json',import.meta.url));
  const folders=await readdir(join(home,'drafts'),{withFileTypes:true});
  for(const item of folders.filter(item=>item.isDirectory()&&item.name!=='apps'))await verify(join(home,'drafts',item.name),expected.modules,item.name+'/');
  await verify(join(home,'drafts','apps'),expected.apps);
  const backup=join(home,'control',`process-app-backup-${randomUUID()}`);
  await mkdir(backup,{mode:0o700});
  await cp(join(home,'drafts'),join(backup,'drafts'),{recursive:true});
  await cp(join(home,'workspace','BOT-API.md'),join(backup,'BOT-API.md'));
  await write(marker(home),{backup,candidate:null});
  try {
    const starter=await read(join(core,'apps','config.json'));
    const apps={...starter.apps};
    for(const [id,settings]of Object.entries(config.modules||{})) {
      const target=id==='pi-model'?'model-provider':id;
      apps[target]={...apps[target],...settings,stateNamespace:id};
    }
    for(const [id,settings]of Object.entries(config.apps||{})) {
      const target=id==='modules'?'updates':id==='pi-model'?'model-provider':id;
      apps[target]={...apps[target],...settings,stateNamespace:config.modules?.[id]?id:`app:${id}`};
    }
    const models=Object.entries(config.modules||{}).filter(([id,settings])=>['pi-model','demo-model'].includes(id)&&settings.enabled).map(([id])=>id);
    if(models.length!==1)throw new Error('Enable exactly one legacy model before migration');
    apps.agent={enabled:true,modelApp:models[0]==='pi-model'?'model-provider':models[0],contextApp:apps.context?.enabled?'context':null,contributors:['memory','skills'].filter(id=>apps[id]?.enabled),projections:apps.chat?.enabled?['chat']:[],tickApps:apps.scheduler?.enabled?['scheduler']:[],services:apps.chat?.enabled?['chat']:[],maxSteps:config.runtime?.maxSteps||100};
    const next={...config,apiVersion:3,controller:'agent',apps};delete next.modules;
    await rm(join(home,'drafts','apps'),{recursive:true});
    for(const item of folders.filter(item=>item.isDirectory()&&item.name!=='apps'))await rm(join(home,'drafts',item.name),{recursive:true});
    await cp(join(core,'apps'),join(home,'drafts','apps'),{recursive:true});
    await write(configPath,next);
    await cp(join(core,'docs','contract.md'),join(home,'workspace','BOT-API.md'));
    return true;
  }catch(error){await restoreApps(home);throw error;}
}
export async function markCandidate(home,candidate){const record=await read(marker(home));await write(marker(home),{...record,candidate});}
export async function restoreApps(home,candidate){
  let record;try{record=await read(marker(home));}catch(error){if(error.code==='ENOENT')return;throw error;}
  if(candidate&&record.candidate!==candidate)return;
  await rm(join(home,'drafts'),{recursive:true});await cp(join(record.backup,'drafts'),join(home,'drafts'),{recursive:true});
  await cp(join(record.backup,'BOT-API.md'),join(home,'workspace','BOT-API.md'));await rm(marker(home));
}
