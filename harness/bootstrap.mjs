// Runs as the agent account during managed updates, never as privileged file I/O.
import { lstat, open } from 'node:fs/promises';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { atomic, safeText } from './host.mjs';
const present = path => lstat(path).then(() => true, error => { if (error.code === 'ENOENT') return false; throw error; });

export async function upgradeBootstrap(home, previousReadme, newReadme, newTask) {
  if (!await present(`${home}/TASK.md`)) {
    const text = await safeText(await present(`${home}/task.md`) ? `${home}/task.md` : newTask, 131072);
    const fd = await open(`${home}/TASK.md`, 'wx', 0o600);
    try { await fd.writeFile(text); await fd.sync(); } finally { await fd.close(); }
  }
  // Adopt an updated seed only if the agent has not edited its previous copy.
  if (await safeText(`${home}/README.md`, 131072) === await safeText(previousReadme, 131072)) {
    await atomic(`${home}/README.md`, await safeText(newReadme, 131072));
  }
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await upgradeBootstrap(...process.argv.slice(2));
