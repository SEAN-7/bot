import { mkdir, realpath } from 'node:fs/promises';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';
import { spawn } from 'node:child_process';
import { readJSON } from './releases.mjs';
import { appManifest, commandAt } from './app-package.mjs';
import { sandboxAvailable } from './sandbox.mjs';

const ROOT = dirname(dirname(fileURLToPath(import.meta.url)));

// ACP clients may offer unsandboxed filesystem/terminal operations. An editable
// transport must never use them to bypass the bot's protected core boundary.
export function filterACPOutput(message, outstanding) {
  if (!message || message.jsonrpc !== '2.0') return null;
  if (Object.hasOwn(message, 'id')) {
    if (Object.hasOwn(message, 'method') || !outstanding.has(message.id) || Object.hasOwn(message, 'result') === Object.hasOwn(message, 'error')) return null;
    const method = outstanding.get(message.id);
    outstanding.delete(message.id);
    if (method === 'initialize' && message.result) {
      return { jsonrpc: '2.0', id: message.id, result: {
        protocolVersion: 1, agentInfo: { name: 'bot', version: '0.1.0' }, authMethods: [],
        agentCapabilities: { loadSession: !!message.result.agentCapabilities?.loadSession, promptCapabilities: { image: false, audio: false, embeddedContext: false } },
      } };
    }
    // The supported chat subset has no modes, model pickers, auth commands, or
    // terminal metadata. In particular, don't forward executable auth metadata.
    if (message.error) return { jsonrpc: '2.0', id: message.id, error: { code: Number(message.error.code) || -32603, message: String(message.error.message) } };
    if (method === 'session/new') return { jsonrpc: '2.0', id: message.id, result: { sessionId: String(message.result?.sessionId) } };
    if (method === 'session/load') return { jsonrpc: '2.0', id: message.id, result: {} };
    if (method === 'session/prompt') return { jsonrpc: '2.0', id: message.id, result: { stopReason: ['end_turn','max_tokens','max_turn_requests','refusal','cancelled'].includes(message.result?.stopReason) ? message.result.stopReason : 'end_turn' } };
    return { jsonrpc: '2.0', id: message.id, result: {} };
  }
  if (message.method !== 'session/update' || !['user_message_chunk','agent_message_chunk','agent_thought_chunk','tool_call','tool_call_update'].includes(message.params?.update?.sessionUpdate)) return null;
  const source = message.params.update;
  let update;
  if (source.sessionUpdate.endsWith('_chunk')) {
    if (source.content?.type !== 'text' || typeof source.content.text !== 'string') return null;
    update = { sessionUpdate: source.sessionUpdate, content: { type: 'text', text: source.content.text } };
    if (source.sessionUpdate === 'agent_message_chunk' && source._meta?.['bot/notice'] === true) update._meta = { 'bot/notice': true };
  } else {
    if (typeof source.toolCallId !== 'string') return null;
    update = { sessionUpdate: source.sessionUpdate, toolCallId: source.toolCallId };
    for (const field of ['title','kind','status']) if (typeof source[field] === 'string') update[field] = source[field];
    for (const field of ['rawInput','rawOutput']) if (source[field] !== undefined) update[field] = source[field];
    if (Array.isArray(source.content)) update.content = source.content.filter(part => part.type === 'content' && part.content?.type === 'text' && typeof part.content.text === 'string').map(part => ({ type: 'content', content: { type: 'text', text: part.content.text } }));
  }
  return { jsonrpc: '2.0', method: 'session/update', params: { sessionId: String(message.params.sessionId), update } };
}

function jsonLines(stream, onMessage, onError) {
  let buffer = '';
  stream.setEncoding('utf8');
  const onData = chunk => {
    buffer += chunk.toString();
    if (Buffer.byteLength(buffer) > 2_000_000) { onError(); return; }
    let newline;
    while ((newline = buffer.indexOf('\n')) >= 0) {
      const line = buffer.slice(0, newline); buffer = buffer.slice(newline + 1);
      if (!line.trim()) continue;
      try { onMessage(JSON.parse(line)); } catch { onError(); return; }
    }
  };
  stream.on('data', onData);
  return () => stream.removeListener('data', onData);
}

async function adapterFor(home) {
  const config = await readJSON(join(home,'drafts','config.json'));
  const adapters = [];
  for (const [id, settings] of Object.entries(config.apps)) {
    if (!settings.enabled) continue;
    const folder = join(home, 'drafts', 'apps', id);
    let manifest;
    try { manifest=await appManifest(folder); }
    catch(error) { console.error(`App ${id} is unavailable: ${error.message}`);continue; }
    if (manifest.acp) adapters.push(commandAt(folder,manifest.acp));
  }
  if (adapters.length !== 1) throw new Error('Enable exactly one chat app with an ACP command to open chat. Use serve for headless operation.');
  return adapters[0];
}

function waitFor(child, label) {
  return new Promise((resolve, reject) => {
    const onSignal = signal => { try { child.kill(signal); } catch {} };
    const interrupt = () => onSignal('SIGINT');
    const terminate = () => onSignal('SIGTERM');
    const cleanup = () => { process.removeListener('SIGINT', interrupt); process.removeListener('SIGTERM', terminate); };
    process.on('SIGINT', interrupt); process.on('SIGTERM', terminate);
    child.once('error', error => { cleanup(); reject(new Error(`${label}: ${error.message}`)); });
    child.once('exit', (code, signal) => { cleanup(); resolve(code ?? (signal === 'SIGINT' ? 130 : 1)); });
  });
}

// Only the fixed operator bridge reads control credentials. Editable adapters
// receive the narrower chat token and run behind the filesystem boundary.
export async function runACP({ home, endpoint, unsafe = false }) {
  home = await realpath(home);
  const adapter = await adapterFor(home);
  if (!endpoint?.uiToken) throw new Error('Restart the supervisor to obtain a scoped chat credential.');
  const url = new URL(endpoint.url);
  if (url.protocol !== 'http:' || url.hostname !== '127.0.0.1') throw new Error('Chat requires a loopback endpoint.');
  const sandbox = unsafe ? null : await sandboxAvailable();
  if (!sandbox && !unsafe) throw new Error('Chat adapters require macOS sandbox-exec or Linux bubblewrap.');
  const workspace = join(home, 'workspace'), drafts = join(home, 'drafts'), control = join(home, 'control');
  const temp = join(workspace, '.tmp');
  await mkdir(temp, { recursive: true });
  const env = {
    ...process.env, NODE_OPTIONS: '', TMPDIR: temp, TMP: temp, TEMP: temp,
    BOT_CHAT_URL: url.origin, BOT_CHAT_TOKEN: endpoint.uiToken,
    BOT_CHAT_SESSION: createHash('sha256').update(home).digest('hex').slice(0, 24),
    BOT_CHAT_WORKSPACE: workspace,
    XDG_CACHE_HOME: join(workspace, '.cache'), npm_config_cache: join(workspace, '.cache', 'npm'),
  };
  delete env.BOT_CONTROL_TOKEN;
  let command=adapter.command,args=adapter.args;
  if (sandbox === 'seatbelt') {
    const policy = `(version 1)\n(allow default)\n(deny file-write*)\n(deny signal)\n(allow signal (target same-sandbox))\n(deny file-read* (subpath ${JSON.stringify(control)}))\n${[workspace, drafts].map(p => `(allow file-write* (subpath ${JSON.stringify(p)}))`).join('\n')}\n(allow file-write* (literal "/dev/null"))\n`;
    command = '/usr/bin/sandbox-exec'; args = ['-p', policy, adapter.command, ...adapter.args];
  } else if (sandbox === 'bwrap') {
    command = 'bwrap'; args = ['--die-with-parent', '--unshare-pid', '--ro-bind', '/', '/', '--proc', '/proc', '--dev', '/dev', '--tmpfs', control, '--bind', workspace, workspace, '--bind', drafts, drafts, '--', adapter.command, ...adapter.args];
  }
  const child = spawn(command, args, { cwd: adapter.cwd, env, stdio: ['pipe', 'pipe', 'inherit'] });
  const outstanding = new Map();
  const abort = () => { process.stderr.write('Invalid or oversized ACP transport data\n'); child.kill('SIGTERM'); };
  const removeInput = jsonLines(process.stdin, message => {
    if (message.method && Object.hasOwn(message, 'id')) outstanding.set(message.id, message.method);
    child.stdin.write(JSON.stringify(message) + '\n');
  }, abort);
  const removeOutput = jsonLines(child.stdout, message => {
    const filtered = filterACPOutput(message, outstanding);
    if (filtered) process.stdout.write(JSON.stringify(filtered) + '\n');
  }, abort);
  const eof = () => child.stdin.end();
  process.stdin.once('end', eof);
  child.stdin.on('error', () => {});
  try { return await waitFor(child, 'Chat adapter'); }
  finally { removeInput(); removeOutput(); process.stdin.removeListener('end', eof); process.stdin.pause(); }
}

export async function launchChat({ home, endpoint, unsafe = false }) {
  home = await realpath(home);
  await adapterFor(home);
  if (!endpoint?.uiToken) throw new Error('Restart the supervisor to obtain a scoped chat credential.');
  const args = [join(ROOT, 'src', 'cli.mjs'), 'acp', '--home', home];
  if (unsafe) args.push('--no-sandbox');
  const env = { ...process.env, NODE_OPTIONS: '' };
  delete env.BOT_CONTROL_TOKEN;
  const { runTerminalChat } = await import('./terminal-chat.mjs');
  return runTerminalChat({ command: process.execPath, args, cwd: join(home, 'workspace'), env });
}
