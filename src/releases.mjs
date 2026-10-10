import { mkdir, readFile, writeFile, rename, readdir, cp, realpath, rm, access, symlink } from 'node:fs/promises';
import { dirname, join, resolve, isAbsolute, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { randomUUID } from 'node:crypto';
import { Store } from './store.mjs';
import { appManifest, APP_ID } from './app-package.mjs';

export const CORE_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..');
export const API_VERSION = 3;
export async function readJSON(path) { return JSON.parse(await readFile(path,'utf8')); }
export async function atomicJSON(path,data) { await mkdir(dirname(path),{recursive:true,mode:0o700}); const temp=`${path}.${randomUUID()}.tmp`; await writeFile(temp,JSON.stringify(data,null,2)+'\n',{mode:0o600}); await rename(temp,path); }
export const metadataPath = home => join(home,'control','releases.json');
async function rejectSymlinks(path) { for(const entry of await readdir(path,{withFileTypes:true})) { const p=join(path,entry.name); if(entry.isSymbolicLink()) throw new Error(`App package contains a symlink: ${entry.name}`); if(entry.isDirectory()) await rejectSymlinks(p); } }
function inside(parent,child) {const path=relative(parent,child);return path===''||(!path.startsWith('..')&&!isAbsolute(path));}
async function coreSnapshot(home,source,{copyDependencies=false}={}) {
  source=await realpath(source);
  const writable=await Promise.all(['workspace','drafts'].map(p=>realpath(join(home,p))));
  const packageInfo=await readJSON(join(source,'package.json'));
  if(packageInfo.botCoreApi!==API_VERSION)throw new Error('Core application API is incompatible; migration required');
  for(const path of [source,await realpath(join(source,'src')),await realpath(join(source,'node_modules'))]) {
    if(writable.some(root=>inside(root,path)))throw new Error('Core cannot live inside agent-writable directories');
  }
  await rejectSymlinks(join(source,'src'));
  const dest=join(home,'cores',`${Date.now()}-${randomUUID().slice(0,8)}`);
  await mkdir(dest,{recursive:true});
  try {
    await cp(join(source,'src'),join(dest,'src'),{recursive:true});
    await cp(join(source,'package.json'),join(dest,'package.json'));
    // Keep bundled app sources available for the bot to inspect and merge itself.
    // A core-only source is still valid; it simply has no bundled app catalog.
    try{await cp(join(source,'apps'),join(dest,'apps'),{recursive:true,dereference:true});}catch(error){if(error.code!=='ENOENT')throw error;}
    if(copyDependencies)await cp(join(source,'node_modules'),join(dest,'node_modules'),{recursive:true,dereference:true});
    else await symlink(await realpath(join(source,'node_modules')),join(dest,'node_modules'),'dir');
    return dest;
  } catch(error){await rm(dest,{recursive:true,force:true});throw error;}
}
export async function readCurrent(home) {
  const meta=await readJSON(metadataPath(home));
  const revision=meta.active;
  const config=await readJSON(join(home,'revisions',revision,'config.json'));
  const recoveryRevision=meta.trial?.from||meta.previous;
  const recoveryConfig=recoveryRevision?await readJSON(join(home,'revisions',recoveryRevision,'config.json')):null;
  return { ...meta, revision, config, compatibleRecovery: recoveryConfig?.apiVersion===API_VERSION, coreRoot:config.coreRoot||CORE_ROOT };
}
export async function initialize(home,config,{appsDir=join(CORE_ROOT,'apps'),fixedCoreRoot}={}) {
  home=resolve(home);
  try { await access(metadataPath(home)); throw new Error('This bot home is already initialized'); } catch(error) { if(error.code!=='ENOENT') throw error; }
  await mkdir(join(home,'control'),{recursive:true,mode:0o700});
  for(const dir of ['drafts','workspace','revisions']) await mkdir(join(home,dir),{recursive:true});
  const starter=await readJSON(join(CORE_ROOT,'apps','config.json'));
  if(config.modules)throw new Error('Plugin modules are unsupported; migrate to process apps');
  config={apiVersion:API_VERSION,...starter,...config,apps:{...starter.apps,...config.apps},coreRoot:fixedCoreRoot ? await trustedCore(fixedCoreRoot) : await coreSnapshot(home,config.coreRoot||CORE_ROOT)};
  if(config.apiVersion!==API_VERSION)throw new Error('Unsupported configuration API');
  await cp(join(CORE_ROOT,'apps'),join(home,'drafts','apps'),{recursive:true});
  if(resolve(appsDir)!==join(CORE_ROOT,'apps'))await cp(appsDir,join(home,'drafts','apps'),{recursive:true});
  await atomicJSON(join(home,'drafts','config.json'),config);
  await atomicJSON(join(home,'control','clients.json'),config.clients||{});
  await symlink(join(config.coreRoot,'node_modules'),join(home,'drafts','node_modules'),'dir');
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
  await mkdir(join(dest,'apps'),{recursive:true});
  try {
    if(config.modules)throw new Error('Plugin modules are unsupported');
    if(!APP_ID.test(config.controller)||!config.apps?.[config.controller]?.enabled)throw new Error('Enable one controller application');
    await rejectSymlinks(join(home,'drafts','apps',config.controller));
    const source=join(home,'drafts','apps',config.controller);
    const manifest=await appManifest(source);
    if(manifest.id!==config.controller)throw new Error('App directory/ID mismatch');
    await cp(source,join(dest,'apps',config.controller),{recursive:true});
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
export async function stageDrafts(home,{fixedCoreRoot}={}) { return stage(home,fixedCoreRoot?await trustedCore(fixedCoreRoot):undefined); }
export async function installApp(home,source,{enable=true}={}) {
  source=resolve(source);const manifest=await appManifest(source);
  const current=await readCurrent(home);
  if(manifest.id===current.config.controller)await rejectSymlinks(source);
  const draft=join(home,'drafts','apps',manifest.id);
  if(source===resolve(draft))throw new Error('Source is already the installed draft');
  const temp=join(home,'drafts',`.install-${randomUUID()}`);
  await cp(source,temp,{recursive:true});
  await rm(draft,{recursive:true,force:true});await rename(temp,draft);
  const config=await readJSON(join(home,'drafts','config.json'));
  config.apps[manifest.id]={...config.apps[manifest.id],enabled:enable};
  await atomicJSON(join(home,'drafts','config.json'),config);return manifest;
}
async function trustedCore(source) {
  source=await realpath(resolve(source));
  const pkg=await readJSON(join(source,'package.json'));
  if(pkg.botCoreApi!==API_VERSION) throw new Error('Core application API is incompatible; migration required');
  await access(join(source,'src','runtime.mjs'));
  return source;
}
export async function stageCore(home,source,{fixed=false}={}) {
  source=await realpath(resolve(source));
  const pkg=await readJSON(join(source,'package.json'));
  if(pkg.botCoreApi!==API_VERSION) throw new Error('Core application API is incompatible; migration required');
  await access(join(source,'src','runtime.mjs'));
  // The operator supplies an installed, trusted core release. Modules cannot call this path.
  if(fixed)return stage(home,source);
  const snapshot=await coreSnapshot(home,source,{copyDependencies:true});
  try{return await stage(home,snapshot);}catch(error){await rm(snapshot,{recursive:true,force:true});throw error;}
}
export async function beginTrial(home,store) {
  const meta=await readJSON(metadataPath(home));
  if(!meta.pending) return null;
  const before=await readJSON(join(home,'revisions',meta.active,'config.json'));
  const after=await readJSON(join(home,'revisions',meta.pending,'config.json'));
  const namespaces=[...new Set([before,after].filter(config=>config.apiVersion===API_VERSION).map(config=>config.apps[config.controller].stateNamespace||`app:${config.controller}`))];
  await atomicJSON(join(home,'revisions',meta.pending,'before-state.json'),before.apiVersion===API_VERSION?{namespaces,state:store.snapshot().filter(row=>namespaces.includes(row.module))}:store.snapshot());
  const trial={from:meta.active,to:meta.pending,startedAt:Date.now(),kind:before.coreRoot===after.coreRoot?'controller':'core'};
  await atomicJSON(metadataPath(home),{...meta,active:meta.pending,pending:null,trial});
  store.record('update.trial',trial);
  return trial;
}
export async function promote(home,store) {
  const meta=await readJSON(metadataPath(home)); if(!meta.trial) return;
  await atomicJSON(metadataPath(home),{...meta,previous:meta.trial.from,trial:null});
  store.record('update.promoted',{revision:meta.active,previous:meta.trial.from});
}
export async function assertCompatibleRecovery(home) {
  const meta=await readJSON(metadataPath(home));
  const target=meta.trial?.from||meta.previous;
  if(!target)throw new Error('No previous working version is available');
  const config=await readJSON(join(home,'revisions',target,'config.json'));
  if(config.apiVersion!==API_VERSION)throw new Error('Previous revision uses a different application API; the operator must restore its retained executable release. The current conversation and state are unchanged.');
}
export async function rollback(home,existingStore) {
  await assertCompatibleRecovery(home);
  const store=existingStore||new Store(home);
  try {
    const meta=await readJSON(metadataPath(home));
    const target=meta.trial?.from||meta.previous;
    if(!target) throw new Error('No previous working version is available');
    const snapshot=await readJSON(join(home,'revisions',meta.active,'before-state.json'));
    // Older revisions kept whole-store snapshots. Restore only controller state,
    // even when recovering one of those revisions.
    const from=await readJSON(join(home,'revisions',meta.active,'config.json'));
    const to=await readJSON(join(home,'revisions',target,'config.json'));
    const namespaces=Array.isArray(snapshot)?[...new Set([from,to].map(config=>config.apps[config.controller].stateNamespace||`app:${config.controller}`))]:snapshot.namespaces;
    store.restore(Array.isArray(snapshot)?snapshot:snapshot.state,namespaces);
    const next={active:target,previous:null,pending:null,trial:null};
    // Preserve the previous release's own rollback chain, when it has one.
    const targetConfig=await readJSON(join(home,'revisions',target,'config.json'));
    await atomicJSON(metadataPath(home),next);
    const editable=await readJSON(join(home,'drafts','config.json'));
    // Controller recovery must not uninstall independently edited CLI apps.
    await atomicJSON(join(home,'drafts','config.json'),{...targetConfig,apps:{...editable.apps,[targetConfig.controller]:targetConfig.apps[targetConfig.controller]}});
    store.record('update.rolled_back',{from:meta.active,to:target});
    return target;
  } finally { if(!existingStore) store.close(); }
}
