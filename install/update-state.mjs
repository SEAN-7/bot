// Trusted helper: invoked by the root installer as the dedicated bot account.
import { migrateApps, markCandidate, restoreApps } from './migrate-apps.mjs';
import { migrateLiveApps, markLiveCandidate, restoreLiveApps } from './live-apps.mjs';
import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
const [operation, home, core, candidate] = process.argv.slice(2);
if (!['stage', 'restore', 'settle-core'].includes(operation) || !home || !core) throw new Error('Invalid update helper arguments');
const releases = await import(pathToFileURL(join(core, 'src/releases.mjs')).href);
const meta = JSON.parse(await readFile(join(home, 'control/releases.json'), 'utf8'));
if (operation === 'stage') {
  if (meta.pending || meta.trial) throw new Error('An agent update is pending or on trial; settle it before updating the installed runtime');
  const migrated = await migrateApps(home, core);
  let liveMigrated=false, revision;
  try { liveMigrated=await migrateLiveApps(home,core);revision = await releases.stageCore(home, core, { fixed: true }); if(migrated)await markCandidate(home,revision);if(liveMigrated)await markLiveCandidate(home,revision); }
  catch(error) { if(liveMigrated)await restoreLiveApps(home);if(migrated)await restoreApps(home); throw error; }
  process.stdout.write(JSON.stringify({ revision }) + '\n');
} else if (operation === 'settle-core') {
  if (!candidate || meta.active !== candidate || meta.pending || meta.trial?.to !== candidate) throw new Error('Previous core trial changed; refusing to settle it');
  const before = JSON.parse(await readFile(join(home, 'revisions', meta.trial.from, 'config.json'), 'utf8'));
  const active = JSON.parse(await readFile(join(home, 'revisions', candidate, 'config.json'), 'utf8'));
  if (before.coreRoot === active.coreRoot) throw new Error('Cannot settle a bot-managed module trial through the operator installer');
  const { Store } = await import(pathToFileURL(join(core, 'src/store.mjs')).href);
  const store = new Store(home);
  try { await releases.promote(home, store); } finally { store.close(); }
  const draftPath = join(home, 'drafts', 'config.json');
  const draft = JSON.parse(await readFile(draftPath, 'utf8'));
  await releases.atomicJSON(draftPath, { ...draft, coreRoot: active.coreRoot });
  process.stdout.write(JSON.stringify({ settled: candidate }) + '\n');
} else {
  if (!candidate) throw new Error('Candidate revision is required');
  if (meta.trial?.to === candidate || (meta.active === candidate && meta.previous)) {
    const revision = await releases.rollback(home);
    await restoreLiveApps(home, candidate);
    await restoreApps(home, candidate);
    process.stdout.write(JSON.stringify({ restored: revision }) + '\n');
  } else if (meta.pending === candidate) {
    await releases.atomicJSON(releases.metadataPath(home), { ...meta, pending: null });
    await restoreLiveApps(home, candidate);
    await restoreApps(home, candidate);
    process.stdout.write(JSON.stringify({ cleared: candidate }) + '\n');
  } else {
    await restoreLiveApps(home, candidate);
    await restoreApps(home, candidate);
    process.stdout.write(JSON.stringify({ unchanged: true }) + '\n');
  }
}
