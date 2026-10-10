#!/usr/bin/env node
import { readFile, readdir, unlink } from 'node:fs/promises';
import { execFileSync } from 'node:child_process';
import { Host, atomic } from './host.mjs';
import { randomUUID } from 'node:crypto';

const [action = 'help', ...args] = process.argv.slice(2);
if (action === 'help') {
  console.log(`bot-next status | pause | resume | kill | send FILE | rollback | delegate FILE
Host commands require sudo. send reads a human-written message from FILE (or -).
Agent scripts wake the harness by atomically writing ~/inbox/ files.
delegate queues a fresh-context brief and returns immediately. Results wake the
primary inbox after its current run ends. All runs share the host daily budget.`);
} else if (action === 'delegate') {
  if (args.length !== 1 || !process.env.HOME) throw new Error('Usage: bot-next delegate FILE');
  const text = await readFile(args[0], 'utf8');
  if (!text.trim() || Buffer.byteLength(text) > 65536) throw new Error('Brief must contain 1..65536 bytes');
  const id = `${Date.now()}-${randomUUID()}`;
  await atomic(`${process.env.HOME}/jobs/${id}`, text);
  console.log(JSON.stringify({ job: id, state: 'queued', result: 'Delivered to the primary inbox; do not poll.' }));
} else {
  if (process.getuid?.() !== 0) throw new Error('Host controls require sudo; agent work goes in ~/inbox.');
  const config = JSON.parse(await readFile('/etc/bot-next/config.json', 'utf8'));
  const host = new Host(config);
  const systemctl = (...argv) => execFileSync('/usr/bin/systemctl', argv, { stdio: 'inherit' });
  if (action === 'status') {
    const flags = await readdir(config.state);
    console.log(JSON.stringify({ state: await host.load(), paused: flags.includes('PAUSE'), killed: flags.includes('KILL'),
      alert: flags.includes('alert.json') ? JSON.parse(await readFile(`${config.state}/alert.json`, 'utf8')) : null,
      home: config.home, journal: `${config.state}/journal` }, null, 2));
    systemctl('is-active', 'bot-next.service');
  } else if (action === 'pause') await atomic(`${config.state}/PAUSE`, 'Operator pause\n');
  else if (action === 'kill') {
    await atomic(`${config.state}/KILL`, 'Operator kill\n');
    systemctl('stop', 'bot-next.service');
  } else if (action === 'resume') {
    for (const flag of ['PAUSE', 'KILL']) await unlink(`${config.state}/${flag}`).catch(error => { if (error.code !== 'ENOENT') throw error; });
    systemctl('start', 'bot-next.service');
  } else if (action === 'send') {
    if (args.length !== 1) throw new Error('Usage: sudo bot-next send FILE|-');
    let text;
    if (args[0] === '-') { const chunks = []; for await (const chunk of process.stdin) chunks.push(chunk); text = Buffer.concat(chunks).toString('utf8'); }
    else text = await readFile(args[0], 'utf8');
    if (!text.trim() || Buffer.byteLength(text) > config.inboxBytes - 1024) throw new Error('Empty or oversized owner message');
    console.log(await host.enqueue('owner', text));
  } else if (action === 'rollback') {
    const { rollback } = await import('./install.mjs'); await rollback();
  } else throw new Error('Unknown command; run bot-next help');
}
