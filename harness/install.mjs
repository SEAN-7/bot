#!/usr/bin/env node
// Separate managed installation. Never changes the legacy bot service or home.
import { mkdir, readFile, writeFile, cp, chmod, chown, lstat, symlink, rename, unlink } from 'node:fs/promises';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { randomUUID } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { atomic, exists, validateConfig } from './host.mjs';
import { createModel } from './model.mjs';
const { validateBundle } = await import(await exists(new URL('../install/manage.mjs', import.meta.url)) ? '../install/manage.mjs' : '../../install/manage.mjs');
const exec = promisify(execFile);
const APP = '/opt/bot-next', HOME = '/var/lib/bot-next', STATE = '/var/lib/bot-next-host', ETC = '/etc/bot-next';
const MARKER = 'bot-file-harness-v1';
const json = async path => JSON.parse(await readFile(path, 'utf8'));
const run = (command, args) => exec(command, args, { timeout: 120000 });
async function protectedPath(path) {
  const info = await lstat(path);
  if (info.isSymbolicLink() || info.uid !== 0 || info.mode & 0o022) throw new Error(`Unprotected host path: ${path}`);
}
async function point(release) {
  const temp = `${APP}/.current-${randomUUID()}`;
  await symlink(release, temp); await rename(temp, `${APP}/current`);
}
async function health(release) {
  // Deliberately no model request or inbox traffic.
  await run(`${release}/runtime/node/bin/node`, [`${release}/app/harness/main.mjs`, `${ETC}/config.json`, '--check']);
  for (let attempt = 0; attempt < 50; attempt++) {
    try {
      await run('/usr/bin/systemctl', ['is-active', 'bot-next.service']);
      const ready = await json(`${STATE}/ready.json`);
      if (ready.entry === `${release}/app/harness/main.mjs`) { process.kill(ready.pid, 0); return; }
    } catch {}
    await delay(100);
  }
  throw new Error('Harness startup readiness was not confirmed');
}
export async function rollback() {
  await protectedPath(ETC); await protectedPath(`${ETC}/installation.json`);
  const record = await json(`${ETC}/installation.json`);
  if (record.manager !== MARKER || !record.previous?.startsWith(`${APP}/releases/`)) throw new Error('No managed previous release');
  await protectedPath(record.previous);
  await run('/usr/bin/systemctl', ['stop', 'bot-next.service']);
  await point(record.previous);
  await atomic('/etc/systemd/system/bot-next.service', await readFile(`${record.previous}/app/harness/bot-next.service`, 'utf8'), 0o644);
  await run('/usr/bin/systemctl', ['daemon-reload']);
  await run('/usr/bin/systemctl', ['start', 'bot-next.service']);
  await health(record.previous);
  await atomic(`${ETC}/installation.json`, { ...record, current: record.previous, previous: record.current });
}
export async function install(action, options) {
  if (process.platform !== 'linux' || process.getuid() !== 0) throw new Error('Requires root on Linux');
  if (!['install-next', 'update-next'].includes(action)) throw new Error('Expected install-next or update-next');
  const { root, manifest } = await validateBundle(options.bundle);
  for (const name of ['install.mjs', 'main.mjs', 'loop.mjs', 'host.mjs', 'model.mjs', 'cli.mjs', 'bootstrap.mjs', 'README.seed.md', 'TASK.seed.md', 'bot-next.service']) {
    if (!(await lstat(`${root}/app/harness/${name}`)).isFile()) throw new Error(`Missing harness file: ${name}`);
  }
  const fresh = action === 'install-next';
  let record;
  if (fresh) {
    for (const path of [APP, HOME, STATE, ETC, '/usr/local/bin/bot-next', '/etc/systemd/system/bot-next.service']) {
      const present = await lstat(path).then(() => true, error => { if (error.code === 'ENOENT') return false; throw error; });
      if (present) throw new Error(`Refusing existing path: ${path}`);
    }
    for (const db of ['passwd', 'group']) {
      let found = false; try { await run('/usr/bin/getent', [db, 'bot-next']); found = true; } catch (error) { if (error.code !== 2) throw error; }
      if (found) throw new Error('Refusing existing bot-next account/group');
    }
  } else {
    for (const path of [APP, STATE, ETC, `${ETC}/installation.json`]) await protectedPath(path);
    record = await json(`${ETC}/installation.json`);
    if (record.manager !== MARKER) throw new Error('Unrelated installation');
    if (Number((await run('/usr/bin/id', ['-u', 'bot-next'])).stdout.trim()) !== record.uid) throw new Error('Managed account UID changed');
  }
  await run('/usr/bin/systemd-run', ['--version']);
  await run('/usr/bin/flock', ['--version']);
  const provider = options.provider || 'anthropic', model = options.model || 'claude-haiku-5-5';
  let environment = '';
  if (fresh) {
    createModel({ provider, model });
    // Preflight credential import before allocating any managed path/account.
    if (options['credential-file']) {
      const key = { anthropic: 'ANTHROPIC_API_KEY', openai: 'OPENAI_API_KEY', google: 'GEMINI_API_KEY' }[provider];
      if (!key) throw new Error('Credential import supports anthropic/openai/google; configure other providers directly.');
      const line = (await readFile(options['credential-file'], 'utf8')).split('\n').find(line => line.startsWith(key + '='));
      if (!line) throw new Error('Selected provider credential is missing');
      environment = line + '\n';
    }
  }
  await mkdir(`${APP}/releases`, { recursive: true, mode: 0o755 });
  await mkdir(ETC, { recursive: true, mode: 0o700 });
  await mkdir(STATE, { recursive: true, mode: 0o755 });
  const release = `${APP}/releases/${manifest.version}-${randomUUID().slice(0, 8)}`;
  await cp(root, release, { recursive: true, dereference: false, verbatimSymlinks: true });
  await run('/usr/bin/chown', ['-R', 'root:root', release]);
  await run('/usr/bin/chmod', ['-R', 'a+rX,go-w', release]);
  if (fresh) {
    await run('/usr/sbin/useradd', ['--system', '--user-group', '--home-dir', HOME, '--create-home', '--shell', '/bin/bash', 'bot-next']);
    await chmod(HOME, 0o700);
    const { stdout } = await run('/usr/bin/id', ['-u', 'bot-next']);
    const uid = Number(stdout.trim());
    const gid = Number((await run('/usr/bin/id', ['-g', 'bot-next'])).stdout.trim());
    for (const name of ['inbox', 'jobs', 'bin', 'notes', 'archive']) {
      await mkdir(`${HOME}/${name}`, { mode: 0o700 }); await chown(`${HOME}/${name}`, uid, gid);
    }
    await writeFile(`${HOME}/README.md`, await readFile(`${release}/app/harness/README.seed.md`), { mode: 0o600 });
    await chown(`${HOME}/README.md`, uid, gid);
    await writeFile(`${HOME}/TASK.md`, await readFile(`${release}/app/harness/TASK.seed.md`), { mode: 0o600 });
    await chown(`${HOME}/TASK.md`, uid, gid);
    await mkdir(`${STATE}/journal`, { mode: 0o755 });
    await symlink(`${STATE}/journal`, `${HOME}/journal`); await chown(HOME, uid, gid);
    const config = validateConfig({ home: HOME, state: STATE, user: 'bot-next', uid, gid, service: 'bot-next.service',
      provider, model,
      maxSteps: 100, maxRunUsd: 2, maxDayUsd: 10,
      clipBytes: 16000, maxOutputBytes: 67108864, inboxBytes: 262144 });
    await atomic(`${ETC}/config.json`, config);
    await atomic(`${ETC}/environment`, environment);
    await atomic(`${STATE}/PAUSE`, 'Installed paused for bootstrap and owner handover.\n');
    record = { manager: MARKER, uid, gid, current: null, previous: null };
    // Persist ownership before activation so a failed health check can be repaired by update-next.
    await atomic(`${ETC}/installation.json`, record);
    await run('/usr/bin/loginctl', ['enable-linger', 'bot-next']);
  }
  const previous = record.current;
  await run('/usr/bin/systemctl', ['stop', 'bot-next.service']).catch(error => { if (!fresh) throw error; });
  try {
    if (previous) await run('/usr/sbin/runuser', ['-u', 'bot-next', '--', `${release}/runtime/node/bin/node`, `${release}/app/harness/bootstrap.mjs`,
      HOME, `${previous}/app/harness/README.seed.md`, `${release}/app/harness/README.seed.md`, `${release}/app/harness/TASK.seed.md`]);
    await run(`${release}/runtime/node/bin/node`, [`${release}/app/harness/main.mjs`, `${ETC}/config.json`, '--check']);
    await unlink(`${STATE}/ready.json`).catch(error => { if (error.code !== 'ENOENT') throw error; });
    await point(release);
    await atomic('/etc/systemd/system/bot-next.service', await readFile(`${release}/app/harness/bot-next.service`, 'utf8'), 0o644);
    await atomic('/usr/local/bin/bot-next', '#!/bin/sh\n# Managed by bot file harness\nexec /opt/bot-next/current/runtime/node/bin/node /opt/bot-next/current/app/harness/cli.mjs "$@"\n', 0o755);
    await run('/usr/bin/systemctl', ['daemon-reload']);
    await run('/usr/bin/systemctl', ['enable', '--now', 'bot-next.service']);
    await health(release);
    await atomic(`${ETC}/installation.json`, { ...record, current: release, previous });
  } catch (error) {
    if (previous) {
      await point(previous);
      await atomic('/etc/systemd/system/bot-next.service', await readFile(`${previous}/app/harness/bot-next.service`, 'utf8'), 0o644);
      await run('/usr/bin/systemctl', ['daemon-reload']);
      await run('/usr/bin/systemctl', ['restart', 'bot-next.service']);
    }
    throw error;
  }
  console.log(JSON.stringify({ installed: release, previous, paused: await exists(`${STATE}/PAUSE`), home: HOME }));
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [action, ...args] = process.argv.slice(2), options = {};
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--yes') continue;
    const key = args[i].slice(2);
    if (!['bundle', 'provider', 'model', 'credential-file'].includes(key) || !args[i + 1]) throw new Error(`Unknown or incomplete option: ${args[i]}`);
    options[key] = args[++i];
  }
  await install(action, options);
}
