import { spawn } from 'node:child_process';

// Background stimuli belong to the controller's applications, not the core.
export function startServices(apps, { restartMs = 3000, launch = spawn } = {}) {
  let stopped = false;
  const children = new Set(), timers = new Set();
  function start(app) {
    if (stopped) return;
    const child = launch('bot-app', [app, 'watch'], { env: process.env, stdio: ['ignore', 'ignore', 'pipe'] });
    children.add(child);
    child.stderr.on('data', chunk => process.stderr.write(chunk.subarray(0, 8000)));
    let settled = false;
    function ended() {
      if (settled) return; settled = true; children.delete(child);
      if (stopped) return;
      const timer = setTimeout(() => { timers.delete(timer); start(app); }, restartMs);
      timers.add(timer);
    }
    child.on('error', () => { console.error(`Background app ${app} could not start; retrying.`); ended(); });
    child.on('exit', ended);
  }
  for (const app of new Set(apps)) start(app);
  return () => { stopped = true; for (const timer of timers) clearTimeout(timer); for (const child of children) child.kill('SIGTERM'); };
}
