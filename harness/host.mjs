import { constants } from 'node:fs';
import { open, mkdir, readFile, readdir, rename, unlink, chmod, chown, stat, lstat } from 'node:fs/promises';
import { join } from 'node:path';
import { randomUUID } from 'node:crypto';
import { spawn, execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { setTimeout as delay } from 'node:timers/promises';
const exec = promisify(execFile);
export const exists = path => stat(path).then(() => true, error => { if (error.code === 'ENOENT') return false; throw error; });
async function syncDirectory(path) {
  const fd = await open(path, 'r');
  try { await fd.sync(); } finally { await fd.close(); }
}

export async function atomic(path, value, mode = 0o600) {
  const temporary = `${path}.${randomUUID()}.tmp`;
  const fd = await open(temporary, 'wx', mode);
  try { await fd.writeFile(typeof value === 'string' ? value : JSON.stringify(value) + '\n'); await fd.sync(); }
  finally { await fd.close(); }
  await rename(temporary, path);
  await syncDirectory(join(path, '..'));
}
export async function safeText(path, maxBytes) {
  const fd = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
  try {
    const info = await fd.stat();
    if (!info.isFile() || info.size > maxBytes) throw new Error('Expected a bounded regular text file');
    const buffer = Buffer.alloc(maxBytes + 1);
    const { bytesRead } = await fd.read(buffer, 0, buffer.length, 0);
    if (bytesRead > maxBytes) throw new Error('File exceeds size limit');
    return buffer.subarray(0, bytesRead).toString('utf8');
  } finally { await fd.close(); }
}

export function validateConfig(c) {
  c = { readmeBytes: 32768, taskBytes: 32768, ...c };
  for (const key of ['home', 'state']) if (!/^\/[a-zA-Z0-9_./-]+$/.test(c[key]) || c[key].split('/').includes('..')) throw new Error(`Invalid ${key}`);
  if (c.home === c.state || c.state.startsWith(c.home + '/')) throw new Error('Host state must be outside the agent home');
  if (!/^[a-z_][a-z0-9_-]*$/.test(c.user)) throw new Error('Invalid agent user');
  for (const key of ['maxSteps', 'maxRunUsd', 'maxDayUsd', 'maxOutputTokens', 'clipBytes', 'maxOutputBytes', 'inboxBytes', 'readmeBytes', 'taskBytes']) {
    if (!Number.isFinite(c[key]) || c[key] <= 0) throw new Error(`Invalid ${key}`);
  }
  if (c.maxSteps > 10000 || c.clipBytes > 65536 || c.inboxBytes > 1048576 || c.maxOutputBytes > 1073741824) throw new Error('Host limits exceed supported bounds');
  if (c.readmeBytes > 131072 || c.taskBytes > 131072) throw new Error('Bootstrap file cap exceeds 128 KiB');
  if (!c.provider || !c.model) throw new Error('Host provider and model required');
  return c;
}

export class Host {
  constructor(config) {
    this.config = config; this.root = config.state; this.home = config.home;
    this.controller = new AbortController(); this.signal = this.controller.signal;
  }
  async init() {
    for (const name of ['incoming', 'claimed', 'delegated', 'rejected', 'snapshots']) await mkdir(join(this.root, name), { recursive: true, mode: 0o700 });
    await mkdir(join(this.root, 'journal'), { recursive: true, mode: 0o755 });
    await mkdir(join(this.root, 'journal', 'output'), { recursive: true, mode: 0o755 });
    for (const path of [join(this.root, 'journal'), join(this.root, 'journal', 'output')]) {
      await chmod(path, this.config.gid === undefined ? 0o755 : 0o750);
      if (this.config.gid !== undefined) await chown(path, 0, this.config.gid);
    }
  }
  load() { return readFile(join(this.root, 'state.json'), 'utf8').then(JSON.parse, error => { if (error.code === 'ENOENT') return {}; throw error; }); }
  save(state) { return atomic(join(this.root, 'state.json'), state); }
  async event(type, data) {
    const path = join(this.root, 'journal', `${new Date().toISOString().slice(0, 10)}.jsonl`);
    const fd = await open(path, 'a', 0o644);
    try {
      await fd.chmod(this.config.gid === undefined ? 0o644 : 0o640);
      if (this.config.gid !== undefined) await fd.chown(0, this.config.gid);
      await fd.writeFile(JSON.stringify({ time: new Date().toISOString(), type, ...data }) + '\n'); await fd.sync();
    }
    finally { await fd.close(); }
    await syncDirectory(join(this.root, 'journal'));
    this.lastJournalPath = path;
  }
  journalPath() { return this.lastJournalPath || join(this.root, 'journal', `${new Date().toISOString().slice(0, 10)}.jsonl`); }
  readme() { return safeText(join(this.home, 'README.md'), this.config.readmeBytes ?? 32768); }
  task() { return safeText(join(this.home, 'TASK.md'), this.config.taskBytes ?? 32768); }
  resetRequested() { return lstat(join(this.home, '.reset')).then(() => true, error => { if (error.code === 'ENOENT') return false; throw error; }); }
  async clearReset() { await unlink(join(this.home, '.reset')).catch(error => { if (error.code !== 'ENOENT') throw error; }); await syncDirectory(this.home); }
  async enqueue(source, text) {
    const id = `${Date.now()}-${randomUUID()}.json`;
    await atomic(join(this.root, 'incoming', id), { source, text });
    return id;
  }
  async pending() {
    if ((await readdir(join(this.root, 'claimed'))).length || (await readdir(join(this.root, 'incoming'))).some(x => !x.endsWith('.tmp'))) return true;
    const dir = await this.inbox();
    try { return (await readdir(dir.path)).some(x => !x.startsWith('.') && !x.endsWith('.tmp')); }
    finally { await dir.fd.close(); }
  }
  async inbox(name = 'inbox') {
    // Pin the agent-controlled directory before following any entry inside it.
    // /proc/self/fd avoids directory replacement/symlink races while privileged.
    const fd = await open(join(this.home, name), constants.O_RDONLY | constants.O_DIRECTORY | constants.O_NOFOLLOW);
    return { fd, path: process.platform === 'linux' ? `/proc/self/fd/${fd.fd}` : join(this.home, name) };
  }
  async claimJob() {
    // Claim only when primary work is idle. Subagents share the same serialized
    // host, model credentials and daily budget, with a fresh README context.
    const dir = await this.inbox('jobs');
    try {
      let id = (await readdir(join(this.root, 'delegated'))).sort()[0];
      if (!id) {
        const name = (await readdir(dir.path)).sort().find(x => !x.startsWith('.') && !x.endsWith('.tmp'));
        if (!name) return null;
        id = randomUUID();
        await rename(join(dir.path, name), join(this.root, 'delegated', id));
      }
      return { id, text: await safeText(join(this.root, 'delegated', id), this.config.inboxBytes) };
    } finally { await dir.fd.close(); }
  }
  async ackJob(id) { await unlink(join(this.root, 'delegated', id)).catch(error => { if (error.code !== 'ENOENT') throw error; }); }
  async take() {
    // Claimed files survive a crash. Events have stable IDs; replay is explicitly at-least-once.
    const dir = await this.inbox();
    try {
      for (const name of (await readdir(dir.path)).sort().filter(x => !x.startsWith('.') && !x.endsWith('.tmp')).slice(0, 16)) {
        const id = `agent-${randomUUID()}`;
        try { await rename(join(dir.path, name), join(this.root, 'claimed', id)); }
        catch (error) { if (error.code !== 'ENOENT') throw error; }
      }
    } finally { await dir.fd.close(); }
    for (const name of (await readdir(join(this.root, 'incoming'))).sort().filter(x => !x.endsWith('.tmp')).slice(0, 16)) {
      await rename(join(this.root, 'incoming', name), join(this.root, 'claimed', name));
    }
    const items = [];
    for (const id of (await readdir(join(this.root, 'claimed'))).sort().slice(0, 32)) {
      try {
        const text = await safeText(join(this.root, 'claimed', id), this.config.inboxBytes);
        const item = id.startsWith('agent-') ? { source: 'agent file (untrusted data)', text } : JSON.parse(text);
        items.push({ ...item, id });
      } catch (error) {
        await rename(join(this.root, 'claimed', id), join(this.root, 'rejected', id));
        await this.alert({ reason: 'Rejected inbox entry', id, error: error.message });
      }
    }
    return items;
  }
  ack(id) { return unlink(join(this.root, 'claimed', id)); }
  async gate() {
    for (;;) {
      this.signal.throwIfAborted();
      if (await exists(join(this.root, 'KILL'))) { this.controller.abort(new Error('Host kill flag')); this.signal.throwIfAborted(); }
      if (!await exists(join(this.root, 'PAUSE'))) return;
      await delay(200, undefined, { signal: this.signal });
    }
  }
  async alert(data) {
    await this.event('alert', data);
    await atomic(join(this.root, 'alert.json'), { time: new Date().toISOString(), ...data });
    console.error('bot-next alert:', data.reason);
    if (this.config.notifyCommand) {
      // Host-owned optional delivery hook; no agent input is evaluated as code.
      try { await exec(this.config.notifyCommand[0], [...this.config.notifyCommand.slice(1), join(this.root, 'alert.json')], { timeout: 10000, env: { PATH: '/usr/bin:/bin' } }); }
      catch { console.error('bot-next: owner notification hook failed; alert retained'); }
    }
  }
  async snapshot() {
    this.signal.throwIfAborted();
    const target = join(this.root, 'snapshots', `${Date.now()}-${randomUUID()}.tar`);
    if (this.config.snapshotCommand) {
      await exec(this.config.snapshotCommand[0], [...this.config.snapshotCommand.slice(1), this.home, target], { timeout: 120000, env: { PATH: '/usr/sbin:/usr/bin:/bin' } });
    } else {
      // Read as the agent: a symlink or race cannot cause root-file disclosure.
      // Tar does not dereference symlinks. Archives live outside the agent's reach.
      const out = await open(target + '.tmp', 'wx', 0o600);
      try {
        await new Promise((resolve, reject) => {
          const unit = `bot-next-snapshot-${randomUUID()}`;
          const child = spawn('/usr/bin/systemd-run', ['--quiet', '--wait', '--pipe', '--collect', `--unit=${unit}`, `--uid=${this.config.user}`, `--gid=${this.config.user}`,
            '--property=RuntimeMaxSec=120', '--property=TimeoutStopSec=2', '--property=KillMode=control-group',
            ...(this.config.service ? [`--property=BindsTo=${this.config.service}`, `--property=After=${this.config.service}`] : []),
            '--', '/usr/bin/tar', '--one-file-system', '--exclude=./journal', '--exclude=./inbox', '--exclude=./.reset', '-C', this.home, '-cf', '-', '.'], { stdio: ['ignore', out.fd, 'pipe'], env: { PATH: '/usr/bin:/bin' } });
          let error = ''; child.stderr.on('data', chunk => { error = (error + chunk).slice(-2000); });
          child.once('error', reject);
          child.once('close', code => { code === 0 ? resolve() : reject(new Error(`Snapshot failed (${code}): ${error}`)); });
        });
        await out.sync();
      } finally { await out.close(); }
      await rename(target + '.tmp', target);
      await syncDirectory(join(this.root, 'snapshots'));
    }
    return target;
  }
  async shell(command) {
    this.signal.throwIfAborted();
    const id = randomUUID();
    const output = join(this.root, 'journal', 'output', `${id}.txt`);
    const fd = await open(output, 'wx', 0o644);
    await fd.chmod(this.config.gid === undefined ? 0o644 : 0o640);
    if (this.config.gid !== undefined) await fd.chown(0, this.config.gid);
    const unit = `bot-next-sh-${id}`;
    const args = ['--quiet', '--wait', '--pipe', '--collect', `--unit=${unit}`, `--uid=${this.config.user}`, `--gid=${this.config.user}`,
      `--property=RuntimeMaxSec=${Math.min(120, Math.max(1, this.config.shellSeconds || 120))}`, '--property=TimeoutStopSec=2', '--property=KillMode=control-group', '--property=UMask=0077',
      '--property=MemoryMax=512M', '--property=TasksMax=128',
      ...(this.config.service ? [`--property=BindsTo=${this.config.service}`, `--property=After=${this.config.service}`] : []),
      `--working-directory=${this.home}`, `--setenv=HOME=${this.home}`, `--setenv=USER=${this.config.user}`, '--setenv=LANG=C.UTF-8',
      ...(this.config.uid ? [`--setenv=XDG_RUNTIME_DIR=/run/user/${this.config.uid}`, `--setenv=DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/${this.config.uid}/bus`] : []),
      `--setenv=PATH=${this.home}/bin:/opt/bot-next/current/runtime/node/bin:/usr/local/bin:/usr/bin:/bin`,
      '--', '/bin/sh', '-c', command];
    let bytes = 0, clipped = Buffer.alloc(0), limited = false, interrupted = false;
    let write = Promise.resolve(), writeError;
    try {
      return await new Promise((resolve, reject) => {
        const child = spawn('/usr/bin/systemd-run', args, { stdio: ['ignore', 'pipe', 'pipe'], env: { PATH: '/usr/sbin:/usr/bin:/bin' } });
        const stop = () => { if (interrupted) return; interrupted = true; exec('/usr/bin/systemctl', ['stop', unit], { timeout: 5000 }).catch(() => {}); };
        const timeout = setTimeout(stop, 122000);
        this.signal.addEventListener('abort', stop, { once: true });
        if (this.signal.aborted) stop();
        const retain = stream => stream.on('data', chunk => {
          stream.pause();
          bytes += chunk.length;
          if (clipped.length < this.config.clipBytes) clipped = Buffer.concat([clipped, chunk.subarray(0, this.config.clipBytes - clipped.length)]);
          if (bytes > this.config.maxOutputBytes) { limited = true; stop(); }
          else write = write.then(() => fd.writeFile(chunk)).catch(error => { writeError = error; stop(); });
          write.finally(() => stream.resume());
        });
        retain(child.stdout); retain(child.stderr);
        child.once('error', reject);
        child.once('close', async code => {
          clearTimeout(timeout); this.signal.removeEventListener('abort', stop);
          await write;
          if (writeError) { reject(writeError); return; }
          resolve({ code, output: clipped.toString('utf8'), fullOutput: output, bytes, clipped: bytes > clipped.length,
            ...(limited ? { error: 'Output disk limit reached; command stopped and output file is incomplete.' } : {}),
            ...(interrupted || code !== 0 ? { warning: 'Command failed or was interrupted; inspect effects before retrying.' } : {}) });
        });
      });
    } finally { await fd.sync(); await fd.close(); await syncDirectory(join(this.root, 'journal', 'output')); }
  }
}
