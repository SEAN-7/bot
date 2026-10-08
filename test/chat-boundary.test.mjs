import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { filterACPOutput, shellQuote } from '../src/chat-client.mjs';

test('ACP requests cannot use a pending reply ID to invoke host filesystem or terminals', () => {
  const outstanding = new Map([[1, 'initialize']]);
  for (const method of ['terminal/create', 'fs/read_text_file', 'fs/write_text_file', 'session/request_permission']) {
    assert.equal(filterACPOutput({ jsonrpc: '2.0', id: 1, method, params: { command: 'untrusted command' } }, outstanding), null);
    assert.equal(outstanding.get(1), 'initialize');
    assert.equal(filterACPOutput({ jsonrpc: '2.0', method, params: {} }, outstanding), null);
  }
  assert.equal(filterACPOutput({ jsonrpc: '2.0', id: 99, result: {} }, outstanding), null);
  assert.equal(filterACPOutput({ jsonrpc: '2.0', id: 1 }, outstanding), null);
  assert.equal(filterACPOutput({ jsonrpc: '2.0', id: 1, result: {}, error: {} }, outstanding), null);
  assert.equal(outstanding.size, 1);
});

test('ACP initialization strips executable auth metadata and unneeded capabilities', () => {
  const outstanding = new Map([[1, 'initialize']]);
  const reply = filterACPOutput({ jsonrpc: '2.0', id: 1, result: {
    protocolVersion: 999,
    authMethods: [{ id: 'unsafe', name: 'Run login', _meta: { command: 'unsafe command' } }],
    agentCapabilities: { loadSession: true, terminal: true, promptCapabilities: { image: true } },
    agentInfo: { name: 'unsafe', _meta: { command: 'unsafe command' } },
    _meta: { command: 'unsafe command' }
  } }, outstanding);
  assert.equal(reply.result.protocolVersion, 1);
  assert.deepEqual(reply.result.authMethods, []);
  assert.deepEqual(reply.result.agentCapabilities, { loadSession: true, promptCapabilities: { image: false, audio: false, embeddedContext: false } });
  assert.equal(JSON.stringify(reply).includes('unsafe'), false);
  assert.equal(outstanding.size, 0);
});

test('ACP allows matched chat replies and required notifications only', () => {
  const outstanding = new Map([[2, 'session/new'], [3, 'session/prompt'], [4, 'session/load']]);
  assert.deepEqual(filterACPOutput({ jsonrpc: '2.0', id: 2, result: { sessionId: 'durable', modes: { unsafe: true }, _meta: { unsafe: true } } }, outstanding), { jsonrpc: '2.0', id: 2, result: { sessionId: 'durable' } });
  assert.deepEqual(filterACPOutput({ jsonrpc: '2.0', id: 3, result: { stopReason: 'cancelled', _meta: { unsafe: true } } }, outstanding), { jsonrpc: '2.0', id: 3, result: { stopReason: 'cancelled' } });
  assert.deepEqual(filterACPOutput({ jsonrpc: '2.0', id: 4, error: { code: -32000, message: 'Blocked', data: { command: 'unsafe' } } }, outstanding), { jsonrpc: '2.0', id: 4, error: { code: -32000, message: 'Blocked' } });
  const message = { jsonrpc: '2.0', method: 'session/update', params: { sessionId: 'durable', update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'reply' } } } };
  assert.deepEqual(filterACPOutput(message, outstanding), message);
  assert.equal(filterACPOutput({ ...message, params: { ...message.params, update: { sessionUpdate: 'available_commands_update', availableCommands: [{ name: 'unsafe' }] } } }, outstanding), null);
});

test('shell quoting preserves a path containing quotes and shell metacharacters', () => {
  const path = "/tmp/space and 'quote'; $(printf INJECTED) `printf INJECTED` $HOME\nnext";
  const received = execFileSync('/bin/sh', ['-c', `printf '%s' ${shellQuote(path)}`], { encoding: 'utf8' });
  assert.equal(received, path);
});
