import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';
import { once } from 'node:events';
import { fileURLToPath } from 'node:url';
import { ChatSession } from '../apps/terminal-chat/chat-session.mjs';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { ChatStore } from '../apps/chat/database.mjs';

const adapter = fileURLToPath(new URL('../apps/chat/acp.mjs', import.meta.url));
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(check) {
  for (let index = 0; index < 150; index++) {
    const value = check();
    if (value) return value;
    await delay(20);
  }
  assert.fail('Timed out waiting for ACP message');
}
async function fixture(t, { frontend = false, history = [{ id: 1, role: 'user', content: 'old question' }, { id: 2, role: 'assistant', content: 'old answer' }] } = {}) {
  const workspace=await mkdtemp(join(tmpdir(),'bot-acp-app-'));
  const chatStore=new ChatStore(workspace);
  const events = [], requests = [], output = [];
  let seq = 2, state = 'idle', available = true, dropSubmission = false;
  const accepted = new Set();
  const append = (kind, payload) => events.push({ seq: ++seq, kind, payload });
  const server = createServer(async (request, response) => {
    let text = '';
    for await (const chunk of request) text += chunk;
    const body = text ? JSON.parse(text) : null;
    requests.push({ path: request.url, authorization: request.headers.authorization, body });
    response.setHeader('content-type', 'application/json');
    if (!available) { response.statusCode = 503; response.end('{}'); return; }
    if (request.headers.authorization !== 'Bearer scoped-chat-token') { response.statusCode = 401; response.end('{}'); return; }
    const url = new URL(request.url, 'http://127.0.0.1');
    let value;
    if (url.pathname === '/api/snapshot') value = { after: 2, messages: history };
    else if (url.pathname === '/api/events') value = { events: events.filter(event => event.seq > Number(url.searchParams.get('after'))).slice(0, 500) };
    else if (url.pathname === '/api/status') value = { state, error: state === 'blocked' ? 'Credentials missing' : null };
    else if (url.pathname === '/api/messages' && accepted.has(body.id)) value = { id: body.id, duplicate: true };
    else if (url.pathname === '/api/messages') {
      accepted.add(body.id);
      const input=chatStore.db.prepare('SELECT text FROM messages WHERE event_id=?').get(body.id)?.text;
      append('message', { role: 'user', content: body.text, metadata: { eventId: body.id } });
      if (input === 'blocked') state = 'blocked';
      else if (input !== 'cancel me') {
        append('message', { role: 'assistant', content: '', toolCalls: [{ id: 'call-1', name: 'shell', arguments: { command: 'pwd' } }] });
        append('tool.started', { id: 'call-1', name: 'shell', arguments: { command: 'pwd' } });
        append('message', { role: 'tool', toolCallId: 'call-1', content: '/workspace', isError: false });
        append('message', { role: 'assistant', content: 'completed answer' });
        append('input.completed', { id: body.id });
      }
      value = { id: body.id, duplicate: false };
      if (dropSubmission) { dropSubmission = false; response.destroy(); return; }
    } else if (url.pathname === '/api/cancel') {
      append('input.cancelled', { id: body.id });
      value = { id: body.id, cancelled: true };
    } else { response.statusCode = 404; value = { error: 'Unknown endpoint' }; }
    response.end(JSON.stringify(value));
  });
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  const child = spawn(process.execPath, [adapter], { env: { ...process.env, BOT_CHAT_URL: `http://127.0.0.1:${server.address().port}`, BOT_CHAT_TOKEN: 'scoped-chat-token', BOT_CHAT_SESSION: 'stable-home-session', BOT_CHAT_WORKSPACE: workspace }, stdio: ['pipe', 'pipe', 'pipe'] });
  let stderr = '';
  child.stderr.on('data', chunk => { stderr += chunk; });
  createInterface({ input: child.stdout }).on('line', line => output.push(JSON.parse(line)));
  t.after(async () => {
    const exited = child.exitCode !== null || child.signalCode !== null;
    if (!exited) { const closed = once(child, 'exit'); child.kill('SIGTERM'); await closed; }
    await new Promise(resolve => server.close(resolve));
    chatStore.close();await rm(workspace,{recursive:true,force:true});
  });
  const send = (id, method, params) => child.stdin.write(JSON.stringify({ jsonrpc: '2.0', ...(id === undefined ? {} : { id }), method, params }) + '\n');
  const reply = id => until(() => output.find(message => message.id === id));
  const updates = () => output.filter(message => message.method === 'session/update').map(message => message.params.update);
  const chat = frontend ? new ChatSession(child) : null;
  if (chat) {
    t.after(() => chat.close());
    await chat.start('/workspace');
  } else {
    send(1, 'initialize', { protocolVersion: 1 });
    assert.equal((await reply(1)).result.protocolVersion, 1);
  }
  return { chat, send, reply, updates, output, requests, append, accepted,
    availability: value => { available = value; }, dropNextSubmission: () => { dropSubmission = true; }, stderr: () => stderr };
}

test('ACP attaches one conversation, replays after new response, reports tools and idle replies', async t => {
  const f = await fixture(t);
  f.send(2, 'session/new', { cwd: '/workspace', mcpServers: [] });
  assert.equal((await f.reply(2)).result.sessionId, 'stable-home-session');
  await until(() => f.updates().some(update => update.content?.text === 'old answer'));
  assert.ok(f.output.findIndex(message => message.id === 2) < f.output.findIndex(message => message.method === 'session/update'));
  f.send(3, 'session/prompt', { sessionId: 'stable-home-session', prompt: [{ type: 'text', text: 'do work' }] });
  assert.equal((await f.reply(3)).result.stopReason, 'end_turn');
  await until(() => f.updates().some(update => update.toolCallId === 'call-1' && update.status === 'completed'));
  assert.deepEqual(f.updates().filter(update => update.toolCallId === 'call-1').map(update => update.status), ['pending', 'in_progress', 'completed']);
  assert.equal(f.updates().filter(update => update.content?.text === 'do work').length, 0);
  f.append('message', { role: 'assistant', content: 'autonomous idle reply' });
  await until(() => f.updates().some(update => update.content?.text === 'autonomous idle reply'));
  assert.equal(f.requests.filter(request => request.path === '/api/messages').length, 1);
  assert.ok(f.requests.every(request => request.authorization === 'Bearer scoped-chat-token'));
  assert.ok(f.requests.every(request => !request.path.includes('update')));
  assert.equal(f.stderr(), '');
});

test('ACP load replays before response and acknowledges delivery while another input is unfinished', async t => {
  const f = await fixture(t);
  f.send(2, 'session/load', { sessionId: 'stable-home-session', cwd: '/workspace', mcpServers: [] });
  await f.reply(2);
  assert.ok(f.output.findIndex(message => message.method === 'session/update') < f.output.findIndex(message => message.id === 2));
  f.send(3, 'session/prompt', { sessionId: 'stable-home-session', prompt: [{ type: 'text', text: 'cancel me' }] });
  assert.equal((await f.reply(3)).result.stopReason, 'end_turn');
  f.send(4, 'session/prompt', { sessionId: 'stable-home-session', prompt: [{ type: 'text', text: 'overlap' }] });
  assert.equal((await f.reply(4)).result.stopReason, 'end_turn');
  assert.equal(f.accepted.size, 2);
  f.send(5, 'session/prompt', { sessionId: 'stable-home-session', prompt: [{ type: 'resource_link', uri: 'file:///secret' }] });
  assert.match((await f.reply(5)).error.message, /Only text/);
});

test('ACP acknowledges blocked input delivery and reports failure separately without losing it', async t => {
  const f = await fixture(t);
  f.send(2, 'session/new', { cwd: '/workspace', mcpServers: [] });
  await f.reply(2);
  f.send(3, 'session/prompt', { sessionId: 'stable-home-session', prompt: [{ type: 'text', text: 'blocked' }] });
  assert.equal((await f.reply(3)).result.stopReason, 'end_turn');
  await until(() => f.updates().some(update => update.content?.text.includes('Credentials missing')));
  assert.equal(f.accepted.size, 1);
  assert.ok(f.updates().some(update => update._meta?.['bot/notice'] === true));
});

test('ACP drains journal pages after the atomic snapshot without skipping tail messages', async t => {
  const f = await fixture(t);
  for (let index = 0; index < 500; index++) f.append('audit', { index });
  f.append('message', { role: 'assistant', content: 'reply beyond first journal page' });
  f.send(2, 'session/new', { cwd: '/workspace', mcpServers: [] });
  await f.reply(2);
  await until(() => f.updates().some(update => update.content?.text === 'reply beyond first journal page'));
  assert.ok(f.requests.some(request => request.path === '/api/events?after=502'));
  assert.equal(f.updates().filter(update => update.content?.text === 'reply beyond first journal page').length, 1);
});

test('Pi chat client replays history, submits once, displays tool progress and receives idle work', async t => {
  const f = await fixture(t, { frontend: true });
  await until(() => f.chat.entries.some(entry => entry.text === 'old answer'));
  assert.equal(f.chat.ready, true);
  await f.chat.submit('do work');
  assert.equal(f.chat.busy, false);
  assert.equal(f.chat.status, 'Ready');
  assert.equal(f.chat.entries.filter(entry => entry.text === 'do work').length, 1);
  await until(() => f.chat.entries.some(entry => entry.role === 'tool' && entry.status === 'completed'));
  const tool = f.chat.entries.find(entry => entry.role === 'tool');
  assert.equal(tool.title, 'shell');
  assert.equal(tool.status, 'completed');
  assert.equal(tool.text, '/workspace');
  await until(() => f.chat.entries.some(entry => entry.text === 'completed answer'));
  f.append('message', { role: 'assistant', content: 'autonomous idle reply' });
  await until(() => f.chat.entries.some(entry => entry.text === 'autonomous idle reply'));
  f.append('message', { role: 'context', content: 'Recovery report', metadata: { recovery: true } });
  await until(() => f.chat.entries.some(entry => entry.text === 'Recovery report'));
  assert.equal(f.chat.entries.at(-1).role, 'notice');
  assert.equal(f.chat.entries.at(-2).text, 'autonomous idle reply');
  assert.equal(f.requests.filter(request => request.path === '/api/messages').length, 1);
});

test('chat hides legacy routine restarts during replay and live updates while preserving genuine recovery notices', async t => {
  const routine = { role: 'context', content: 'Recovery report: Supervisor restarted; resumed the durable conversation', metadata: { recovery: true } };
  const interrupted = { role: 'context', content: 'Recovery report: Supervisor interrupted; resumed the durable conversation', metadata: { recovery: true } };
  const f = await fixture(t, { frontend: true, history: [routine, routine, interrupted, { role: 'assistant', content: 'normal answer' }] });
  await until(() => f.chat.entries.some(entry => entry.text === 'normal answer'));
  assert.deepEqual(f.chat.entries.map(entry => entry.role), ['notice', 'assistant']);
  assert.equal(f.chat.entries[0].text, interrupted.content);
  f.append('message', routine);
  const unresolved = { ...routine, content: routine.content + '\nInterrupted actions: unknown' };
  f.append('message', unresolved);
  await until(() => f.chat.entries.some(entry => entry.text === unresolved.content));
  assert.equal(f.chat.entries.some(entry => entry.text === routine.content), false);
  assert.equal(f.chat.entries.at(-1).role, 'notice');
});

test('chat delivers messages while earlier work is unfinished and reports blocked work separately', async t => {
  const f = await fixture(t, { frontend: true });
  await f.chat.submit('cancel me');
  assert.equal(f.chat.busy, false);
  await f.chat.submit('blocked');
  assert.equal(f.chat.busy, false);
  await until(() => f.chat.entries.some(entry => entry.role === 'notice' && entry.text.includes('Credentials missing')));
  assert.equal(f.requests.filter(request => request.path === '/api/messages').length, 2);
});

test('closing chat rejects an undelivered request without cancelling saved work', async t => {
  const f = await fixture(t, { frontend: true });
  await f.chat.submit('cancel me');
  f.availability(false);
  const prompt = f.chat.submit('not delivered yet');
  const rejection = assert.rejects(prompt, /Chat closed/);
  await until(() => f.requests.filter(request => request.path === '/api/messages').length === 2);
  f.chat.close();
  await rejection;
  assert.equal(f.chat.ready, false);
  assert.equal(f.chat.pending.size, 0);
  assert.equal(f.requests.filter(request => request.path === '/api/cancel').length, 0);
});

test('an open chat retries a prompt during a service outage and resumes without duplicate input', async t => {
  const f = await fixture(t, { frontend: true });
  f.availability(false);
  const submitted = f.chat.submit('sent during restart');
  await until(() => f.requests.some(request => request.path === '/api/messages'));
  assert.equal(f.chat.busy, true);
  assert.equal(f.accepted.size, 0);
  await delay(350);
  f.availability(true);
  await submitted;
  assert.equal(f.chat.status, 'Ready');
  assert.equal(f.chat.closed, false);
  assert.equal(f.accepted.size, 1);
  assert.equal(f.chat.entries.filter(entry => entry.role === 'user' && entry.text === 'sent during restart').length, 1);
  const requests = f.requests.filter(request => request.path === '/api/messages');
  assert.ok(requests.length >= 2);
  assert.equal(new Set(requests.map(request => request.body.id)).size, 1);
  assert.equal(f.stderr(), '');
});

test('a lost submission response retries the same durable event ID without creating another input', async t => {
  const f = await fixture(t, { frontend: true });
  f.dropNextSubmission();
  await f.chat.submit('accepted before connection dropped');
  await until(() => f.requests.filter(request => request.path === '/api/messages').length >= 2);
  assert.equal(f.accepted.size, 1);
  const requests = f.requests.filter(request => request.path === '/api/messages');
  assert.equal(new Set(requests.map(request => request.body.id)).size, 1);
  await until(() => f.chat.entries.some(entry => entry.role === 'assistant' && entry.text === 'completed answer'));
  assert.equal(f.chat.entries.filter(entry => entry.role === 'assistant' && entry.text === 'completed answer').length, 1);
  assert.equal(f.chat.status, 'Ready');
});

test('ACP can explicitly cancel saved unfinished work after delivery acknowledgement', async t => {
  const f=await fixture(t);
  f.send(2,'session/new',{cwd:'/workspace',mcpServers:[]});await f.reply(2);
  f.send(3,'session/prompt',{sessionId:'stable-home-session',prompt:[{type:'text',text:'cancel me'}]});
  assert.equal((await f.reply(3)).result.stopReason,'end_turn');
  f.send(4,'session/cancel',{sessionId:'stable-home-session'});
  assert.deepEqual((await f.reply(4)).result,{});
  assert.equal(f.requests.filter(request=>request.path==='/api/cancel').length,1);
});
