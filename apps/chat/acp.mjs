// ACP displays the chat app's own state and sends reference notifications to the bot.
import { createInterface } from 'node:readline';
import { randomUUID } from 'node:crypto';
import { ChatStore, notification } from './database.mjs';

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
const store = new ChatStore(workspace);
let chatCursor = 0;
const localEcho = new Set();
let workIsChat = true;
function chatMessages(replay=false) {
  let rows;
  do {
    rows=store.allAfter(chatCursor);
    for(const message of rows) {
      if(replay||message.sender!=='human'||!localEcho.has(message.event_id))update({sessionUpdate:message.sender==='human'?'user_message_chunk':'agent_message_chunk',content:{type:'text',text:message.text}});
      localEcho.delete(message.event_id);
      chatCursor=message.id;
    }
  } while(rows.length===500);
}
const reply = (id, result) => write({ jsonrpc: '2.0', id, result });
const failure = (id, code, message) => write({ jsonrpc: '2.0', id, error: { code, message } });
const invalid = message => Object.assign(new Error(message), { code: -32602 });
const update = value => write({ jsonrpc: '2.0', method: 'session/update', params: { sessionId, update: value } });
let initialized = false, attached = false, attaching = false, stopped = false, cursor = 0;
let active = null;
const accepted = new Set();
let lastNotice = '';
let polling = false;
const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
async function api(path, body) {
  let retryDelay = 250;
  const data = body === undefined ? undefined : JSON.stringify(body);
  while (true) {
    try {
      const response = await fetch(new URL(path, base), {
        method: body === undefined ? 'GET' : 'POST',
        headers: { authorization: `Bearer ${token}`, 'content-type': 'application/json' },
        ...(data === undefined ? {} : { body: data }),
        signal: AbortSignal.timeout(5000)
      });
      if ([502, 503, 504].includes(response.status)) {
        await response.body?.cancel();
        throw Object.assign(new Error('Bot service is restarting'), { retryable: true });
      }
      const value = await response.json();
      if (!response.ok) throw new Error(value.error || `Chat transport returned ${response.status}`);
      return value;
    } catch (error) {
      // A lost POST response may follow durable acceptance. Retrying the exact
      // same body/event ID lets the supervisor deduplicate it safely.
      const retryable = error.retryable || error instanceof TypeError || ['AbortError', 'TimeoutError'].includes(error.name);
      if (!retryable || stopped) throw error;
      await wait(retryDelay);
      retryDelay = Math.min(2000, retryDelay * 2);
    }
  }
}

function message(message, replay = false) {
  store.import([message]);
  if((message.role==='user'||message.role==='context')&&message.metadata?.source)workIsChat=message.metadata.source==='chat';
  if (message.role === 'user') {
    return;
  } else if (message.role === 'assistant') {
    if(workIsChat)for (const call of message.toolCalls || []) update({ sessionUpdate: 'tool_call', toolCallId: call.id, title: call.name, kind: 'other', status: 'pending', rawInput: call.arguments });
  } else if (message.role === 'channel' && message.metadata?.module === 'chat') {
    return;
  } else if (message.role === 'tool') {
    if(workIsChat)update({ sessionUpdate: 'tool_call_update', toolCallId: message.toolCallId, status: message.isError ? 'failed' : 'completed', content: [{ type: 'content', content: { type: 'text', text: message.content } }], rawOutput: message.content });
  } else if (message.metadata?.recovery) {
    // Old versions journaled routine restarts. Keep the audit history, but omit
    // these exact benign notices from both replay and live chat.
    if (message.content === 'Recovery report: Supervisor restarted; resumed the durable conversation') return;
    update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: message.content }, _meta: { 'bot/notice': true } });
  }
}
function event(entry) {
  if (entry.kind === 'message') message({...entry.payload,id:entry.seq});
  else if (entry.kind === 'tool.started'&&workIsChat) update({ sessionUpdate: 'tool_call_update', toolCallId: entry.payload.id, status: 'in_progress', rawInput: entry.payload.arguments });
  else if (['input.completed', 'input.delivered', 'input.cancelled'].includes(entry.kind)) accepted.delete(entry.payload?.id);
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
        chatMessages();
        if (accepted.size) {
          const status = await api('/api/status');
          const explanation = ['blocked', 'failed'].includes(status.state)
            ? `The accepted input remains in the durable queue. ${status.error || 'The runtime needs operator attention.'}` : '';
          if (explanation && explanation !== lastNotice) update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: explanation }, _meta: { 'bot/notice': true } });
          lastNotice = explanation;
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
        store.import(snapshot.messages);
        attached = true;
        if (request.method === 'session/new') reply(request.id, { sessionId });
        for (const entry of snapshot.messages) if(entry.metadata?.recovery)message(entry,true);
        chatMessages(true);
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
      const pending = { id: request.id, eventId: 'chat:'+randomUUID() };
      active = pending;
      if (!accepted.size) lastNotice = '';
      accepted.add(pending.eventId);
      localEcho.add(pending.eventId);
      store.accept(pending.eventId,text);
      pending.accepted = api('/api/messages', { id: pending.eventId, text: notification(pending.eventId) }).then(result=>{store.notified(pending.eventId);return result;});
      try { await pending.accepted; }
      catch (error) { accepted.delete(pending.eventId); if (active === pending) active = null; throw error; }
      if (active === pending) active = null;
      reply(request.id, { stopReason: 'end_turn' });
      return; // Delivery acknowledgement; replies arrive independently through the journal.
    }
    if (request.method === 'session/cancel') {
      const pending = active;
      const eventId = pending?.eventId ?? [...accepted].at(-1);
      if (eventId) {
        if (pending) await pending.accepted;
        await api('/api/cancel', { id: eventId });
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
process.on('exit',()=>store.close());
