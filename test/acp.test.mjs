import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';
import { once } from 'node:events';
import { fileURLToPath } from 'node:url';

const adapter = fileURLToPath(new URL('../defaults/chat/acp.mjs', import.meta.url));
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(check) {
  for (let index = 0; index < 150; index++) {
    const value = check();
    if (value) return value;
    await delay(20);
  }
  assert.fail('Timed out waiting for ACP message');
}
async function fixture(t) {
  const events = [], requests = [], output = [];
  let seq = 2, state = 'idle';
  const append = (kind, payload) => events.push({ seq: ++seq, kind, payload });
  const server = createServer(async (request, response) => {
    let text = '';
    for await (const chunk of request) text += chunk;
    const body = text ? JSON.parse(text) : null;
    requests.push({ path: request.url, authorization: request.headers.authorization, body });
    response.setHeader('content-type', 'application/json');
    if (request.headers.authorization !== 'Bearer scoped-chat-token') { response.statusCode = 401; response.end('{}'); return; }
    const url = new URL(request.url, 'http://127.0.0.1');
    let value;
    if (url.pathname === '/api/chat-snapshot') value = { after: 2, messages: [{ id: 1, role: 'user', content: 'old question' }, { id: 2, role: 'assistant', content: 'old answer' }] };
    else if (url.pathname === '/api/events') value = { events: events.filter(event => event.seq > Number(url.searchParams.get('after'))).slice(0, 500) };
    else if (url.pathname === '/api/status') value = { state, error: state === 'blocked' ? 'Credentials missing' : null };
    else if (url.pathname === '/api/messages') {
      append('message', { role: 'user', content: body.text, metadata: { eventId: body.id } });
      if (body.text === 'blocked') state = 'blocked';
      else if (body.text !== 'cancel me') {
        append('message', { role: 'assistant', content: '', toolCalls: [{ id: 'call-1', name: 'shell', arguments: { command: 'pwd' } }] });
        append('tool.started', { id: 'call-1', name: 'shell', arguments: { command: 'pwd' } });
        append('message', { role: 'tool', toolCallId: 'call-1', content: '/workspace', isError: false });
        append('message', { role: 'assistant', content: 'completed answer' });
        append('input.completed', { id: body.id });
      }
      value = { id: body.id, duplicate: false };
    } else if (url.pathname === '/api/cancel') {
      append('input.cancelled', { id: body.id });
      value = { id: body.id, cancelled: true };
    } else { response.statusCode = 404; value = { error: 'Unknown endpoint' }; }
    response.end(JSON.stringify(value));
  });
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  const child = spawn(process.execPath, [adapter], { env: { ...process.env, BOT_CHAT_URL: `http://127.0.0.1:${server.address().port}`, BOT_CHAT_TOKEN: 'scoped-chat-token', BOT_CHAT_SESSION: 'stable-home-session', BOT_CHAT_WORKSPACE: '/workspace' }, stdio: ['pipe', 'pipe', 'pipe'] });
  let stderr = '';
  child.stderr.on('data', chunk => { stderr += chunk; });
  createInterface({ input: child.stdout }).on('line', line => output.push(JSON.parse(line)));
  t.after(async () => {
    const exited = child.exitCode !== null || child.signalCode !== null;
    if (!exited) { const closed = once(child, 'exit'); child.kill('SIGTERM'); await closed; }
    await new Promise(resolve => server.close(resolve));
  });
  const send = (id, method, params) => child.stdin.write(JSON.stringify({ jsonrpc: '2.0', ...(id === undefined ? {} : { id }), method, params }) + '\n');
  const reply = id => until(() => output.find(message => message.id === id));
  const updates = () => output.filter(message => message.method === 'session/update').map(message => message.params.update);
  send(1, 'initialize', { protocolVersion: 1 });
  assert.equal((await reply(1)).result.protocolVersion, 1);
  return { send, reply, updates, output, requests, append, stderr: () => stderr };
}

test('ACP attaches one conversation, replays after new response, reports tools and idle replies', async t => {
  const f = await fixture(t);
  f.send(2, 'session/new', { cwd: '/workspace', mcpServers: [] });
  assert.equal((await f.reply(2)).result.sessionId, 'stable-home-session');
  await until(() => f.updates().some(update => update.content?.text === 'old answer'));
  assert.ok(f.output.findIndex(message => message.id === 2) < f.output.findIndex(message => message.method === 'session/update'));
  f.send(3, 'session/prompt', { sessionId: 'stable-home-session', prompt: [{ type: 'text', text: 'do work' }] });
  assert.equal((await f.reply(3)).result.stopReason, 'end_turn');
  assert.deepEqual(f.updates().filter(update => update.toolCallId === 'call-1').map(update => update.status), ['pending', 'in_progress', 'completed']);
  assert.equal(f.updates().filter(update => update.content?.text === 'do work').length, 0);
  f.append('message', { role: 'assistant', content: 'autonomous idle reply' });
  await until(() => f.updates().some(update => update.content?.text === 'autonomous idle reply'));
  assert.equal(f.requests.filter(request => request.path === '/api/messages').length, 1);
  assert.ok(f.requests.every(request => request.authorization === 'Bearer scoped-chat-token'));
  assert.ok(f.requests.every(request => !request.path.includes('update')));
  assert.equal(f.stderr(), '');
});

test('ACP load replays before response and cancellation waits for the durable cancel event', async t => {
  const f = await fixture(t);
  f.send(2, 'session/load', { sessionId: 'stable-home-session', cwd: '/workspace', mcpServers: [] });
  await f.reply(2);
  assert.ok(f.output.findIndex(message => message.method === 'session/update') < f.output.findIndex(message => message.id === 2));
  f.send(3, 'session/prompt', { sessionId: 'stable-home-session', prompt: [{ type: 'text', text: 'cancel me' }] });
  await until(() => f.requests.some(request => request.path === '/api/messages'));
  f.send(4, 'session/prompt', { sessionId: 'stable-home-session', prompt: [{ type: 'text', text: 'overlap' }] });
  assert.equal((await f.reply(4)).error.code, -32602);
  assert.equal(f.output.some(message => message.id === 3), false);
  f.send(undefined, 'session/cancel', { sessionId: 'stable-home-session' });
  assert.equal((await f.reply(3)).result.stopReason, 'cancelled');
  assert.equal(f.requests.filter(request => request.path === '/api/cancel').length, 1);
  f.send(5, 'session/prompt', { sessionId: 'stable-home-session', prompt: [{ type: 'resource_link', uri: 'file:///secret' }] });
  assert.match((await f.reply(5)).error.message, /Only text/);
});

test('ACP blocked model explains retained input and completes prompt with an error', async t => {
  const f = await fixture(t);
  f.send(2, 'session/new', { cwd: '/workspace', mcpServers: [] });
  await f.reply(2);
  f.send(3, 'session/prompt', { sessionId: 'stable-home-session', prompt: [{ type: 'text', text: 'blocked' }] });
  assert.match((await f.reply(3)).error.message, /remains in the durable queue.*Credentials missing/);
  assert.ok(f.updates().some(update => update.content?.text.includes('Credentials missing')));
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
