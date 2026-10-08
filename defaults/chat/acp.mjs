// ACP is only a view and input transport for the supervisor's durable conversation.
import { createInterface } from 'node:readline';
import { randomUUID } from 'node:crypto';

const sessionId = process.env.BOT_CHAT_SESSION;
const token = process.env.BOT_CHAT_TOKEN;
const workspace = process.env.BOT_CHAT_WORKSPACE;
let base;
try {
  base = new URL(process.env.BOT_CHAT_URL);
  if (base.protocol !== 'http:' || !['127.0.0.1', '[::1]'].includes(base.hostname) || base.username || base.password || !sessionId || !token || !workspace) throw new Error('Missing or invalid chat transport configuration');
} catch {
  process.stderr.write('Missing or invalid chat transport configuration\n');
  process.exit(1);
}
const write = value => process.stdout.write(JSON.stringify(value) + '\n');
const reply = (id, result) => write({ jsonrpc: '2.0', id, result });
const failure = (id, code, message) => write({ jsonrpc: '2.0', id, error: { code, message } });
const invalid = message => Object.assign(new Error(message), { code: -32602 });
const update = value => write({ jsonrpc: '2.0', method: 'session/update', params: { sessionId, update: value } });
let initialized = false, attached = false, attaching = false, stopped = false, cursor = 0;
let active = null;
let polling = false;
const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
async function api(path, body) {
  const response = await fetch(new URL(path, base), {
    method: body === undefined ? 'GET' : 'POST',
    headers: { authorization: `Bearer ${token}`, 'content-type': 'application/json' },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    signal: AbortSignal.timeout(5000)
  });
  const value = await response.json();
  if (!response.ok) throw new Error(value.error || `Chat transport returned ${response.status}`);
  return value;
}
function message(message, replay = false) {
  if (message.role === 'user') {
    // Toad renders its own submitted prompt. Replay and autonomous wakes need an echo.
    if (!replay && active?.eventId === message.metadata?.eventId) return;
    update({ sessionUpdate: 'user_message_chunk', content: { type: 'text', text: message.content } });
  } else if (message.role === 'assistant') {
    if (message.content) update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: message.content } });
    for (const call of message.toolCalls || []) update({ sessionUpdate: 'tool_call', toolCallId: call.id, title: call.name, kind: 'other', status: 'pending', rawInput: call.arguments });
  } else if (message.role === 'tool') {
    update({ sessionUpdate: 'tool_call_update', toolCallId: message.toolCallId, status: message.isError ? 'failed' : 'completed', content: [{ type: 'content', content: { type: 'text', text: message.content } }], rawOutput: message.content });
  } else if (message.metadata?.recovery) {
    update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: message.content } });
  }
}
function settle(stopReason) {
  if (!active) return;
  const { id } = active;
  active = null;
  reply(id, { stopReason });
}
function event(entry) {
  if (entry.kind === 'message') message(entry.payload);
  else if (entry.kind === 'tool.started') update({ sessionUpdate: 'tool_call_update', toolCallId: entry.payload.id, status: 'in_progress', rawInput: entry.payload.arguments });
  else if (active?.eventId === entry.payload?.id && entry.kind === 'input.completed') settle('end_turn');
  else if (active?.eventId === entry.payload?.id && entry.kind === 'input.cancelled') settle('cancelled');
}
async function poll() {
  if (polling) return;
  polling = true;
  try {
    while (attached && !stopped) {
      try {
        const { events } = await api(`/api/events?after=${cursor}`);
        if (!Array.isArray(events)) throw new Error('Invalid journal response');
        for (const entry of events) {
          if (!Number.isSafeInteger(entry.seq) || entry.seq <= cursor) continue;
          event(entry);
          cursor = entry.seq;
        }
        if (active) {
          const status = await api('/api/status');
          if (['blocked', 'failed'].includes(status.state)) {
            const explanation = `The accepted input remains in the durable queue. ${status.error || 'The runtime needs operator attention.'}`;
            update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: explanation } });
            const pending = active;
            active = null;
            failure(pending.id, -32000, explanation);
          }
        }
        // Drain full pages without waiting so a large replay cannot lose its tail.
        if (events.length >= 500) continue;
      } catch (error) { process.stderr.write(`Chat transport: ${error.message}\n`); }
      await wait(250);
    }
  } finally { polling = false; }
}
function validateSession(params) {
  if (!attached || params?.sessionId !== sessionId) throw invalid('Unknown session; attach the durable conversation first');
}
async function dispatch(request) {
  const hasId = Object.hasOwn(request, 'id');
  try {
    if (request.jsonrpc !== '2.0' || typeof request.method !== 'string' || (hasId && typeof request.id !== 'string' && typeof request.id !== 'number') || (request.params !== undefined && (!request.params || typeof request.params !== 'object' || Array.isArray(request.params)))) throw Object.assign(new Error('Invalid JSON-RPC request'), { code: -32600 });
    const params = request.params || {};
    if (request.method === 'initialize') {
      if (!hasId || params.protocolVersion !== 1) throw invalid('ACP protocol version 1 is required');
      initialized = true;
      reply(request.id, { protocolVersion: 1, agentCapabilities: { loadSession: true, promptCapabilities: { image: false, audio: false, embeddedContext: false } }, agentInfo: { name: 'durable-bot-chat', version: '1.0.0' }, authMethods: [] });
      return;
    }
    if (!initialized) throw invalid('Initialize ACP before using a session');
    if (request.method === 'session/new' || request.method === 'session/load') {
      if (!hasId) throw invalid('Session attachment requires a request ID');
      if (attached || attaching || active) throw invalid('This transport is already attached');
      if (request.method === 'session/load' && params.sessionId !== sessionId) throw invalid('Unknown session: this bot has one durable conversation');
      if (typeof params.cwd !== 'string' || !Array.isArray(params.mcpServers)) throw invalid('Session setup requires cwd and an MCP server array');
      if (params.mcpServers.length) throw invalid('This chat transport does not install client MCP servers');
      attaching = true;
      try {
        const snapshot = await api('/api/chat-snapshot');
        if (!Array.isArray(snapshot.messages) || !Number.isSafeInteger(snapshot.after)) throw new Error('Invalid chat snapshot');
        cursor = snapshot.after;
        attached = true;
        if (request.method === 'session/new') reply(request.id, { sessionId });
        for (const entry of snapshot.messages) message(entry, true);
        if (request.method === 'session/load') reply(request.id, {});
        void poll();
      } finally { attaching = false; }
      return;
    }
    validateSession(params);
    if (request.method === 'session/prompt') {
      if (!hasId || active) throw invalid('Only one prompt may be outstanding');
      if (!Array.isArray(params.prompt) || !params.prompt.length || params.prompt.some(block => !block || block.type !== 'text' || typeof block.text !== 'string')) throw invalid('Only text prompts are supported; paste resource text into the prompt');
      const text = params.prompt.map(block => block.text).join('\n');
      if (!text.trim() || text.length > 100000) throw invalid('Prompt must contain 1 to 100000 characters');
      const pending = { id: request.id, eventId: randomUUID() };
      active = pending;
      pending.accepted = api('/api/messages', { id: pending.eventId, text });
      try { await pending.accepted; }
      catch (error) { if (active === pending) active = null; throw error; }
      return; // Journal completion settles this request, not enqueue acknowledgement.
    }
    if (request.method === 'session/cancel') {
      const pending = active;
      if (pending) {
        await pending.accepted;
        if (active === pending) await api('/api/cancel', { id: pending.eventId });
      }
      if (hasId) reply(request.id, {});
      return;
    }
    throw Object.assign(new Error(`Unsupported ACP method: ${request.method}`), { code: -32601 });
  } catch (error) {
    if (hasId) failure(request.id, error.code || -32603, error.message);
    else process.stderr.write(`ACP: ${error.message}\n`);
  }
}
const lines = createInterface({ input: process.stdin, crlfDelay: Infinity });
lines.on('line', line => {
  let request;
  try {
    if (line.length > 1000000) throw new Error('Oversized request');
    request = JSON.parse(line);
    if (!request || typeof request !== 'object' || Array.isArray(request)) throw new Error('Invalid request');
  } catch { failure(null, -32700, 'Invalid JSON'); return; }
  void dispatch(request);
});
lines.on('close', () => { stopped = true; attached = false; });
