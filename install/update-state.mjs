// Trusted helper: invoked by the root installer as the dedicated bot account.
import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
const [operation, home, core, candidate] = process.argv.slice(2);
if (!['stage', 'restore'].includes(operation) || !home || !core) throw new Error('Invalid update helper arguments');
const releases = await import(pathToFileURL(join(core, 'src/releases.mjs')).href);
const meta = JSON.parse(await readFile(join(home, 'control/releases.json'), 'utf8'));
if (operation === 'stage') {
  if (meta.pending || meta.trial) throw new Error('An agent update is pending or on trial; settle it before updating the installed runtime');
  const revision = await releases.stageCore(home, core);
  process.stdout.write(JSON.stringify({ revision }) + '\n');
} else {
  if (!candidate) throw new Error('Candidate revision is required');
  if (meta.trial?.to === candidate || (meta.active === candidate && meta.previous)) {
    const revision = await releases.rollback(home);
    process.stdout.write(JSON.stringify({ restored: revision }) + '\n');
  } else if (meta.pending === candidate) {
    await releases.atomicJSON(releases.metadataPath(home), { ...meta, pending: null });
    process.stdout.write(JSON.stringify({ cleared: candidate }) + '\n');
  } else {
    process.stdout.write(JSON.stringify({ unchanged: true }) + '\n');
  }
}
