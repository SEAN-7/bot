import { fixtureSource } from './process-fixtures.mjs';
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { initialize } from '../src/releases.mjs';
import { startSupervisor } from '../src/supervisor.mjs';
import { ChatSession } from '../apps/terminal-chat/chat-session.mjs';
import { ChatStore, isNotification } from '../apps/chat/database.mjs';

// Keep the frontend and ACP process alive while replacing the real supervisor.
test('an independently running chat survives supervisor replacement and a prompt during downtime', { timeout: 40_000 }, async t => {
  const home = await mkdtemp(join(tmpdir(), 'bot-chat-restart-'));
  let server, chat;
  t.after(async () => { chat?.close(); await server?.stop(); await rm(home, { recursive: true, force: true }); });
  await initialize(home,fixtureSource({
    modules: { chat: { enabled: true }, 'demo-model': { enabled: true } },
    runtime: { port: 0, sandbox: 'off' }
  }));
  server = await startSupervisor({ home, port: 0, unsafe: true });
  const initial = server;
  const port = Number(new URL(server.url).port);
  const child = spawn(process.execPath, [fileURLToPath(new URL('../src/cli.mjs', import.meta.url)), 'acp', '--home', home, '--no-sandbox'], { stdio: ['pipe', 'pipe', 'pipe'] });
  chat = new ChatSession(child);
  await chat.start(join(home, 'workspace'));
  await chat.submit('before restart');
  for (let i=0;i<100&&!chat.entries.some(m=>m.role==='assistant');i++)await new Promise(resolve=>setTimeout(resolve,50));
  const settled=async()=>{
    for(let i=0;i<400;i++){
      const status=await fetch(new URL('/api/status',server.url),{headers:{authorization:`Bearer ${server.token}`}}).then(r=>r.json());
      if(status.pending===0)return;
      await new Promise(resolve=>setTimeout(resolve,50));
    }
    assert.fail('Chat delivery did not settle');
  };
  await settled();
  await server.stop();
  server = null;
  const during = chat.submit('during restart');
  await new Promise(resolve => setTimeout(resolve, 400));
  assert.equal(chat.busy, true);
  server = await startSupervisor({ home, port, unsafe: true });
  await during;
  await chat.submit('after restart');
  assert.equal(child.pid, chat.child.pid);
  assert.equal(child.exitCode, null);
  assert.equal(chat.closed, false);
  assert.equal(chat.status, 'Ready');
  assert.equal(server.uiToken, initial.uiToken);
  assert.notEqual(server.token, initial.token);
  await settled();
  const response = await fetch(new URL('/api/messages', server.url), { headers: { authorization: `Bearer ${initial.uiToken}` } });
  const { messages } = await response.json();
  const appStore=new ChatStore(join(home,'workspace'));
  for (const text of ['before restart', 'during restart', 'after restart']) {
    assert.equal(messages.filter(m => m.role === 'user' && m.content === text).length, 0);
    assert.equal(appStore.read().messages.filter(m=>m.sender==='human'&&m.text===text).length,1);
    assert.equal(chat.entries.filter(m => m.role === 'user' && m.text === text).length, 1);
  }
  appStore.close();assert.equal(messages.filter(m=>m.role==='user'&&isNotification(m.content)).length,3);
  assert.equal(chat.entries.filter(m => m.role === 'notice').length, 0);
});
