import { EventEmitter } from 'node:events';
import { createInterface } from 'node:readline';

// This client speaks only chat ACP to the fixed bridge. It never handles agent
// requests for filesystem access, terminals, authentication, or permissions.
export class ChatSession extends EventEmitter {
  constructor(child) {
    super();
    this.child = child;
    this.entries = [];
    this.tools = new Map();
    this.pending = new Map();
    this.nextId = 0;
    this.ready = false;
    this.busy = false;
    this.status = 'Connecting…';
    this.closed = false;
    this.lines = createInterface({ input: child.stdout });
    this.lines.on('line', line => {
      try { this.receive(JSON.parse(line)); }
      catch { this.disconnect(new Error('Invalid chat transport data')); }
    });
    child.stderr.setEncoding('utf8');
    child.stderr.on('data', text => { this.status = text.trim().slice(-500); this.changed(); });
    child.stdin.on('error', error => this.disconnect(error));
    child.once('error', error => this.disconnect(error));
    child.once('close', () => this.disconnect(new Error('Chat disconnected. Reattach with bot chat.')));
  }

  changed() { this.emit('change'); }

  request(method, params, timeout = 0) {
    if (this.closed) return Promise.reject(new Error('Chat is closed'));
    const id = ++this.nextId;
    return new Promise((resolve, reject) => {
      const timer = timeout ? setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`Chat ${method} timed out`));
      }, timeout) : null;
      this.pending.set(id, { resolve, reject, timer });
      this.child.stdin.write(JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n');
    });
  }

  async start(cwd) {
    await this.request('initialize', { protocolVersion: 1, clientCapabilities: {}, clientInfo: { name: 'bot-chat', version: '1.0.0' } }, 10000);
    const result = await this.request('session/new', { cwd, mcpServers: [] }, 10000);
    this.sessionId = result.sessionId;
    this.ready = true;
    this.status = 'Ready';
    this.changed();
  }

  receive(message) {
    if (Object.hasOwn(message, 'id')) {
      const pending = this.pending.get(message.id);
      if (!pending || message.method) return;
      this.pending.delete(message.id);
      clearTimeout(pending.timer);
      if (message.error) pending.reject(new Error(message.error.message));
      else pending.resolve(message.result);
      return;
    }
    if (message.method !== 'session/update' || (this.sessionId && message.params?.sessionId !== this.sessionId)) return;
    const update = message.params?.update;
    if (update?.sessionUpdate === 'user_message_chunk') {
      this.entries.push({ role: 'user', text: update.content.text });
    } else if (update?.sessionUpdate === 'agent_message_chunk') {
      // The durable adapter emits one complete journal message per chunk,
      // including system notices and replies produced while chat is idle.
      this.entries.push({ role: update._meta?.['bot/notice'] === true ? 'notice' : 'assistant', text: update.content.text });
    } else if (['tool_call', 'tool_call_update'].includes(update?.sessionUpdate)) {
      let entry = this.tools.get(update.toolCallId);
      if (!entry) {
        entry = { role: 'tool', id: update.toolCallId, title: 'Tool', status: 'pending', text: '' };
        this.tools.set(entry.id, entry);
        this.entries.push(entry);
      }
      if (update.title) entry.title = update.title;
      if (update.status) entry.status = update.status;
      if (update.rawInput !== undefined) entry.input = update.rawInput;
      if (update.content) entry.text = update.content.map(part => part.content?.text || '').join('\n');
      else if (update.rawOutput !== undefined) entry.text = typeof update.rawOutput === 'string' ? update.rawOutput : JSON.stringify(update.rawOutput, null, 2);
    } else return;
    this.changed();
  }

  async submit(text) {
    if (!this.ready || this.busy || this.closed) throw new Error('Chat is not ready for another message');
    if (!text.trim() || text.length > 100000) throw new Error('Message must contain 1 to 100000 characters');
    this.busy = true;
    this.status = 'Sending…';
    this.entries.push({ role: 'user', text });
    this.changed();
    try {
      const result = await this.request('session/prompt', { sessionId: this.sessionId, prompt: [{ type: 'text', text }] });
      this.status = result.stopReason === 'cancelled' ? 'Cancelled' : 'Ready';
    } catch (error) {
      this.status = error.message;
      this.entries.push({ role: 'notice', text: error.message });
      throw error;
    } finally {
      this.busy = false;
      this.changed();
    }
  }

  cancel() {
    if (!this.busy || this.closed) return;
    this.status = 'Cancelling…';
    this.changed();
    this.child.stdin.write(JSON.stringify({ jsonrpc: '2.0', method: 'session/cancel', params: { sessionId: this.sessionId } }) + '\n');
  }

  disconnect(error) {
    if (this.closed) return;
    this.closed = true;
    this.ready = false;
    this.status = error.message;
    for (const pending of this.pending.values()) { clearTimeout(pending.timer); pending.reject(error); }
    this.pending.clear();
    this.lines.close();
    this.child.kill('SIGTERM');
    this.changed();
  }

  close() { this.disconnect(new Error('Chat closed')); }
}
