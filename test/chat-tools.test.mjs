import { fixtureApps, fixtureSource } from './process-fixtures.mjs';
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, rm, cp } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { spawn } from 'node:child_process';
import { Store } from '../src/store.mjs';
import { initialize } from '../src/releases.mjs';
import { startSupervisor } from '../src/supervisor.mjs';
import { ChatSession } from '../src/chat-session.mjs';
import { project } from '../apps/chat/behavior.mjs';
const chatModule=api=>api.transformHistory(project);
import {commands as chatCommands} from '../apps/chat/commands.mjs';
import { ChatStore, isNotification } from '../apps/chat/database.mjs';
import { createContext } from '../apps/context/behavior.mjs';
const context=api=>api.registerContext(createContext(api));

async function until(check) {
  const deadline = Date.now() + 10000;
  while (Date.now() < deadline) { if (await check()) return; await new Promise(resolve => setTimeout(resolve, 25)); }
  assert.fail('Timed out waiting for isolated chat');
}

test('chat tools keep raw human text out of model input and preserve paginated legacy history', async t => {
  const workspace=await mkdtemp(join(tmpdir(),'bot-chat-app-'));t.after(()=>rm(workspace,{recursive:true,force:true}));
  const history = [
    { id: 1, role: 'user', content: 'old human', metadata: { source: 'chat' } },
    { id: 2, role: 'assistant', content: 'old reply' },
    { id: 3, role: 'user', content: 'scheduled task', metadata: { source: 'scheduler' } },
    { id: 4, role: 'assistant', content: 'private thought', metadata: { modelOutput: true } },
    { id: 5, role: 'channel', content: 'sent reply', metadata: { module: 'chat' } },
    { id: 6, role: 'user', content: 'new human', metadata: { source: 'chat' } },
  ];
  const tools = new Map(); let transform;
  const api = { paths:{workspace},history: async () => history, transformHistory: fn => { transform = fn; }, publish: async () => assert.fail('Chat must hold its own state') };
  chatModule(api);
  tools.set('chat_read',chatCommands(api).read); tools.set('chat_send',chatCommands(api).send);
  const prepared = transform(history);
  assert.ok(!prepared.some(message => message.role === 'user' && message.metadata?.source === 'chat'));
  assert.ok(!JSON.stringify(prepared).includes('new human'));
  assert.ok(prepared.some(message => message.content === 'private thought'));
  assert.deepEqual((await tools.get('chat_read').execute({ limit: 2 })).messages.map(message => [message.sender, message.text]), [['bot', 'sent reply'], ['human', 'new human']]);
  const first = await tools.get('chat_read').execute({ after: 0, limit: 2 });
  assert.equal(first.hasMore, true);
  assert.deepEqual(first.messages.map(message => message.text), ['old human', 'old reply']);
  assert.deepEqual((await tools.get('chat_read').execute({ after: first.nextAfter })).messages.map(message => message.text), ['sent reply', 'new human']);
  const sent=await tools.get('chat_send').execute({ text: 'hello' }, { callId: 'call' });
  assert.equal(typeof sent.id,'number');
  assert.deepEqual(await tools.get('chat_send').execute({text:'hello'},{callId:'call'}),{...sent,duplicate:true});
  assert.equal((await tools.get('chat_read').execute({})).messages.at(-1).text,'hello');
});

test('publications survive interruption and reject inactive actions and conflicting duplicates', async t => {
  const home = await mkdtemp(join(tmpdir(), 'bot-chat-publication-'));
  const store = new Store(home);
  t.after(async () => { store.close(); await rm(home, { recursive: true, force: true }); });
  store.enqueue({ id: 'human', text: 'hello', metadata: { source: 'chat' } }); store.next();
  store.append({ role: 'assistant', content: '', toolCalls: [{ id: 'send', name: 'chat_send', arguments: { text: 'reply' } }] });
  store.startAction('send', 'human', 'chat_send', { text: 'reply' });
  const result = store.publish('chat', 'send', 'reply', 'human');
  assert.deepEqual(store.publish('chat', 'send', 'reply', 'human'), result);
  assert.throws(() => store.publish('chat', 'send', 'changed', 'human'), /different content/);
  assert.throws(() => store.publish('chat', 'send', 'reply', 'another'), /active tool/);
  store.recover('test interruption');
  assert.equal(store.history().filter(message => message.role === 'channel').length, 1);
  assert.equal(store.history().findLast(message => message.role === 'tool').isError, true);
  assert.throws(() => store.publish('chat', 'send', 'reply', 'human'), /active tool/);
});

test('context compaction retains incoming chat marker and its read results throughout the active turn', async () => {
  let manager;
  context({ config: { maxInputChars: 1000, keepRecentGroups: 1 }, registerContext: fn => { manager = fn; }, state: { get: async (_key, fallback) => fallback, delete: async () => {} } });
  const messages = [
    { id: 1, role: 'context', content: 'Incoming chat message', metadata: { input: true } },
    { id: 2, role: 'assistant', content: '', toolCalls: [{ id: 'read', name: 'chat_read', arguments: {} }] },
    { id: 3, role: 'tool', content: 'human request'.repeat(200), toolCallId: 'read', toolName: 'chat_read' },
  ];
  const result = await manager({ messages, model: { complete: () => assert.fail('Must retain active input') } });
  assert.deepEqual(result.messages, messages);
});

test('isolated chat accepts another message while working, sends only through tools, and survives restart', { timeout: 25000 }, async t => {
  const root = await mkdtemp(join(tmpdir(), 'bot-chat-tool-integration-'));
  const home = join(root, 'home'), defaults = join(root, 'defaults');
  await mkdir(join(defaults, 'fixture-model'), { recursive: true });
  await writeFile(join(defaults, 'fixture-model/module.json'), JSON.stringify({ id: 'fixture-model', name: 'Offline fixture', version: '1', apiVersion: 2, entry: 'index.mjs' }));
  await writeFile(join(defaults, 'fixture-model/index.mjs'), `export default function(api) { api.registerModel({ async complete({messages}) {
    const input=messages.findLast(m=>m.metadata?.input);
    if(!input)throw new Error('No incoming marker');
    if(messages.some(m=>m.role==='user'&&m.metadata?.source==='chat'))throw new Error('Chat leaked as user role');
    if(input.content.includes('first human')||input.content.includes('second human'))throw new Error('Raw input bypassed tool');
    const current=messages.slice(messages.indexOf(input)+1);
    const read=current.findLast(m=>m.role==='tool'&&m.toolName==='shell'&&JSON.parse(m.content).stdout?.includes('messages'));
    const sent=current.findLast(m=>m.role==='tool'&&m.toolName==='shell'&&/send/.test(current.find(x=>x.toolCalls?.some(c=>c.id===m.toolCallId))?.toolCalls?.[0]?.arguments.command||''));
    if(sent)return {text:'PRIVATE FINAL OUTPUT',toolCalls:[]};
    if(!read)return {text:'PRIVATE REASONING',toolCalls:[{id:crypto.randomUUID(),name:'shell',arguments:{command:"bot-app chat read '{}'"}}]};
    await new Promise(resolve=>setTimeout(resolve,500));
    return {text:'PRIVATE TOOL OUTPUT',toolCalls:[{id:crypto.randomUUID(),name:'shell',arguments:{command: ${JSON.stringify("bot-app chat send '{\"text\":\"explicit reply\"}'")}}}]};
  }}); }`);
  await initialize(home, fixtureSource({ modules: { chat: { enabled: true }, 'fixture-model': { enabled: true } }, runtime: { port: 0, sandbox: 'off' } }), { appsDir: await fixtureApps(defaults) });
  let server = await startSupervisor({ home, port: 0, unsafe: true });
  const port = new URL(server.url).port;
  const child = spawn(process.execPath, [new URL('../apps/chat/acp.mjs', import.meta.url).pathname], { env: { ...process.env, BOT_CHAT_URL: server.url, BOT_CHAT_TOKEN: server.uiToken, BOT_CHAT_SESSION: 'isolated', BOT_CHAT_WORKSPACE: join(home, 'workspace') }, stdio: ['pipe', 'pipe', 'pipe'] });
  const chat = new ChatSession(child);
  t.after(async () => { chat.close(); await server.stop(); await rm(root, { recursive: true, force: true }); });
  await chat.start(join(home, 'workspace'));
  await chat.submit('first human');
  assert.equal(chat.busy, false);
  await until(async () => (await fetch(server.url+'/api/status', { headers: { authorization: 'Bearer '+server.token } }).then(r=>r.json())).state==='working');
  await chat.submit('second human'); // Acknowledged before the first response.
  assert.equal(chat.busy, false);
  await until(() => chat.entries.filter(message => message.text === 'explicit reply').length === 1);
  assert.ok(!chat.entries.some(message => message.text.includes('PRIVATE')));
  await until(async () => (await fetch(server.url+'/api/status', { headers: { authorization: 'Bearer '+server.token } }).then(r=>r.json())).pending===0);
  const response = await fetch(server.url+'/api/chat-snapshot', { headers: { authorization: 'Bearer '+server.uiToken } }).then(r=>r.json());
  assert.ok(!response.messages.some(message => message.content.includes('PRIVATE')));
  assert.equal(response.messages.filter(message => message.content === 'explicit reply').length, 0);
  assert.ok(response.messages.filter(m=>m.role==='user').every(m=>isNotification(m.content)));
  const appStore=new ChatStore(join(home,'workspace'));
  assert.equal(appStore.read().messages.filter(m=>m.text==='explicit reply').length,1);appStore.close();
  await server.stop();
  server=await startSupervisor({ home, port: Number(port), unsafe: true });
  await chat.submit('third human');
  await until(() => chat.entries.filter(message => message.text === 'explicit reply').length === 2);
  assert.equal(child.exitCode, null);
});
