#!/usr/bin/env node
import { readFile, realpath } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { Host, validateConfig, exists, atomic } from './host.mjs';
import { Harness } from './loop.mjs';
import { createModel } from './model.mjs';

if (process.getuid?.() !== 0) throw new Error('The managed harness requires its host service. Submit work through the inbox.');
const config = validateConfig(JSON.parse(await readFile(process.argv[2] || '/etc/bot-next/config.json', 'utf8')));
const host = new Host(config);
await host.init();
const model = createModel(config);
if (process.argv.includes('--check')) {
  await host.readme();
  await host.task();
  await host.load();
  console.log(JSON.stringify({ ok: true, contextWindow: model.contextWindow, maxTokens: model.maxTokens, reservation: model.reservation }));
  process.exit(0);
}
for (const sig of ['SIGINT', 'SIGTERM']) process.on(sig, () => host.controller.abort(new Error(sig)));
const killMonitor = setInterval(async () => { if (await exists(`${config.state}/KILL`)) host.controller.abort(new Error('Host kill flag')); }, 200);
const loop = new Harness(host, model, config);
try {
  await loop.boot();
  await atomic(`${config.state}/ready.json`, { pid: process.pid, entry: await realpath(fileURLToPath(import.meta.url)) });
  for (;;) {
    await host.gate();
    await loop.heartbeat();
    if (!await loop.run()) {
      const job = await host.claimJob();
      if (job) await loop.run(job);
      else await delay(500, undefined, { signal: host.signal });
    }
  }
} catch (error) {
  if (!host.signal.aborted) {
    await host.alert({ reason: 'Harness stopped', error: error.message });
    // Fail closed on provider/snapshot/config errors; operator resume acknowledges it.
    await atomic(`${config.state}/PAUSE`, error.message + '\n');
  }
  process.exitCode = host.signal.aborted ? 0 : 1;
} finally { clearInterval(killMonitor); }
