import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, readFile, cp, realpath, stat, access, rm, symlink, lstat, readlink } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { createHash } from 'node:crypto';
import { materializeState } from '../install/materialize-state.mjs';
import { createManager, validateBundle, setupChoices, environmentText, parseEnvironment } from '../install/manage.mjs';

async function fixture(t) {
  const root = await realpath(await mkdtemp(join(tmpdir(), 'bot-installer-test-')));
  t.after(() => rm(root, { recursive: true, force: true }));
  const paths = { app: join(root, 'opt/bot'), data: join(root, 'var/lib/bot'), config: join(root, 'etc/bot'), service: join(root, 'systemd/bot.service'), launcher: join(root, 'bin/bot'), osRelease: join(root, 'os-release') };
  await writeFile(paths.osRelease, 'ID=debian\n');
  const bundle = join(root, 'bundle');
  for (const path of ['app/src', 'app/defaults', 'app/node_modules', 'runtime/node/bin', 'runtime/python', 'runtime/toad', 'bin', 'install/templates']) await mkdir(join(bundle, path), { recursive: true });
  await writeFile(join(bundle, 'bundle.json'), JSON.stringify({ format: 1, version: '0.2.0', platform: 'linux', arch: 'x64', repository: 'SEAN-7/bot' }));
  await writeFile(join(bundle, 'app/package.json'), JSON.stringify({ botCoreApi: 1 }));
  for (const path of ['app/src/cli.mjs', 'app/src/releases.mjs', 'runtime/node/bin/node', 'bin/toad', 'install/manage.mjs', 'install/update-state.mjs', 'install/materialize-state.mjs']) await writeFile(join(bundle, path), '// fixture\n');
  await cp(new URL('../install/templates/bot', import.meta.url), join(bundle, 'install/templates/bot'));
  await cp(new URL('../install/templates/bot.service', import.meta.url), join(bundle, 'install/templates/bot.service'));
  let account = false, checks = 0;
  const commands = [];
  const run = async (command, args, options = {}) => {
    commands.push({ command, args, options });
    if (command === 'getent' && args[0] === 'group') return { code: account ? 0 : 2, stdout: account ? 'bot:x:987:bot\n' : '' };
    if (command === 'id') return { code: 0, stdout: 'bot\n' };
    if (command === 'getent') return { code: account ? 0 : 2, stdout: account ? `bot:x:987:987::${paths.data}:/usr/sbin/nologin\n` : '' };
    if (command === 'useradd') account = true;
    if (command === 'runuser' && args.includes('setup')) {
      const control = join(paths.data, 'state/control');
      await mkdir(control, { recursive: true });
      await writeFile(join(control, 'releases.json'), JSON.stringify({ active: 'old', previous: null, pending: null, trial: null }));
    }
    return { code: 0, stdout: '' };
  };
  const make = (overrides = {}) => createManager({ paths, run, platform: 'linux', arch: 'x64', uid: 0, ownerUid: process.getuid(), env: { GH_TOKEN: 'admin-private-token', OPENAI_API_KEY: 'provider-secret' }, print: () => {}, choices: () => setupChoices({ yes: true, provider: 'openai', model: 'test-model' }, { env: { OPENAI_API_KEY: 'provider-secret' }, interactive: false }), stage: async () => ({ revision: 'candidate' }), health: async ({ candidate } = {}) => { checks++; return { state: 'idle', protected: true, revision: candidate || 'old', trial: candidate ? { to: candidate } : null }; }, ...overrides });
  return { root, paths, bundle, commands, make, checks: () => checks };
}

test('installer validates bundle architecture and refuses unrelated files/accounts', async t => {
  const f = await fixture(t);
  await assert.rejects(validateBundle(f.bundle, 'arm64'), /Unsupported bundle/);
  await mkdir(f.paths.app, { recursive: true });
  await assert.rejects(f.make().install({ bundle: f.bundle, yes: true }), /unrelated directory/);
  assert.equal(f.commands.some(command => command.command === 'apt-get'), false);
});

test('install provisions a dedicated service and never places credentials in arguments', async t => {
  const f = await fixture(t);
  const result = await f.make().install({ bundle: f.bundle, yes: true });
  assert.equal(await realpath(join(f.paths.app, 'current')), result.release);
  assert.equal((await stat(join(f.paths.config, 'environment'))).mode & 0o777, 0o600);
  assert.deepEqual(parseEnvironment(await readFile(join(f.paths.config, 'environment'), 'utf8')), { OPENAI_API_KEY: 'provider-secret' });
  const service = await readFile(f.paths.service, 'utf8');
  assert.match(service, /User=bot\nGroup=bot/);
  assert.match(service, /UMask=0077/);
  assert.match(service, /Restart=on-failure/);
  assert.match(service, /serve --home .*\/state/);
  assert.equal(JSON.stringify(f.commands.map(command => command.args)).includes('provider-secret'), false);
  const botCommands = f.commands.filter(command => command.command === 'runuser');
  assert.ok(botCommands.length);
  assert.ok(botCommands.every(command => command.options.env.GH_TOKEN === undefined));
  await assert.rejects(f.make().install({ bundle: f.bundle, yes: true }), /already installed/);
});

test('local update preserves state and reports unpromoted candidate probation', async t => {
  const f = await fixture(t);
  const manager = f.make();
  const initial = await manager.install({ bundle: f.bundle, yes: true });
  const marker = join(f.paths.data, 'state/conversation-preserved');
  await writeFile(marker, 'durable history');
  const result = await manager.update({ bundle: f.bundle });
  assert.notEqual(result.release, initial.release);
  assert.equal(result.probation, true);
  assert.equal(await readFile(marker, 'utf8'), 'durable history');
  assert.ok(f.commands.some(command => command.command === 'systemctl' && command.args.join(' ') === 'stop bot.service'));
  assert.equal(await realpath(join(f.paths.app, 'current')), result.release);
});

test('failed candidate health restores the old executable pointer and invokes exact state restore', async t => {
  const f = await fixture(t);
  const initial = await f.make().install({ bundle: f.bundle, yes: true });
  const failing = f.make({ health: async ({ candidate } = {}) => { if (candidate) throw new Error('candidate never ready'); return { state: 'idle' }; } });
  await assert.rejects(failing.update({ bundle: f.bundle }), /restored the previous executable pointer/);
  assert.equal(await realpath(join(f.paths.app, 'current')), initial.release);
  const restore = f.commands.find(command => command.command === 'runuser' && command.args.includes('restore'));
  assert.ok(restore);
  assert.equal(restore.args.at(-1), 'candidate');
});

test('uninstall removes only managed executables and retains state and credentials', async t => {
  const f = await fixture(t);
  const manager = f.make();
  await manager.install({ bundle: f.bundle, yes: true });
  const history = join(f.paths.data, 'state/history');
  await writeFile(history, 'conversation');
  await manager.uninstall();
  await assert.rejects(access(f.paths.app), { code: 'ENOENT' });
  await assert.rejects(access(f.paths.launcher), { code: 'ENOENT' });
  assert.equal(await readFile(history, 'utf8'), 'conversation');
  assert.ok(await readFile(join(f.paths.config, 'environment'), 'utf8'));
  assert.equal(f.commands.some(command => command.command === 'userdel'), false);
  // A retained installation record permits reuse of its own nologin account.
  await manager.install({ bundle: f.bundle, yes: true });
  assert.equal(await readFile(history, 'utf8'), 'conversation');
});

test('setup supports deterministic defaults and credential injection prevention', async () => {
  assert.equal((await setupChoices({ yes: true }, { interactive: false })).provider, 'demo');
  await assert.rejects(setupChoices({ yes: true, provider: 'openai', model: 'model' }, { env: {}, interactive: false }), /Provide OPENAI_API_KEY/);
  assert.throws(() => environmentText({ OPENAI_API_KEY: 'key\nINJECT=value' }), /Invalid credential/);
  assert.throws(() => environmentText({ OPENAI_API_KEY: 'key\tvalue' }), /Invalid credential/);
  assert.equal(environmentText({ OPENAI_API_KEY: 'quotes" and $dollars\\' }), 'OPENAI_API_KEY="quotes\\" and $dollars\\\\"\n');
});


test('persistent core dependencies survive removal of their installed release', async t => {
  const f = await fixture(t);
  const core = join(f.root, 'persistent/cores/core-1');
  const dependencies = join(f.root, 'installed-release/node_modules');
  await mkdir(core, { recursive: true });
  await mkdir(dependencies, { recursive: true });
  await writeFile(join(dependencies, 'dependency.mjs'), 'export default 1;');
  await symlink(dependencies, join(core, 'node_modules'), 'dir');
  await materializeState(join(f.root, 'persistent'));
  assert.equal((await lstat(join(core, 'node_modules'))).isSymbolicLink(), false);
  await rm(join(f.root, 'installed-release'), { recursive: true, force: true });
  assert.equal(await readFile(join(core, 'node_modules/dependency.mjs'), 'utf8'), 'export default 1;');
  await materializeState(join(f.root, 'persistent')); // Repeated materialization is harmless.
});

test('bundle rejects dependency symlinks that escape the authenticated payload', async t => {
  const f = await fixture(t);
  const outside = join(f.root, 'outside-secret');
  await writeFile(outside, 'outside');
  await symlink(outside, join(f.bundle, 'app/node_modules/escape'));
  await assert.rejects(validateBundle(f.bundle, 'x64'), /symlink escapes|relative symlinks/);
});


test('an incomplete managed install can be retried without replacing initialized state or credentials', async t => {
  const f = await fixture(t);
  await assert.rejects(f.make({ health: async () => { throw new Error('sandbox unavailable'); } }).install({ bundle: f.bundle, yes: true }), /sandbox unavailable/);
  const savedEnvironment = await readFile(join(f.paths.config, 'environment'), 'utf8');
  const history = join(f.paths.data, 'state/preserve');
  await writeFile(history, 'already initialized');
  await f.make({ choices: async () => assert.fail('Initialized state must retain its configuration') }).install({ bundle: f.bundle, yes: true });
  assert.equal(await readFile(join(f.paths.config, 'environment'), 'utf8'), savedEnvironment);
  assert.equal(await readFile(history, 'utf8'), 'already initialized');
  assert.ok(f.commands.some(command => command.command === 'systemctl' && command.args.join(' ') === 'stop bot.service'));
});


test('bundle rejects absolute internal links that would break after temporary extraction cleanup', async t => {
  const f = await fixture(t);
  const inside = join(f.bundle, 'app/node_modules/inside');
  await writeFile(inside, 'inside');
  await symlink(inside, join(f.bundle, 'app/node_modules/absolute-link'));
  await assert.rejects(validateBundle(f.bundle, 'x64'), /relative symlinks/);
});


test('installed relative links remain valid after self-extractor cleanup removes the source bundle', async t => {
  const f = await fixture(t);
  const dependencies = join(f.bundle, 'app/node_modules');
  await writeFile(join(dependencies, 'target'), 'retained target');
  await symlink('target', join(dependencies, 'relative-link'));
  const installed = await f.make().install({ bundle: f.bundle, yes: true });
  assert.equal(await readlink(join(installed.release, 'app/node_modules/relative-link')), 'target');
  await rm(f.bundle, { recursive: true, force: true });
  assert.equal(await readFile(join(installed.release, 'app/node_modules/relative-link'), 'utf8'), 'retained target');
});


test('remote update reads downloaded bootstrap through sh for noexec temporary directories', async t => {
  const f = await fixture(t);
  const manager = f.make();
  await manager.install({ bundle: f.bundle, yes: true });
  const payload = Buffer.from('#!/bin/sh\nexit 0\n');
  const hash = createHash('sha256').update(payload).digest('hex');
  const originalFetch = globalThis.fetch;
  t.after(() => { globalThis.fetch = originalFetch; });
  globalThis.fetch = async url => {
    if (url.endsWith('/releases/latest')) return Response.json({ assets: [
      { name: 'bot-linux-x64', url: 'https://api.github.com/repos/SEAN-7/bot/releases/assets/1' },
      { name: 'bot-linux-x64.sha256', url: 'https://api.github.com/repos/SEAN-7/bot/releases/assets/2' },
    ] });
    if (url.endsWith('/assets/2')) return new Response(`${hash}  bot-linux-x64\n`);
    if (url.endsWith('/assets/1')) return new Response(payload);
    assert.fail(`Unexpected download URL: ${url}`);
  };
  await manager.update({});
  const execution = f.commands.find(command => command.command === '/bin/sh');
  assert.ok(execution, 'Download is interpreted rather than executed from the temporary mount');
  assert.equal(execution.args.length, 3);
  assert.match(execution.args[0], /bot-update-.*[\/]bundle$/);
  assert.deepEqual(execution.args.slice(1), ['update', '--yes']);
  assert.deepEqual(execution.options.env, { PATH: '/usr/sbin:/usr/bin:/sbin:/bin' });
  await assert.rejects(access(execution.args[0]), /ENOENT/);
});
