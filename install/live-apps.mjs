// Operator migration to ordinary editable CLI apps. Preserve custom programs,
// journal, credentials, and an exact backup for executable rollback.
import { readFile, writeFile, mkdir, cp, rm, readdir, lstat, symlink, rename } from 'node:fs/promises';
import { join } from 'node:path';
import { randomUUID } from 'node:crypto';

const marker = home => join(home, 'control', 'live-apps-migration.json');
const read = async path => JSON.parse(await readFile(path, 'utf8'));
const write = (path, value) => writeFile(path, JSON.stringify(value, null, 2) + '\n', { mode: 0o600 });
async function bytes(path) { try { return await readFile(path); } catch(error) { if(error.code==='ENOENT')return null;throw error; } }

export async function migrateLiveApps(home, core) {
  const config = await read(join(home, 'drafts', 'config.json'));
  const apps = join(home, 'drafts', 'apps');
  const renameModel = !!config.apps?.['pi-model'] || !!await bytes(join(apps, 'pi-model', 'app.json'));
  if(config.appUpdates==='direct'&&!renameModel)return false;
  if(renameModel&&(config.apps?.['model-provider']||await bytes(join(apps, 'model-provider', 'app.json')))) {
    throw new Error('Both pi-model and model-provider exist; reconcile the model apps before updating. Drafts are unchanged.');
  }
  const backup = join(home, 'control', `live-apps-backup-${randomUUID()}`);
  await mkdir(backup, { mode: 0o700 });
  await cp(join(home, 'drafts'), join(backup, 'drafts'), { recursive: true, verbatimSymlinks: true });
  await cp(join(home, 'workspace', 'BOT-API.md'), join(backup, 'BOT-API.md'));
  await write(marker(home), { backup, candidate: null });
  try {
    if(renameModel) {
      await rename(join(apps, 'pi-model'), join(apps, 'model-provider'));
      const settings=config.apps['pi-model'];
      if(settings)config.apps['model-provider']={...settings,stateNamespace:settings.stateNamespace??'app:pi-model'};
      delete config.apps['pi-model'];
      for(const settings of Object.values(config.apps))if(settings.modelApp==='pi-model')settings.modelApp='model-provider';
    }
    async function refresh(relative='') {
      for(const entry of await readdir(join(core, 'apps', relative), { withFileTypes: true })) {
        const name=join(relative, entry.name);
        if(entry.isDirectory()) { await refresh(name);continue; }
        if(!entry.isFile())continue;
        const target=join(home, 'drafts', 'apps', name), existing=await bytes(target);
        const previousName=renameModel?name.replace(/^model-provider\//,'pi-model/'):name;
        const previous=await bytes(join(config.coreRoot, 'apps', previousName));
        // Only update unchanged bundled files; bot-authored code remains intact.
        if(!existing||(previous&&existing.equals(previous))) {
          await mkdir(join(target, '..'), { recursive: true });
          await cp(join(core, 'apps', name), target);
        }
      }
    }
    if(config.appUpdates==='direct')await refresh('model-provider');
    else await refresh();
    if(renameModel) {
      const manifestPath=join(apps,'model-provider','app.json'),manifest=await read(manifestPath);
      manifest.id='model-provider';
      if(manifest.description==='pi-model behavior application')manifest.description='Model access for the conversation controller';
      await write(manifestPath,manifest);
      const guide=join(apps,'model-provider','SKILL.md');
      await writeFile(guide,(await readFile(guide,'utf8')).replaceAll('pi-model','model-provider'),{mode:0o600});
    }
    const dependencies=join(home, 'drafts', 'node_modules');
    try { await lstat(dependencies); }
    catch(error) { if(error.code!=='ENOENT')throw error;await symlink(join(core, 'node_modules'), dependencies, 'dir'); }
    await write(join(home, 'drafts', 'config.json'), { ...config, appUpdates: 'direct' });
    await cp(join(core, 'docs', 'contract.md'), join(home, 'workspace', 'BOT-API.md'));
    return true;
  } catch(error) { await restoreLiveApps(home);throw error; }
}

export async function markLiveCandidate(home, candidate) {
  const record=await read(marker(home));await write(marker(home), { ...record, candidate });
}
export async function restoreLiveApps(home, candidate) {
  let record;try { record=await read(marker(home)); } catch(error) { if(error.code==='ENOENT')return;throw error; }
  if(candidate&&record.candidate!==candidate)return;
  await rm(join(home, 'drafts'), { recursive: true });
  await cp(join(record.backup, 'drafts'), join(home, 'drafts'), { recursive: true, verbatimSymlinks: true });
  await cp(join(record.backup, 'BOT-API.md'), join(home, 'workspace', 'BOT-API.md'));
  await rm(marker(home));
}
