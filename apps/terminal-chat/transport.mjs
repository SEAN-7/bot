import { spawn } from 'node:child_process';
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

export async function runACP(adapter) {
  const child = spawn(adapter[0],adapter.slice(1),{env:process.env,stdio:['pipe','pipe','inherit']});
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
