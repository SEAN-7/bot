#!/usr/bin/env node
import { access, readFile, readdir, realpath } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createInterface } from 'node:readline/promises';

const FEATURES = ['chat', 'memory', 'context', 'scheduler', 'skills', 'web-search'];
const VALUES = new Set(['home', 'provider', 'model', 'enable', 'disable', 'port']);
const FLAGS = new Set(['help', 'yes', 'unsafe-development', 'no-sandbox', 'installed-core']);
const CREDENTIALS = { openai: ['OPENAI_API_KEY'], anthropic: ['ANTHROPIC_API_KEY'], google: ['GEMINI_API_KEY'] };
const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

export function parseArgs(args) {
  const options = {};
  const positional = [];
  for (let index = 0; index < args.length; index++) {
    const value = args[index];
    if (!value.startsWith('--')) { positional.push(value); continue; }
    const equal = value.indexOf('=');
    const key = value.slice(2, equal < 0 ? undefined : equal);
    if (!VALUES.has(key) && !FLAGS.has(key)) throw new Error(`Unknown option --${key}. Run bot --help.`);
    if (FLAGS.has(key)) {
      if (equal >= 0) throw new Error(`--${key} does not take a value.`);
      options[key] = true;
      continue;
    }
    const next = equal < 0 ? args[++index] : value.slice(equal + 1);
    if (!next || next.startsWith('--')) throw new Error(`--${key} needs a value.`);
    if (key === 'enable' || key === 'disable') options[key] = [options[key], next].filter(Boolean).join(',');
    else options[key] = next;
  }
  return { positional, options };
}

export function resolveHome(options, { cwd = process.cwd(), env = process.env, demo = false } = {}) {
  return path.resolve(cwd, options.home || env.BOT_HOME || (demo ? '.bot-demo' : '.bot'));
}

export function createConfig(options, env = process.env) {
  const provider = options.provider || 'demo';
  const model = provider === 'demo' ? 'deterministic-demo' : options.model;
  if (!model) throw new Error('Choose an explicit model with --model MODEL when using a real provider.');
  const credentialNames = CREDENTIALS[provider];
  if (credentialNames && !credentialNames.some(name => env[name])) {
    throw new Error(`Provider ${provider} requires ${credentialNames.join(' or ')} in your environment. Setup never stores API keys.`);
  }
  const apps = Object.fromEntries(FEATURES.map(id => [id, { enabled: true }]));
  for (const [flag, enabled] of [['disable', false], ['enable', true]]) {
    for (const id of (options[flag] || '').split(',').filter(Boolean)) {
      if (!FEATURES.includes(id)) throw new Error(`Unknown feature ${id}. Choose ${FEATURES.join(', ')}.`);
      apps[id] = { enabled };
    }
  }
  apps.updates={enabled:true};
  apps['model-provider']={enabled:provider!=='demo',...(provider!=='demo'?{provider,model}:{})};
  apps['demo-model']={enabled:provider==='demo'};
  apps.agent={enabled:true,modelApp:provider==='demo'?'demo-model':'model-provider',contextApp:apps.context.enabled?'context':null,contributors:['memory','skills'].filter(id=>apps[id].enabled),projections:apps.chat.enabled?['chat']:[],tickApps:apps.scheduler.enabled?['scheduler']:[],services:apps.chat.enabled?['chat']:[]};
  const port = Number(options.port || 4317);
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('--port must be an integer from 1 to 65535.');
  return {
    apiVersion: 3, appUpdates: 'direct', controller: 'agent', apps, model: { provider, model },
    runtime: { port, startupTimeoutMs: 10000, heartbeatTimeoutMs: 10000, operationTimeoutMs: 120000, trialMs: 3000, maxRestarts: 2, retryMs: 3000, sandbox: 'auto' },
  };
}

async function exists(location) {
  try { await access(location); return true; } catch (error) { if (error.code === 'ENOENT') return false; throw error; }
}

async function setupOptions(options) {
  if (options.yes || !process.stdin.isTTY || options.provider) return options;
  const rl = createInterface({ input: process.stdin, output: process.stdout });
  try {
    console.log('Set up one conversation. You can change the editable app configuration later.');
    const provider = (await rl.question('Model provider [demo, openai, anthropic, google, or another supported provider] (demo): ')).trim() || 'demo';
    const model = provider === 'demo' ? undefined : (await rl.question('Model ID: ')).trim();
    const disabled = new Set([...(options.disable || '').split(',').filter(Boolean)]);
    for (const id of (options.enable || '').split(',').filter(Boolean)) disabled.delete(id);
    for (const id of FEATURES) {
      const answer = (await rl.question(`Enable ${id}? ${disabled.has(id) ? '[y/N]' : '[Y/n]'}: `)).trim().toLowerCase();
      if (answer === 'n' || answer === 'no') disabled.add(id);
      if (answer === 'y' || answer === 'yes') disabled.delete(id);
    }
    return { ...options, provider, model, disable: [...disabled].join(','), enable: FEATURES.filter(id => !disabled.has(id)).join(',') };
  } finally { rl.close(); }
}

export async function readEndpoint(home) {
  let endpoint;
  try { endpoint = JSON.parse(await readFile(path.join(home, 'control', 'endpoint.json'), 'utf8')); }
  catch (error) { if (error.code === 'ENOENT') return null; throw error; }
  const url = new URL(endpoint.url);
  if (url.protocol !== 'http:' || url.hostname !== '127.0.0.1' || !endpoint.token) throw new Error('Invalid local control endpoint.');
  return { ...endpoint, url: url.origin };
}

async function request(endpoint, route, body) {
  const response = await fetch(new URL(route, endpoint.url), {
    method: body === undefined ? 'GET' : 'POST',
    headers: { Authorization: `Bearer ${endpoint.token}`, ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(30000),
  });
  const result = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(result.error || `Control request failed (${response.status}).`);
  return result;
}

async function runningEndpoint(home) {
  const endpoint = await readEndpoint(home);
  if (!endpoint) return null;
  try { await request(endpoint, '/api/status'); return endpoint; }
  catch (error) {
    if (error.cause?.code === 'ECONNREFUSED') return null;
    throw error;
  }
}

function print(value) { console.log(typeof value === 'string' ? value : JSON.stringify(value, null, 2)); }

const HELP = `Usage: bot COMMAND [options]

  setup                 Create a new installation; existing homes are preserved
  start                 Run the supervisor without a frontend (source alias for serve)
  serve                 Run the supervisor without a frontend
  chat                  Attach terminal chat to a running supervisor
  acp                   Internal protected ACP chat bridge
  demo                  Create/start an isolated deterministic demo (.bot-demo)
  status                Show local runtime health
  apps                  List installed apps
  apps install DIR      Install an executable app for use on its next invocation
  apply                 Activate edited app drafts
  recover               Restore the previous tested app revision
  core install DIR      Trial an operator-supplied runtime core

  --home DIR            Installation directory (or BOT_HOME; default .bot)
  --provider PROVIDER   demo (default), openai, anthropic, google, or another supported provider
  --model MODEL         Required for a real provider
  --disable IDS         Comma-separated chat,memory,context,scheduler,skills,web-search
  --enable IDS          Explicitly enable setup features
  --port NUMBER         Local HTTP port (default 4317)
  --yes                 Accept setup defaults without prompts
  --no-sandbox          Run without OS isolation (apps share the bot account)
  --unsafe-development  Legacy alias for --no-sandbox

Credentials come from your environment. Setup and demo make no paid model calls.
Send ordinary messages in chat. No service installation or automatic frontend downloads.
For a managed Linux installation, bot start/stop control the background service.`;

export async function main(argv = process.argv.slice(2)) {
  const { positional, options } = parseArgs(argv);
  const [command, subcommand, source] = positional;
  if (!command || options.help) { print(HELP); return; }
  if (['demo', 'chat'].includes(command) && (!process.stdin.isTTY || !process.stdout.isTTY)) throw new Error('Terminal chat needs an interactive terminal. Run bot serve for headless operation.');
  if (command === 'stop') throw new Error('bot stop controls the managed Linux service. From source, stop bot serve with Ctrl+C or SIGTERM.');
  const home = resolveHome(options, { demo: command === 'demo' });
  const releases = () => import('./releases.mjs');
  if (command === 'setup') {
    if ((await readdir(home).catch(error => { if (error.code === 'ENOENT') return []; throw error; })).length) throw new Error(`Setup will not overwrite ${home}. Choose a new --home directory.`);
    const config = createConfig(await setupOptions(options));
    const { initialize } = await releases();
    const result = await initialize(home, config, { appsDir: path.join(ROOT, 'apps'), fixedCoreRoot: options['installed-core'] ? ROOT : undefined });
    print(`Created ${home}\nModel: ${config.model.provider} / ${config.model.model}\nRevision: ${result.revision}\nStart: node src/cli.mjs start --home ${JSON.stringify(home)}`);
    return;
  }
  if (command === 'demo') {
    const { initialize } = await releases();
    if (!await exists(home)) await initialize(home, createConfig({ ...options, provider: 'demo' }), { appsDir: path.join(ROOT, 'apps'), fixedCoreRoot: options['installed-core'] ? ROOT : undefined });
    else {
      // Never silently repurpose an existing real installation as a demo.
      const { readCurrent } = await releases();
      const installation = await readCurrent(home);
      if (installation.config?.model?.provider !== 'demo') throw new Error(`Refusing to start a non-demo installation with demo: ${home}.`);
    }
  }
  if (command === 'acp' || command === 'chat') {
    const endpoint = await runningEndpoint(home);
    if (!endpoint) throw new Error('The supervisor is stopped. Run bot start, or bot serve in another terminal.');
    if (!endpoint.uiToken) throw new Error('The endpoint has no scoped chat token. Restart the supervisor with this version.');
    const { launchChat, runACP } = await import('./chat-client.mjs');
    const result = await (command === 'chat' ? launchChat : runACP)({ home, endpoint, unsafe: !!(options['no-sandbox'] || options['unsafe-development']) });
    if (typeof result === 'number') process.exitCode = result;
    return;
  }
  if (command === 'start' || command === 'demo' || command === 'serve') {
    const { startSupervisor } = await import('./supervisor.mjs');
    const port = options.port === undefined ? undefined : Number(options.port);
    if (port !== undefined && (!Number.isInteger(port) || port < 1 || port > 65535)) throw new Error('--port must be an integer from 1 to 65535.');
    const supervisor = await startSupervisor({ home, port, fixedCoreRoot: options['installed-core'] ? ROOT : undefined, unsafe: !!(options['no-sandbox'] || options['unsafe-development']) });
    if (!supervisor.uiToken) { await supervisor.stop(); throw new Error('Supervisor did not provide a scoped chat token.'); }
    print(`${command === 'demo' ? 'Demo mode — no AI or paid calls.\n' : ''}Home: ${home}\n${command !== 'demo' ? `Local API: ${supervisor.url}\nAttach with bot chat --home ${JSON.stringify(home)}\nKeep this terminal open. Ctrl+C stops the bot.` : 'Starting full-screen terminal chat.'}`);
    let stopping = false;
    const stop = async () => {
      if (stopping) return;
      stopping = true;
      await supervisor.stop();
      process.exitCode = 0;
    };
    process.once('SIGINT', stop);
    process.once('SIGTERM', stop);
    if (command === 'demo') {
      try {
        const { launchChat } = await import('./chat-client.mjs');
        const endpoint = await readEndpoint(home);
        const code = await launchChat({ home, endpoint, unsafe: !!(options['no-sandbox'] || options['unsafe-development']) });
        if (typeof code === 'number') process.exitCode = code;
      } finally { await supervisor.stop(); process.removeListener('SIGINT', stop); process.removeListener('SIGTERM', stop); }
    }
    return;
  }
  if (command === 'status') {
    const endpoint = await runningEndpoint(home);
    print(endpoint ? await request(endpoint, '/api/status') : { state: 'stopped', home });
    return;
  }
  if (command === 'apps' && !subcommand) {
    const endpoint = await runningEndpoint(home);
    if (endpoint) { print(await request(endpoint, '/api/apps')); return; }
    const entries=await readdir(path.join(home,'drafts','apps'),{withFileTypes:true});const apps=[];
    for(const entry of entries.filter(entry=>entry.isDirectory()&&!entry.name.startsWith('_')))apps.push(JSON.parse(await readFile(path.join(home,'drafts','apps',entry.name,'app.json'),'utf8')));
    print({state:'stopped',drafts:apps});
    return;
  }
  if (command === 'apps' && subcommand === 'install') {
    if (!source) throw new Error('Usage: bot apps install /path/to/app [--home DIR]');
    const endpoint = await runningEndpoint(home);
    const { installApp, stageDrafts } = await releases();
    const manifest = await installApp(home, path.resolve(source), { enable: true });
    const current=await (await releases()).readCurrent(home);
    print({ installed: manifest.id, ...(manifest.id===current.config.controller
      ? (endpoint ? await request(endpoint, '/api/update', {}) : { revision: await stageDrafts(home), state: 'controller staged for next start' })
      : {state:'available on next invocation'}) });
    return;
  }
  if (command === 'apply' || command === 'recover') {
    const endpoint = await runningEndpoint(home);
    if (endpoint) print(await request(endpoint, command === 'apply' ? '/api/update' : '/api/recover', {}));
    else {
      const { stageDrafts, rollback } = await releases();
      print({ revision: await (command === 'apply' ? stageDrafts(home) : rollback(home)), state: 'ready for next start' });
    }
    return;
  }
  if (command === 'core' && subcommand === 'install') {
    if (!source) throw new Error('Usage: bot core install /path/to/bot-core [--home DIR]');
    const endpoint = await runningEndpoint(home);
    if (endpoint) print(await request(endpoint, '/api/core-update', { source: path.resolve(source) }));
    else {
      const { stageCore } = await releases();
      print({ revision: await stageCore(home, path.resolve(source)), state: 'staged for next start' });
    }
    return;
  }
  throw new Error(`Unknown command: ${positional.join(' ')}. Run bot --help.`);
}

if (process.argv[1] && (await realpath(process.argv[1]).catch(() => null)) === fileURLToPath(import.meta.url)) {
  main().catch(error => { console.error(`bot: ${error.message}`); process.exitCode = 1; });
}
