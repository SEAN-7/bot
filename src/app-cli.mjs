import { readdir } from 'node:fs/promises';
import { join } from 'node:path';
import { spawn } from 'node:child_process';
import { appManifest, commandAt, APP_ID } from './app-package.mjs';

// This launcher only executes argv. It never imports application code.
export async function main(argv = process.argv.slice(2), env = process.env) {
  if (!env.BOT_APPS) throw new Error('BOT_APPS is missing. Run bot-app from the bot shell.');
  const [id, ...args] = argv;
  if (!id || id === '--help') {
    const packages = [];
    for (const item of await readdir(env.BOT_APPS, { withFileTypes: true })) if (item.isDirectory() && APP_ID.test(item.name)) {
      const folder = join(env.BOT_APPS, item.name);
      try {
        const manifest = await appManifest(folder);
        packages.push(`${manifest.id}: ${manifest.description || ''}\n  ${join(folder, 'SKILL.md')}`);
      } catch(error) { packages.push(`${item.name}: unavailable (${error.message})`); }
    }
    console.log('Usage: bot-app APP [ARGS...]\nEach app defines its CLI. Read its SKILL.md.\n' + packages.join('\n')); return;
  }
  if (!APP_ID.test(id)) throw new Error('Invalid app ID');
  const folder = join(env.BOT_APPS, id), manifest = await appManifest(folder);
  if (manifest.id !== id) throw new Error('App directory/ID mismatch');
  const launch = commandAt(folder, manifest.command, env);
  const child = spawn(launch.command, [...launch.args, ...args], { cwd: folder, env, stdio: 'inherit' });
  const forward = signal => { try { child.kill(signal); } catch {} };
  const interrupt = () => forward('SIGINT'), terminate = () => forward('SIGTERM');
  process.on('SIGINT', interrupt); process.on('SIGTERM', terminate);
  try { process.exitCode = await new Promise((resolve, reject) => { child.once('error', reject); child.once('exit', (code, signal) => resolve(code ?? (signal === 'SIGINT' ? 130 : 1))); }); }
  finally { process.removeListener('SIGINT', interrupt); process.removeListener('SIGTERM', terminate); }
}
if (process.argv[1] === new URL(import.meta.url).pathname) main().catch(error => { console.error(`bot-app: ${error.message}`); process.exitCode = 1; });
