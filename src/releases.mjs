import { mkdir, readFile, writeFile, rename, readdir, cp, realpath, rm, access, symlink } from 'node:fs/promises';
import { dirname, join, resolve, isAbsolute, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { randomUUID } from 'node:crypto';
import { Store } from './store.mjs';

export const CORE_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..');
export const API_VERSION = 1;
export async function readJSON(path) { return JSON.parse(await readFile(path,'utf8')); }
export async function atomicJSON(path,data) { await mkdir(dirname(path),{recursive:true,mode:0o700}); const temp=`${path}.${randomUUID()}.tmp`; await writeFile(temp,JSON.stringify(data,null,2)+'\n',{mode:0o600}); await rename(temp,path); }
export const metadataPath = home => join(home,'control','releases.json');
export async function manifestAt(source) {
  const manifest=await readJSON(join(source,'module.json'));
  if(!/^[a-z][a-z0-9-]{0,63}$/.test(manifest.id)||manifest.apiVersion!==API_VERSION||typeof manifest.version!=='string'||typeof manifest.entry!=='string') throw new Error('Invalid or incompatible module manifest');
  for(const field of ['entry','acp']) if(manifest[field]) {
    if(isAbsolute(manifest[field])||relative(source,resolve(source,manifest[field])).startsWith('..')) throw new Error(`Module ${field} escapes its package`);
    const real=await realpath(join(source,manifest[field]));
    if(relative(await realpath(source),real).startsWith('..')) throw new Error('Module paths cannot escape via symlinks');
  }
  return manifest;
}
async function rejectSymlinks(path) { for(const entry of await readdir(path,{withFileTypes:true})) { const p=join(path,entry.name); if(entry.isSymbolicLink()) throw new Error(`Module package contains a symlink: ${entry.name}`); if(entry.isDirectory()) await rejectSymlinks(p); } }
function inside(parent,child) {const path=relative(parent,child);return path===''||(!path.startsWith('..')&&!isAbsolute(path));}
async function coreSnapshot(home,source,{copyDependencies=false}={}) {
  source=await realpath(source);
  const writable=await Promise.all(['workspace','drafts'].map(p=>realpath(join(home,p))));
  const packageInfo=await readJSON(join(source,'package.json'));
  if(packageInfo.botCoreApi!==API_VERSION)throw new Error('Core module API is incompatible; migration required');
  for(const path of [source,await realpath(join(source,'src')),await realpath(join(source,'node_modules'))]) {
    if(writable.some(root=>inside(root,path)))throw new Error('Core cannot live inside agent-writable directories');
  }
  await rejectSymlinks(join(source,'src'));
  const dest=join(home,'cores',`${Date.now()}-${randomUUID().slice(0,8)}`);
  await mkdir(dest,{recursive:true});
  try {
    await cp(join(source,'src'),join(dest,'src'),{recursive:true});
    await cp(join(source,'package.json'),join(dest,'package.json'));
    if(copyDependencies)await cp(join(source,'node_modules'),join(dest,'node_modules'),{recursive:true,dereference:true});
    else await symlink(await realpath(join(source,'node_modules')),join(dest,'node_modules'),'dir');
    return dest;
  } catch(error){await rm(dest,{recursive:true,force:true});throw error;}
}
export async function readCurrent(home) {
  const meta=await readJSON(metadataPath(home));
  const revision=meta.active;
  const config=await readJSON(join(home,'revisions',revision,'config.json'));
  return { ...meta, revision, config, coreRoot:config.coreRoot||CORE_ROOT };
}
export async function initialize(home,config,{defaultsDir=join(CORE_ROOT,'defaults'),fixedCoreRoot}={}) {
  home=resolve(home);
  try { await access(metadataPath(home)); throw new Error('This bot home is already initialized'); } catch(error) { if(error.code!=='ENOENT') throw error; }
  await mkdir(join(home,'control'),{recursive:true,mode:0o700});
  for(const dir of ['drafts','workspace','revisions']) await mkdir(join(home,dir),{recursive:true});
  config={apiVersion:API_VERSION,modules:{},model:{},...config,coreRoot:fixedCoreRoot ? await trustedCore(fixedCoreRoot) : await coreSnapshot(home,config.coreRoot||CORE_ROOT)};
  if(config.apiVersion!==API_VERSION) throw new Error('Unsupported configuration API');
  for(const entry of await readdir(defaultsDir,{withFileTypes:true})) if(entry.isDirectory()) {
    const source=join(defaultsDir,entry.name); const m=await manifestAt(source);
    await cp(source,join(home,'drafts',m.id),{recursive:true,errorOnExist:true,force:false});
  }
  await atomicJSON(join(home,'drafts','config.json'),config);
  try {await cp(join(CORE_ROOT,'docs','contract.md'),join(home,'workspace','BOT-API.md'));}catch(error){if(error.code!=='ENOENT')throw error;}
  const revision=await createBundle(home,config);
  await atomicJSON(metadataPath(home),{active:revision,previous:null,pending:null,trial:null});
  const store=new Store(home); store.record('installation.created',{revision}); store.close();
  return {revision,config};
}
async function createBundle(home,config) {
  if(config.apiVersion!==API_VERSION) throw new Error('Unsupported configuration API');
  const id=`${Date.now()}-${randomUUID().slice(0,8)}`;
  const dest=join(home,'revisions',id);
  await mkdir(join(dest,'plugins'),{recursive:true});
  try {
    for(const [id,options] of Object.entries(config.modules||{})) {
      if(!options.enabled) continue;
      const source=join(home,'drafts',id); const manifest=await manifestAt(source);
      if(manifest.id!==id) throw new Error('Module directory/ID mismatch');
      await rejectSymlinks(source);
      await cp(source,join(dest,'plugins',id),{recursive:true});
    }
    await atomicJSON(join(dest,'config.json'),config);
    await symlink(join(config.coreRoot,'node_modules'),join(dest,'node_modules'),'dir');
    return id;
  } catch(error) { await rm(dest,{recursive:true,force:true}); throw error; }
}
async function stage(home,coreOverride) {
  const meta=await readJSON(metadataPath(home));
  if(meta.pending||meta.trial) throw new Error('An update is already pending or on trial');
  const config=await readJSON(join(home,'drafts','config.json'));
  const current=await readCurrent(home);
  if(config.coreRoot!==current.coreRoot&&!coreOverride) throw new Error('Only the operator can update core');
  if(JSON.stringify(config.runtime||{})!==JSON.stringify(current.config.runtime||{})) throw new Error('Runtime protection settings are operator-owned');
  config.coreRoot=coreOverride||current.coreRoot;
  const pending=await createBundle(home,config);
  await atomicJSON(metadataPath(home),{...meta,pending});
  return pending;
}
export async function stageDrafts(home) { return stage(home); }
export async function installModule(home,source,{enable=true}={}) {
  source=resolve(source); const manifest=await manifestAt(source); await rejectSymlinks(source);
  const draft=join(home,'drafts',manifest.id);
  if(source===resolve(draft)) throw new Error('Source is already the installed draft');
  const temp=join(home,'drafts',`.install-${randomUUID()}`);
  await cp(source,temp,{recursive:true});
  await rm(draft,{recursive:true,force:true}); await rename(temp,draft);
  const config=await readJSON(join(home,'drafts','config.json'));
  config.modules[manifest.id]={...config.modules[manifest.id],enabled:enable};
  await atomicJSON(join(home,'drafts','config.json'),config);
  return manifest;
}
async function trustedCore(source) {
  source=await realpath(resolve(source));
  const pkg=await readJSON(join(source,'package.json'));
  if(pkg.botCoreApi!==API_VERSION) throw new Error('Core module API is incompatible; migration required');
  await access(join(source,'src','runtime.mjs'));
  return source;
}
export async function stageCore(home,source,{fixed=false}={}) {
  source=await realpath(resolve(source));
  const pkg=await readJSON(join(source,'package.json'));
  if(pkg.botCoreApi!==API_VERSION) throw new Error('Core module API is incompatible; migration required');
  await access(join(source,'src','runtime.mjs'));
  // The operator supplies an installed, trusted core release. Modules cannot call this path.
  if(fixed)return stage(home,source);
  const snapshot=await coreSnapshot(home,source,{copyDependencies:true});
  try{return await stage(home,snapshot);}catch(error){await rm(snapshot,{recursive:true,force:true});throw error;}
}
export async function beginTrial(home,store) {
  const meta=await readJSON(metadataPath(home));
  if(!meta.pending) return null;
  const snapshot=store.snapshot();
  await atomicJSON(join(home,'revisions',meta.pending,'before-state.json'),snapshot);
  const trial={from:meta.active,to:meta.pending,startedAt:Date.now()};
  await atomicJSON(metadataPath(home),{...meta,active:meta.pending,pending:null,trial});
  store.record('update.trial',trial);
  return trial;
}
export async function promote(home,store) {
  const meta=await readJSON(metadataPath(home)); if(!meta.trial) return;
  await atomicJSON(metadataPath(home),{...meta,previous:meta.trial.from,trial:null});
  store.record('update.promoted',{revision:meta.active,previous:meta.trial.from});
}
export async function rollback(home,existingStore) {
  const store=existingStore||new Store(home);
  try {
    const meta=await readJSON(metadataPath(home));
    const target=meta.trial?.from||meta.previous;
    if(!target) throw new Error('No previous working version is available');
    const snapshot=await readJSON(join(home,'revisions',meta.active,'before-state.json'));
    store.restore(snapshot);
    const next={active:target,previous:null,pending:null,trial:null};
    // Preserve the previous release's own rollback chain, when it has one.
    const targetConfig=await readJSON(join(home,'revisions',target,'config.json'));
    await atomicJSON(metadataPath(home),next);
    await atomicJSON(join(home,'drafts','config.json'),targetConfig);
    store.record('update.rolled_back',{from:meta.active,to:target});
    return target;
  } finally { if(!existingStore) store.close(); }
}
