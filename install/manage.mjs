#!/usr/bin/env node
// Root-owned installation management. It never runs from an agent-editable directory.
import { readFile, writeFile, mkdir, cp, rename, symlink, readlink, realpath, lstat, readdir, rm, chmod, mkdtemp } from 'node:fs/promises';
import { join, dirname, resolve, relative, isAbsolute } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { randomUUID, createHash } from 'node:crypto';
import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline/promises';
import { tmpdir } from 'node:os';

export const DEFAULT_PATHS = { app: '/opt/bot', data: '/var/lib/bot', config: '/etc/bot', service: '/etc/systemd/system/bot.service', launcher: '/usr/local/bin/bot', osRelease: '/etc/os-release', apparmor: '/etc/apparmor.d/bot-bwrap', usernsRestriction: '/proc/sys/kernel/apparmor_restrict_unprivileged_userns' };
const MANAGED = 'Managed by self-building-bot installer';
const FEATURES = ['chat', 'memory', 'context', 'scheduler', 'skills', 'web-search'];
const KEYS = { openai: 'OPENAI_API_KEY', anthropic: 'ANTHROPIC_API_KEY', google: 'GEMINI_API_KEY', groq: 'GROQ_API_KEY', openrouter: 'OPENROUTER_API_KEY', mistral: 'MISTRAL_API_KEY', deepseek: 'DEEPSEEK_API_KEY', xai: 'XAI_API_KEY', cerebras: 'CEREBRAS_API_KEY' };
const inside = (parent, child) => { const path = relative(parent, child); return path === '' || (!path.startsWith('..') && !isAbsolute(path)); };
const exists = path => lstat(path).then(() => true, error => { if (error.code === 'ENOENT') return false; throw error; });
const json = async path => JSON.parse(await readFile(path, 'utf8'));
async function atomic(path, text, mode = 0o600) {
  await mkdir(dirname(path), { recursive: true });
  const temporary = `${path}.${randomUUID()}.tmp`;
  await writeFile(temporary, text, { mode });
  await rename(temporary, path);
}
const atomicJSON = (path, value) => atomic(path, JSON.stringify(value, null, 2) + '\n');
export function parseArgs(args) {
  const [command = 'help', ...rest] = args;
  const options = {}, positional = [];
  const values = new Set(['bundle', 'provider', 'model', 'enable', 'disable', 'credential-env', 'repository', 'sandbox']);
  for (let index = 0; index < rest.length; index++) {
    const item = rest[index];
    if (!item.startsWith('--')) { positional.push(item); continue; }
    if (item === '--yes') { options.yes = true; continue; }
    const split = item.indexOf('=');
    const key = item.slice(2, split < 0 ? undefined : split);
    if (!values.has(key)) throw new Error(`Unknown option --${key}`);
    const value = split < 0 ? rest[++index] : item.slice(split + 1);
    if (!value || value.startsWith('--')) throw new Error(`--${key} requires a value`);
    options[key] = value;
  }
  return { command, options, positional };
}
export async function validateBundle(path, arch = process.arch) {
  const root = await realpath(resolve(path));
  const manifest = await json(join(root, 'bundle.json'));
  if (manifest.format !== 1 || manifest.platform !== 'linux' || manifest.arch !== arch || !['x64', 'arm64'].includes(arch) || typeof manifest.version !== 'string' || !/^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$/.test(manifest.version)) throw new Error('Unsupported bundle format, platform, architecture, or version');
  for (const item of ['app/src/cli.mjs', 'app/src/releases.mjs', 'app/package.json', 'app/apps', 'app/node_modules', 'runtime/node/bin/node', 'app/src/terminal-chat.mjs', 'app/src/chat-session.mjs', 'app/node_modules/@earendil-works/pi-tui/package.json', 'install/manage.mjs', 'install/update-state.mjs', 'install/materialize-state.mjs', 'install/templates/bot', 'install/templates/bot.service']) {
    if (!inside(root, await realpath(join(root, item)))) throw new Error(`Bundle path escapes release: ${item}`);
  }
  const inspect = async directory => {
    for (const entry of await readdir(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name);
      if (entry.isSymbolicLink()) {
        if (isAbsolute(await readlink(path))) throw new Error(`Bundle requires relative symlinks: ${relative(root, path)}`);
        if (!inside(root, await realpath(path))) throw new Error(`Bundle symlink escapes release: ${relative(root, path)}`);
      } else if (entry.isDirectory()) await inspect(path);
      else if (!entry.isFile()) throw new Error('Bundle contains a special filesystem object');
    }
  };
  await inspect(root);
  if ((await json(join(root, 'app/package.json'))).botCoreApi !== 3) throw new Error('Bundle core API is incompatible');
  return { root, manifest };
}
export function environmentText(credentials) {
  return Object.entries(credentials).map(([key, value]) => {
    if (!/^[A-Z][A-Z0-9_]{0,63}$/.test(key) || typeof value !== 'string' || /[\x00-\x1f\x7f]/.test(value)) throw new Error('Invalid credential environment value');
    return `${key}=${JSON.stringify(value)}\n`;
  }).join('');
}
export function parseEnvironment(text) {
  const result = {};
  for (const line of text.split('\n').filter(Boolean)) {
    const match = /^([A-Z][A-Z0-9_]*)=(".*")$/.exec(line);
    if (!match) throw new Error('Invalid managed environment file');
    result[match[1]] = JSON.parse(match[2]);
  }
  return result;
}
export function runCommand(command, args = [], { env = process.env, inherit = false, allowFailure = false } = {}) {
  return new Promise((resolveResult, reject) => {
    const child = spawn(command, args, { env, stdio: inherit ? 'inherit' : ['ignore', 'pipe', 'pipe'] });
    let stdout = '';
    if (!inherit) { child.stdout.on('data', chunk => { stdout = (stdout + chunk).slice(-2_000_000); }); child.stderr.resume(); }
    child.once('error', () => reject(new Error(`Could not run ${command}`)));
    child.once('exit', (code, signal) => {
      if (code === 0 || allowFailure) resolveResult({ code: code ?? 1, stdout });
      else reject(new Error(`${command} failed (${code ?? signal}); inspect the service logs if applicable`));
    });
  });
}
async function ask(question) {
  const rl = createInterface({ input: process.stdin, output: process.stdout });
  try { return (await rl.question(question)).trim(); } finally { rl.close(); }
}
async function secret(question) {
  if (!process.stdin.isTTY) throw new Error('Provide credentials in the environment for noninteractive setup');
  process.stdout.write(question);
  const prior = process.stdin.isRaw;
  process.stdin.setRawMode(true); process.stdin.resume();
  return new Promise((resolveSecret, reject) => {
    let text = '';
    const finish = (error) => {
      process.stdin.removeListener('data', onData); process.stdin.setRawMode(prior); process.stdin.pause(); process.stdout.write('\n');
      if (error) reject(error); else resolveSecret(text);
    };
    const onData = chunk => {
      for (const char of chunk.toString()) {
        if (char === '\u0003') { finish(new Error('Setup cancelled')); return; }
        if (char === '\r' || char === '\n') { finish(); return; }
        if (char === '\u007f' || char === '\b') text = text.slice(0, -1);
        else if (char >= ' ') text += char;
      }
    };
    process.stdin.on('data', onData);
  });
}
export async function setupChoices(options, { env = process.env, question = ask, password = secret, interactive = process.stdin.isTTY, print = console.log, catalogue = [] } = {}) {
  let provider = options.provider, model = options.model;
  const disabled = new Set([ ...(options.disable || '').split(',').filter(Boolean)]);
  for (const feature of disabled) if (!FEATURES.includes(feature)) throw new Error(`Unknown feature ${feature}`);
  for (const feature of (options.enable || '').split(',').filter(Boolean)) {
    if (!FEATURES.includes(feature)) throw new Error(`Unknown feature ${feature}`);
    disabled.delete(feature);
  }
  if (!provider && !options.yes && interactive) {
    print('Providers: demo (no paid calls), ' + [...new Set(catalogue.map(item => item.provider))].join(', '));
    provider = await question('Provider [demo]: ') || 'demo';
  }
  provider ||= 'demo';
  if (!/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(provider)) throw new Error('Invalid provider');
  if (provider !== 'demo' && !model && interactive && !options.yes) {
    const available = catalogue.filter(item => item.provider === provider);
    print(available.map((item, index) => `${index + 1}. ${item.id}${item.name ? ' — ' + item.name : ''}`).join('\n'));
    const selection = await question('Model ID or number: ');
    model = available[Number(selection) - 1]?.id || selection;
  }
  if (provider !== 'demo' && catalogue.length && !catalogue.some(item => item.provider === provider && item.id === model)) throw new Error('Provider/model combination is not in the bundled registry');
  if (provider !== 'demo' && (!model || /[\r\n\0]/.test(model))) throw new Error('A real provider requires --model or an interactive model selection');
  if (!options.yes && interactive) for (const feature of FEATURES) {
    const answer = await question(`Enable ${feature}? ${disabled.has(feature) ? '[y/N]' : '[Y/n]'}: `);
    if (/^n(?:o)?$/i.test(answer)) disabled.add(feature);
    if (/^y(?:es)?$/i.test(answer)) disabled.delete(feature);
  }
  const credentials = {};
  if (provider !== 'demo') {
    const key = options['credential-env'] || KEYS[provider];
    if (!key || !/^[A-Z][A-Z0-9_]{0,63}$/.test(key) || ['GH_TOKEN', 'GITHUB_TOKEN', 'BOT_CONTROL_TOKEN', 'NODE_OPTIONS'].includes(key)) throw new Error('For this provider specify --credential-env with its API-key variable name');
    const value = env[key] || (interactive && !options.yes ? await password(`${key} (hidden): `) : '');
    if (!value) throw new Error(`Provide ${key} in the environment`);
    credentials[key] = value;
  }
  environmentText(credentials);
  return { provider, model: provider === 'demo' ? 'deterministic-demo' : model, disabled: [...disabled], credentials };
}
function substitute(template, paths) {
  for (const [key, value] of [['APP_ROOT', paths.app], ['DATA_ROOT', paths.data], ['CONFIG_ROOT', paths.config]]) {
    if (!/^\/[a-zA-Z0-9_./-]+$/.test(value)) throw new Error('Installation paths must be absolute paths without shell or systemd metacharacters');
    template = template.replaceAll(`@${key}@`, value);
  }
  return template;
}
export function createManager({ paths = DEFAULT_PATHS, run = runCommand, platform = process.platform, arch = process.arch, uid = process.getuid?.(), env = process.env, print = console.log, choices = setupChoices, health, stage, ownerUid = uid, interactive = process.stdin.isTTY } = {}) {
  const recordPath = join(paths.config, 'installation.json');
  const appMarker = join(paths.app, '.managed-by-bot.json');
  const environmentPath = join(paths.config, 'environment');
  const state = join(paths.data, '.bot');
  async function ensureStatePath() {
    // Existing installations keep working while adopting the conventional home.
    if (!await exists(state) && await exists(join(paths.data, 'state'))) await symlink('state', state, 'dir');
  }
  const current = join(paths.app, 'current');
  const nodeAt = release => join(release, 'runtime/node/bin/node');
  const cliAt = release => join(release, 'app/src/cli.mjs');
  const safeEnv = credentials => ({ PATH: `${current}/runtime/node/bin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin`, HOME: paths.data, LANG: env.LANG || 'C.UTF-8', ...(env.TERM ? { TERM: env.TERM } : {}), ...(env.COLORTERM ? { COLORTERM: env.COLORTERM } : {}), ...credentials });
  const asBot = (command, args, credentials = {}, inherit = false) => run('runuser', ['-u', 'bot', '--', command, ...args], { env: safeEnv(credentials), inherit });
  const systemctl = (...args) => run('systemctl', args);
  async function platformCheck() {
    if (platform !== 'linux' || uid !== 0) throw new Error('Installation management requires root on Linux; use sudo');
    if (!['x64', 'arm64'].includes(arch)) throw new Error('Only Linux x64 and arm64 bundles are supported');
    const text = await readFile(paths.osRelease, 'utf8');
    const id = /^ID=(?:"([^"\n]+)"|([^\n]+))$/m.exec(text);
    if (!['debian', 'ubuntu'].includes(id?.[1] || id?.[2])) throw new Error('The first installer supports Debian and Ubuntu with systemd');
    await run('systemctl', ['--version']);
  }
  async function rootOwned(path) {
    const info = await lstat(path);
    if (info.isSymbolicLink() || info.uid !== ownerUid || (info.mode & 0o022)) throw new Error(`Refusing installation path not owned and protected by root: ${path}`);
  }
  async function record() {
    if (!await exists(recordPath)) return null;
    await rootOwned(paths.config);
    await rootOwned(recordPath);
    return json(recordPath);
  }
  async function ownership({ fresh = false } = {}) {
    const known = await record();
    if (known && known.manager !== MANAGED) throw new Error('Refusing an unrelated installation record');
    for (const path of [paths.app, paths.config, paths.data]) {
      if (await exists(path)) {
        if ((await lstat(path)).isSymbolicLink()) throw new Error('Refusing symbolic installation directories');
        if (!known) throw new Error(`Refusing preexisting unrelated directory: ${path}`);
        if (path !== paths.data) await rootOwned(path);
      }
    }
    for (const path of [paths.service, paths.launcher]) if (await exists(path)) {
      if ((await lstat(path)).isSymbolicLink() || !(await readFile(path, 'utf8')).includes(MANAGED) || !known) throw new Error(`Refusing unrelated file: ${path}`);
    }
    if (await exists(paths.app)) {
      if (!await exists(appMarker)) throw new Error('Managed application marker is missing');
      await rootOwned(appMarker);
      if ((await json(appMarker)).manager !== MANAGED) throw new Error('Unrelated application marker');
    }
    for (const path of [paths.service, paths.launcher]) if (await exists(path)) await rootOwned(path);
    const account = await run('getent', ['passwd', 'bot'], { allowFailure: true });
    if (account.code === 0 && account.stdout.trim()) {
      const fields = account.stdout.trim().split(':');
      if (!known || Number(fields[2]) !== known.uid || fields[5] !== paths.data || !['/usr/sbin/nologin', '/sbin/nologin'].includes(fields[6])) throw new Error('Refusing preexisting unrelated bot account');
    } else if (known?.uid !== undefined) throw new Error('Managed bot account is missing; restore it before managing this installation');
    const group = await run('getent', ['group', 'bot'], { allowFailure: true });
    if (!known && group.code === 0 && group.stdout.trim()) throw new Error('Refusing preexisting unrelated bot group');
    if (known && account.code === 0) {
      const groups = await run('id', ['-nG', 'bot']);
      if (groups.stdout.trim().split(/\s+/).filter(Boolean).some(group => group !== 'bot')) throw new Error('The bot account has privileged or unrelated supplementary groups');
    }
    if (fresh && known?.installed) throw new Error('Bot is already installed; run bot update');
    if (!fresh && !known) throw new Error('Bot is not installed');
    return { known, account };
  }
  async function copyRelease(bundle) {
    const release = join(paths.app, 'releases', `${bundle.manifest.version}-${randomUUID().slice(0, 8)}`);
    await mkdir(dirname(release), { recursive: true, mode: 0o755 });
    await chmod(paths.app, 0o755);
    await chmod(dirname(release), 0o755);
    await cp(bundle.root, release, { recursive: true, dereference: false, verbatimSymlinks: true });
    // Root owns the installed executable tree; the bot receives only read/execute.
    await run('chown', ['-R', 'root:root', release]);
    await run('chmod', ['-R', 'a+rX,go-w', release]);
    return release;
  }
  async function point(release) {
    const temporary = join(paths.app, `.current-${randomUUID()}`);
    await symlink(release, temporary, 'dir');
    await rename(temporary, current);
  }
  async function configFiles(release) {
    const noSandbox = (await record())?.sandbox === 'none';
    const service = substitute(await readFile(join(release, 'install/templates/bot.service'), 'utf8'), paths).replaceAll('@SANDBOX_ARGS@', noSandbox ? '--no-sandbox' : '');
    const launcher = substitute(await readFile(join(release, 'install/templates/bot'), 'utf8'), paths);
    await atomic(paths.service, service, 0o644);
    await atomic(paths.launcher, launcher, 0o755);
  }
  async function readCredentials() { return await exists(environmentPath) ? parseEnvironment(await readFile(environmentPath, 'utf8')) : {}; }
  async function defaultHealth({ candidate, allowTrial = false } = {}) {
    const protectedRequired = (await record())?.sandbox !== 'none';
    const deadline = Date.now() + 30000;
    let explanation = 'Supervisor did not become ready';
    while (Date.now() < deadline) {
      try {
        const endpoint = await json(join(state, 'control', 'endpoint.json'));
        const url = new URL(endpoint.url);
        if (url.protocol !== 'http:' || url.hostname !== '127.0.0.1') throw new Error('Invalid supervisor endpoint');
        const response = await fetch(new URL('/api/status', url), { headers: { authorization: `Bearer ${endpoint.token}` }, signal: AbortSignal.timeout(2000) });
        if (response.ok) {
          const value = await response.json();
          if (value.protected === protectedRequired && ['idle', 'working'].includes(value.state) && (!candidate || (value.revision === candidate && (allowTrial || !value.trial)))) return value;
          explanation = value.error || value.state;
        }
      } catch {}
      await new Promise(resolve => setTimeout(resolve, 250));
    }
    throw new Error(`Supervisor health check failed: ${explanation}`);
  }
  const checkHealth = health || defaultHealth;
  async function install(options) {
    await platformCheck();
    const { known } = await ownership({ fresh: true });
    await ensureStatePath();
    if (!options.bundle) throw new Error('Install requires a verified local bundle');
    const sandbox = options.sandbox || known?.sandbox || (known ? 'auto' : 'none');
    if (!['auto', 'none'].includes(sandbox)) throw new Error('--sandbox must be auto or none');
    const bundle = await validateBundle(options.bundle, arch);
    if (known && await exists(paths.service)) await systemctl('stop', 'bot.service');
    if (options.repository && !/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(options.repository)) throw new Error('Repository must be OWNER/REPO');
    const prerequisites = [...(sandbox === 'auto' ? ['bubblewrap'] : []), 'procps', 'sudo'];
    print(`Installing host prerequisites (${prerequisites.join(', ')})...`);
    await run('apt-get', ['update'], { inherit: true, env: { PATH: '/usr/sbin:/usr/bin:/sbin:/bin', DEBIAN_FRONTEND: 'noninteractive' } });
    await run('apt-get', ['install', '-y', ...prerequisites], { inherit: true, env: { PATH: '/usr/sbin:/usr/bin:/sbin:/bin', DEBIAN_FRONTEND: 'noninteractive' } });
    await mkdir(paths.config, { recursive: true, mode: 0o700 });
    await mkdir(paths.data, { recursive: true, mode: 0o700 });
    if (!known) await run('useradd', ['--system', '--user-group', '--home-dir', paths.data, '--shell', '/usr/sbin/nologin', '--no-create-home', 'bot']);
    const account = await run('getent', ['passwd', 'bot']);
    const fields = account.stdout.trim().split(':');
    const accountUid = Number(fields[2]);
    if (!Number.isInteger(accountUid) || accountUid <= 0) throw new Error('Dedicated bot account was not created');
    const installation = { manager: MANAGED, uid: accountUid, sandbox, repository: options.repository || bundle.manifest.repository || known?.repository || null, version: bundle.manifest.version, installed: false, configured: known?.configured ?? !!known?.installed, setupPending: known?.setupPending || false, setup: known?.setup || null };
    await atomicJSON(recordPath, installation);
    await mkdir(paths.app, { recursive: true, mode: 0o755 });
    await atomicJSON(appMarker, { manager: MANAGED });
    await run('chown', ['bot:bot', paths.data]);
    await mkdir(state, { recursive: true, mode: 0o700 });
    await run('chown', ['bot:bot', state]);
    if (!await exists(environmentPath)) await atomic(environmentPath, '');
    await chmod(environmentPath, 0o600);
    const release = await copyRelease(bundle);
    await point(release);
    await configFiles(release);
    await installSandboxProfile();
    await systemctl('daemon-reload');
    await systemctl('unmask', '--runtime', 'bot.service');
    await systemctl('disable', '--now', 'bot.service');
    await atomicJSON(recordPath, { ...installation, installed: true, release });
    print(`Installed bot ${installation.version}. The service is stopped. Run bot start to configure and start it, then bot chat to open chat.`);
    return { release };
  }
  async function installSandboxProfile() {
    if ((await record())?.sandbox === 'none') return;
    if (!paths.apparmor || !paths.usernsRestriction || !await exists(paths.usernsRestriction) || (await readFile(paths.usernsRestriction, 'utf8')).trim() !== '1') return;
    // Ubuntu restricts unprivileged namespaces. Allow only this executable;
    // do not weaken the global kernel setting or bypass the worker sandbox.
    if (await exists(paths.apparmor)) {
      await rootOwned(paths.apparmor);
      if (!(await readFile(paths.apparmor, 'utf8')).includes(MANAGED)) throw new Error('Refusing unrelated Bubblewrap AppArmor profile');
    }
    await atomic(paths.apparmor, `# ${MANAGED}
abi <abi/4.0>,
include <tunables/global>
profile bot-bwrap /usr/bin/bwrap flags=(unconfined) {
  userns,
}
`, 0o644);
    await run('apparmor_parser', ['-r', paths.apparmor]);
  }
  async function configure(known, release, options) {
    if (!interactive && !options.yes) throw new Error('First launch needs an interactive terminal. SSH in and run bot start.');
    let catalogue = [];
    if (choices === setupChoices) {
      const registry = pathToFileURL(join(release, 'app/node_modules/@earendil-works/pi-ai/dist/providers/all.js')).href;
      const result = await run(nodeAt(release), ['--input-type=module', '-e', `import {builtinModels} from ${JSON.stringify(registry)}; console.log(JSON.stringify(builtinModels().getModels().map(({provider,id,name})=>({provider,id,name}))));`], { env: safeEnv({}) });
      catalogue = JSON.parse(result.stdout);
    }
    print('Welcome to bot. Choose your model and features.');
    const selections = known.setupPending
      ? { ...known.setup, credentials: await readCredentials() }
      : await choices(options, { env, print, catalogue, interactive });
    const setup = { provider: selections.provider, model: selections.model, disabled: selections.disabled };
    if (!known.setupPending && !await exists(join(state, 'control/releases.json')) && (await readdir(state)).length) throw new Error('Refusing unrelated incomplete state; preserve it before retrying setup');
    await atomic(environmentPath, environmentText(selections.credentials));
    await atomicJSON(recordPath, { ...known, setupPending: true, setup });
    if (await exists(join(state, 'control/releases.json'))) {
      // Also recovers interrupted setup and the old installer's unstarted demo.
      const configPath = join(state, 'drafts/config.json');
      const config = await json(configPath);
      config.apps ||= {};
      for (const feature of FEATURES) {
        if(config.modules&&feature!=='web-search')config.modules[feature] = { ...config.modules[feature], enabled: !selections.disabled.includes(feature) };
        config.apps[feature]={...config.apps[feature],enabled:!selections.disabled.includes(feature)};
      }
      if(config.modules)config.modules['pi-model'] = { enabled: selections.provider !== 'demo', provider: selections.provider, model: selections.model };
      if(config.modules)config.modules['demo-model'] = { enabled: selections.provider === 'demo' };
      config.apps['pi-model']={enabled:selections.provider!=='demo',provider:selections.provider,model:selections.model};
      config.apps['demo-model']={enabled:selections.provider==='demo'};
      config.controller='agent';config.apps.agent={...config.apps.agent,enabled:true,modelApp:selections.provider==='demo'?'demo-model':'pi-model',contextApp:config.apps.context?.enabled?'context':null,contributors:['memory','skills'].filter(id=>config.apps[id]?.enabled),projections:config.apps.chat?.enabled?['chat']:[],tickApps:config.apps.scheduler?.enabled?['scheduler']:[],services:config.apps.chat?.enabled?['chat']:[]};
      config.model = { provider: selections.provider, model: selections.model };
      await atomicJSON(configPath, config);
      await run('chown', ['bot:bot', configPath]);
      const metadata = await json(join(state, 'control/releases.json'));
      if (!metadata.pending) await asBot(nodeAt(release), [cliAt(release), 'apply', '--home', state], selections.credentials);
    } else {
      if (known.setupPending) {
        await rm(state, { recursive: true });
        await mkdir(state, { mode: 0o700 });
        await run('chown', ['bot:bot', state]);
      }
      const args = [cliAt(release), 'setup', '--installed-core', '--home', state, '--yes', '--provider', selections.provider, '--model', selections.model];
      if (selections.disabled.length) args.push('--disable', selections.disabled.join(','));
      await asBot(nodeAt(release), args, selections.credentials);
    }
    await asBot(nodeAt(release), [join(release, 'install/materialize-state.mjs'), state]);
    await atomicJSON(recordPath, { ...known, configured: true, setupPending: false, setup });
  }
  async function update(options) {
    await platformCheck();
    const { known } = await ownership();
    await ensureStatePath();
    if (!await exists(current)) throw new Error('Bot was uninstalled; run a bundle install');
    if (!options.bundle) return remoteUpdate(options, known);
    if ((await lstat(options.bundle)).isFile()) {
      const file = await realpath(options.bundle);
      const header = (await readFile(file)).subarray(0, 4096).toString();
      if (!header.startsWith('#!/bin/sh') || !header.includes('bot-bundle.')) throw new Error('Local update file must be a bot self-extracting bundle');
      await run('/bin/sh', [file, 'update', '--yes'], { env: { PATH: '/usr/sbin:/usr/bin:/sbin:/bin' }, inherit: true });
      return { delegated: true };
    }
    const bundle = await validateBundle(options.bundle, arch);
    if (known.configured === false) {
      const release = await copyRelease(bundle);
      await point(release);
      await configFiles(release);
      await installSandboxProfile();
      await systemctl('daemon-reload');
      await atomicJSON(recordPath, { ...known, version: bundle.manifest.version, release });
      print('Updated bot. Run bot start to complete first-launch setup.');
      return { release };
    }
    const metadata = await json(join(state, 'control/releases.json'));
    if (metadata.pending) throw new Error('A module update is pending; settle it before updating the installed runtime');
    let settleCore = false;
    if (metadata.trial) {
      const before = await json(join(state, 'revisions', metadata.trial.from, 'config.json'));
      const active = await json(join(state, 'revisions', metadata.active, 'config.json'));
      if (before.coreRoot === active.coreRoot) throw new Error('A bot-managed module update is on trial; settle it before updating the installed runtime');
      const status = await checkHealth({ candidate: metadata.active, allowTrial: true });
      if (status.state !== 'idle' || status.pending) throw new Error('Previous core update is not idle and healthy');
      settleCore = true;
    }
    const previous = await realpath(current);
    const release = await copyRelease(bundle);
    const credentials = await readCredentials();
    // Stage via the trusted core API while the service is stopped. The supervisor
    // owns candidate preflight and state snapshot/rollback after it starts again.
    let candidate;
    // Migrate the running service's chat credential before its endpoint is
    // removed on shutdown. Open chat apps keep this scoped credential.
    const chatCredentials = join(state, 'control/chat-credentials.json');
    const endpointPath = join(state, 'control/endpoint.json');
    if (!await exists(chatCredentials) && await exists(endpointPath)) {
      const endpoint = await json(endpointPath);
      if (typeof endpoint.uiToken !== 'string' || !/^[a-f0-9]{64}$/.test(endpoint.uiToken)) throw new Error('Invalid existing chat credential; refusing to break connected clients');
      await atomicJSON(chatCredentials, { token: endpoint.uiToken, legacyTokens: [] });
      await run('chown', ['bot:bot', chatCredentials]);
    }
    await systemctl('stop', 'bot.service');
    try {
      if (settleCore) await asBot(nodeAt(previous), [join(release, 'install/update-state.mjs'), 'settle-core', state, join(previous, 'app'), metadata.active], credentials);
      if (stage) candidate = (await stage({ release, state, credentials, previous })).revision;
      else {
        const result = await asBot(nodeAt(release), [join(release, 'install/update-state.mjs'), 'stage', state, join(release, 'app')], credentials);
        candidate = JSON.parse(result.stdout).revision;
      }
      if (typeof candidate !== 'string' || !candidate) throw new Error('Core staging returned no revision');
      await point(release);
      await configFiles(release);
      await systemctl('daemon-reload');
      await systemctl('start', 'bot.service');
      const status = await checkHealth({ candidate });
      if (status.trial) throw new Error('Core health validation did not complete');
      await atomicJSON(recordPath, { ...known, version: bundle.manifest.version, release, previousRelease: previous, installed: true });
      print(`Installed bot ${bundle.manifest.version}; supervisor health: ${status.state}. Core startup validation completed; the previous release remains available for rollback. This check made no model call.`);
      return { release, status, probation: !!status.trial };
    } catch (error) {
      await systemctl('stop', 'bot.service').catch(() => {});
      if (candidate) {
        try {
          await asBot(nodeAt(previous), [join(release, 'install/update-state.mjs'), 'restore', state, join(previous, 'app'), candidate], credentials);
        } catch {
          throw new Error('Update failed and exact candidate state restoration failed; the service is stopped. Preserve state and inspect it before restarting.');
        }
      }
      await point(previous);
      await configFiles(previous);
      await systemctl('daemon-reload');
      await systemctl('start', 'bot.service');
      await checkHealth().catch(() => {});
      throw new Error(`Update failed; restored the previous executable pointer. State was preserved. ${error.message}`);
    }
  }
  async function remoteUpdate(options, known) {
    const repository = options.repository || known.repository;
    if (!repository || !/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository)) throw new Error('Configure the private GitHub repository with install --repository OWNER/REPO, or supply a local bundle');
    let token = env.GH_TOKEN;
    if (!token) {
      try {
        if (env.SUDO_USER && env.SUDO_USER !== 'root' && env.SUDO_USER !== 'bot') {
          const caller = await run('getent', ['passwd', env.SUDO_USER]);
          const callerHome = caller.stdout.trim().split(':')[5];
          const result = await run('runuser', ['-u', env.SUDO_USER, '--', 'gh', 'auth', 'token'], { env: { PATH: '/usr/local/bin:/usr/bin:/bin', HOME: callerHome } });
          token = result.stdout.trim();
        } else token = (await run('gh', ['auth', 'token'])).stdout.trim();
      } catch {}
    }
    if (!token) throw new Error('Private release update requires GH_TOKEN or the administrator gh login. Alternatively download the bundle and run bot update /path/to/bot-linux-' + arch);
    const headers = { authorization: `Bearer ${token}`, accept: 'application/vnd.github+json', 'x-github-api-version': '2022-11-28', 'user-agent': 'bot-installer' };
    const latest = await fetch(`https://api.github.com/repos/${repository}/releases/latest`, { headers, signal: AbortSignal.timeout(30000) });
    if (!latest.ok) throw new Error(`Could not read private GitHub release (${latest.status})`);
    const release = await latest.json();
    const asset = release.assets?.find(asset => asset.name === `bot-linux-${arch}`);
    const checksum = release.assets?.find(asset => asset.name === `bot-linux-${arch}.sha256`);
    if (!asset || !checksum) throw new Error(`Latest release lacks bot-linux-${arch} and its SHA256 file`);
    const directory = await mkdtemp(join(tmpdir(), 'bot-update-'));
    try {
      const download = async asset => {
        if (!/^https:\/\/api\.github\.com\/repos\//.test(asset.url)) throw new Error('Invalid release asset endpoint');
        const response = await fetch(asset.url, { headers: { ...headers, accept: 'application/octet-stream' }, signal: AbortSignal.timeout(120000) });
        if (!response.ok) throw new Error(`Could not download release asset (${response.status})`);
        return Buffer.from(await response.arrayBuffer());
      };
      const expected = (await download(checksum)).toString('utf8').trim().split(/\s+/)[0];
      if (!/^[a-fA-F0-9]{64}$/.test(expected)) throw new Error('Release checksum is invalid');
      const payload = await download(asset);
      if (createHash('sha256').update(payload).digest('hex') !== expected.toLowerCase()) throw new Error('Downloaded release checksum does not match');
      const file = join(directory, 'bundle');
      await writeFile(file, payload, { mode: 0o700 });
      // The downloaded bootstrap validates its embedded payload, then invokes
      // the new release manager. GitHub credentials are excluded from its env.
      await run('/bin/sh', [file, 'update', '--yes'], { env: { PATH: '/usr/sbin:/usr/bin:/sbin:/bin' }, inherit: true });
      return { downloaded: true };
    } finally { token = ''; await rm(directory, { recursive: true, force: true }); }
  }
  async function uninstall() {
    await platformCheck();
    const { known } = await ownership();
    await ensureStatePath();
    if (await exists(paths.service)) await systemctl('stop', 'bot.service');
    if (await exists(current)) {
      const release = await realpath(current);
      await asBot(nodeAt(release), [join(release, 'install/materialize-state.mjs'), state]);
    }
    if (await exists(paths.service)) await systemctl('disable', 'bot.service');
    await rm(paths.service, { force: true });
    await rm(paths.launcher, { force: true });
    await systemctl('daemon-reload');
    await rm(paths.app, { recursive: true, force: true });
    await atomicJSON(recordPath, { ...known, installed: false });
    print(`Removed bot executables and service. Retained ${paths.data}, ${environmentPath}, and the unprivileged bot account.`);
  }
  async function dispatch(command, options = {}, positional = []) {
    if (command === 'help' || command === '--help') { print('Usage: bot COMMAND\n\n  start       Start the background service and enable it after reboot\n  stop        Stop the service and disable startup after reboot\n  chat        Attach the chat plugin to the running service\n  status      Show runtime health\n  logs        Follow service logs\n  update      Update the installed application\n  uninstall   Remove executables; retain conversation and credentials\n\nBare bot shows this help. Closing chat leaves the service running.\nFirst bot start configures the model and features if needed.\nSetup: --provider ID --model ID --enable FEATURES --disable FEATURES --credential-env NAME --yes\nInstall/update: [--bundle DIR] [--repository OWNER/REPO]; install: [--sandbox none|auto]'); return; }
    if (command === 'install') return install(options);
    if (command === 'update') {
      if (positional[0]) options.bundle = positional[0];
      return update(options);
    }
    if (command === 'uninstall') return uninstall();
    await platformCheck(); const { known } = await ownership();
    await ensureStatePath();
    if (command === 'logs') return run('journalctl', ['-u', 'bot.service', '-f', '--no-pager'], { inherit: true });
    if (!['start', 'stop', 'chat', 'status'].includes(command)) throw new Error('Unknown installed bot command. Run bot --help.');
    if (positional.length) throw new Error(`Usage: bot ${command}`);
    const release = await realpath(current);
    if (command === 'stop') {
      await systemctl('disable', '--now', 'bot.service');
      const result = await run('systemctl', ['is-active', 'bot.service'], { allowFailure: true });
      if (result.stdout.trim() !== 'inactive') throw new Error('Bot service did not stop; inspect bot logs.');
      print('Bot stopped. Startup after reboot is disabled. Conversation and credentials are preserved. Run bot start to resume.');
      return;
    }
    if (command === 'start') {
      if (!(known.configured ?? known.installed)) await configure(known, release, options);
      await systemctl('enable', '--now', 'bot.service');
      let status;
      try { status = await checkHealth(); } catch (error) {
        await systemctl('disable', '--now', 'bot.service');
        throw error;
      }
      print(`Bot running in the background; supervisor health: ${status.state}. Startup after reboot is enabled. Run bot chat to attach.`);
      return status;
    }
    if (command === 'chat' && !(known.configured ?? known.installed)) throw new Error('Bot is not configured. Run bot start first.');
    const isolationArgs = known.sandbox === 'none' && command === 'chat' ? ['--no-sandbox'] : [];
    return asBot(nodeAt(release), [cliAt(release), command, '--home', state, ...isolationArgs], {}, true);
  }
  return { dispatch, install, update, uninstall };
}
if (process.argv[1] && (await realpath(process.argv[1]).catch(() => null)) === fileURLToPath(import.meta.url)) {
  try {
    const { command, options, positional } = parseArgs(process.argv.slice(2));
    await createManager().dispatch(command, options, positional);
  } catch (error) { process.stderr.write(`Bot: ${error.message}\n`); process.exitCode = 1; }
}
