// The dedicated bot account makes persistent cores independent of installed releases.
import { readdir, lstat, cp, rename, rm, realpath } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { randomUUID } from 'node:crypto';
export async function materializeState(home) {
  const cores = join(home, 'cores');
  const entries = await readdir(cores, { withFileTypes: true }).catch(error => { if (error.code === 'ENOENT') return []; throw error; });
  for (const entry of entries) {
    if (!entry.isDirectory()) continue;
    const dependencies = join(cores, entry.name, 'node_modules');
    if (!(await lstat(dependencies)).isSymbolicLink()) continue;
    const temporary = join(cores, entry.name, `.dependencies-${randomUUID()}`);
    const backup = `${temporary}.old`;
    try {
      await cp(dependencies, temporary, { recursive: true, dereference: true });
      await rename(dependencies, backup);
      try { await rename(temporary, dependencies); }
      catch (error) { await rename(backup, dependencies); throw error; }
      await rm(backup);
    } finally { await rm(temporary, { recursive: true, force: true }); }
  }
}
if (process.argv[1] && (await realpath(process.argv[1]).catch(() => null)) === fileURLToPath(import.meta.url)) {
  await materializeState(process.argv[2]);
}
