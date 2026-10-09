import { fixtureApps, fixtureSource } from './process-fixtures.mjs';
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, readFile, rm, access } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { tmpdir } from 'node:os';
import { initialize, stageDrafts } from '../src/releases.mjs';
import { startSupervisor } from '../src/supervisor.mjs';
import { sandboxAvailable } from '../src/sandbox.mjs';
import { Store } from '../src/store.mjs';

const workingModel = `
export default async function (api) {
  const schema = await api.state.get('schema', 1);
  if (schema !== 1) throw new Error('Incompatible persistent state: ' + schema);
  await api.state.set('schema', 1);
  api.registerModel({ async complete({ messages }) {
    const input = messages.findLast(m => m.role === 'user');
    return { text: 'schema=' + await api.state.get('schema') + '; reply=' + input.content, toolCalls: [] };
  }});
}
`;

const delay = ms => new Promise(resolve => setTimeout(resolve, ms));

async function until(check, explanation, timeout = 10_000) {
  const end = Date.now() + timeout;
  let last;
  while (Date.now() < end) {
    last = await check();
    if (last) return last;
    await delay(40);
  }
  assert.fail(`Timed out: ${explanation}; last result=${JSON.stringify(last)}`);
}

async function exists(path) {
  try { await access(path); return true; } catch { return false; }
}

function messagesOf(value) {
  return Array.isArray(value) ? value : value.messages;
}

async function fixture(t, source = workingModel, options = {}) {
  const directory = await mkdtemp(join(tmpdir(), 'bot-recovery-review-'));
  const defaultsDir = join(directory, 'defaults');
  const modelDir = join(defaultsDir, 'test-model');
  const home = join(directory, 'home');
  await mkdir(modelDir, { recursive: true });
  await writeFile(join(modelDir, 'module.json'), JSON.stringify({
    id: 'test-model', name: 'Recovery test model', version: '1.0.0', apiVersion: 2, entry: 'index.mjs'
  }));
  await writeFile(join(modelDir, 'index.mjs'), source);
  await initialize(home, fixtureSource({
    apiVersion: 2,
    modules: { 'test-model': { enabled: true, ...options.moduleConfig } },
    model: {},
    runtime: {
      port: 0, startupTimeoutMs: 5000, heartbeatTimeoutMs: 5000,
      operationTimeoutMs: 1800, trialMs: 50, maxRestarts: 2, retryMs: 100,
      sandbox: options.protected ? 'auto' : 'off', ...options.runtime
    }
  }), { appsDir: await fixtureApps(defaultsDir) });
  // This custom controller deliberately owns a pinned model dependency. Recovery
  // tests exercise controller replacement, while ordinary CLI apps remain live.
  const meta=JSON.parse(await readFile(join(home,'control/releases.json'),'utf8'));
  const controllerPath=join(home,'revisions',meta.active,'apps','agent','main.mjs');
  const pinned="process.env.BOT_APPS=process.env.BOT_CONTROLLER_APPS;\nif(process.argv[2]==='--preflight')await import('../test-model/main.mjs');\n"+await readFile(controllerPath,'utf8');
  await writeFile(controllerPath,pinned);await writeFile(join(home,'drafts/apps/agent/main.mjs'),pinned);
  if(options.controllerPatch){const meta=JSON.parse(await readFile(join(home,'control','releases.json'),'utf8'));const path=join(home,'revisions',meta.active,'apps','agent','main.mjs');await writeFile(path,options.controllerPatch(await readFile(path,'utf8')));}
  let server;
  const launch = async () => {
    server = await startSupervisor({ home, port: 0, unsafe: !options.protected });
    return server;
  };
  const stop = async () => {
    if (server) { const previous = server; server = undefined; await previous.stop(); }
  };
  t.after(async () => { await stop(); await rm(directory, { recursive: true, force: true }); });
  await launch();
  const api = async (path, body) => {
    const url = new URL(path, server.url);
    const response = await fetch(url, {
      method: body === undefined ? 'GET' : 'POST',
      headers: { authorization: `Bearer ${server.token}`, 'content-type': 'application/json' },
      ...(body === undefined ? {} : { body: JSON.stringify(body) })
    });
    const value = await response.json();
    assert.equal(response.ok, true, `${path}: ${JSON.stringify(value)}`);
    return value;
  };
  const reply = async (content) => until(async () => {
    const history = messagesOf(await api('/api/messages'));
    return history.find(message => message.role === 'assistant' && message.content.includes(content));
  }, `assistant reply containing ${content}`);
  const send = text => api('/api/messages', { text });
  const replace = async source => {
    await writeFile(join(home, 'drafts', 'apps', 'test-model', 'index.mjs'), source);
    await api('/api/update', {});
  };
  const request = (path, body, browser = false) => fetch(new URL(path, server.url), {
    method: 'POST',
    headers: { authorization: `Bearer ${browser ? server.uiToken : server.token}`, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  });
  return { directory, home, api, launch, stop, reply, send, replace, request };
}

test('clean supervisor restarts stay quiet while an interrupted supervisor produces a recovery notice', { timeout: 20_000 }, async t => {
  const bot = await fixture(t);
  await bot.send('retained conversation');
  await bot.reply('reply=retained conversation');
  for (let index = 0; index < 2; index++) {
    await bot.stop();
    await bot.launch();
  }
  let messages = messagesOf(await bot.api('/api/messages'));
  assert.equal(messages.some(message => message.metadata?.recovery), false);
  assert.ok(messages.some(message => message.content.includes('reply=retained conversation')));
  await bot.stop();
  // An abrupt supervisor exit leaves this durable marker false.
  const store = new Store(bot.home);
  store.set('cleanShutdown', false);
  store.close();
  await bot.launch();
  messages = messagesOf(await bot.api('/api/messages'));
  assert.deepEqual(messages.filter(message => message.metadata?.recovery).map(message => message.content), ['Recovery report: Supervisor interrupted; resumed the durable conversation']);
});

test('controller candidate crash restores its state snapshot without losing accepted input', { timeout: 20_000 }, async t => {
  const bot = await fixture(t, workingModel, { runtime: { startupTimeoutMs: 5000, heartbeatTimeoutMs: 5000 } });
  await bot.send('before update');
  await bot.reply('schema=1; reply=before update');
  const before = await bot.api('/api/status');
  await bot.replace(`
    import { writeFile } from 'node:fs/promises';
    import { join } from 'node:path';
    export default api => {
      api.registerModel({ async complete() {
        await api.state.set('schema', 2);
        await writeFile(join(api.paths.workspace, 'candidate-started'), 'yes');
        process.exit(62);
      }});
    };
  `);
  await until(async () => (await bot.api('/api/status')).revision !== before.revision, 'candidate activation');
  await bot.send('accepted during recovery');
  try {
    await until(() => exists(join(bot.home, 'workspace', 'candidate-started')), 'candidate to execute');
  } catch (error) {
    t.diagnostic(JSON.stringify({ status: await bot.api('/api/status'), events: await bot.api('/api/events') }));
    throw error;
  }
  await bot.reply('schema=1; reply=accepted during recovery');
  assert.equal((await bot.api('/api/status')).revision, before.revision);
  const messages = messagesOf(await bot.api('/api/messages'));
  assert.equal(messages.filter(m => m.role === 'user' && m.content === 'accepted during recovery').length, 1);
  assert.ok(messages.some(m => m.role === 'assistant' && m.content.includes('before update')));
});

test('a controller candidate with a blocked event loop is killed and rolled back', { timeout: 20_000 }, async t => {
  const bot = await fixture(t);
  await bot.send('baseline');
  await bot.reply('reply=baseline');
  const before = await bot.api('/api/status');
  await bot.replace(`
    import { writeFile } from 'node:fs/promises';
    import { join } from 'node:path';
    export default api => {
      api.registerModel({ async complete() {
        await writeFile(join(api.paths.workspace, 'candidate-hung'), 'yes');
        while (true) {}
      }});
    };
  `);
  await until(async () => (await bot.api('/api/status')).revision !== before.revision, 'candidate activation');
  await bot.send('after hang');
  await until(() => exists(join(bot.home, 'workspace', 'candidate-hung')), 'candidate to hang');
  await bot.reply('schema=1; reply=after hang');
  assert.equal((await bot.api('/api/status')).revision, before.revision);
});

test('an acknowledged queued message survives supervisor restart and duplicate delivery', { timeout: 20_000 }, async t => {
  const bot = await fixture(t, `
    import { access } from 'node:fs/promises';
    import { join } from 'node:path';
    export default api => {
      api.registerModel({ async complete({ messages }) {
        try { await access(join(api.paths.workspace, 'allow-completion')); }
        catch { await new Promise(() => {}); }
        return { text: 'durable:' + messages.findLast(m => m.role === 'user').content, toolCalls: [] };
      }});
    };
  `);
  const input = { id: 'stable-user-event', text: 'retain me' };
  await bot.api('/api/messages', input);
  await bot.stop();
  await writeFile(join(bot.home, 'workspace', 'allow-completion'), 'yes');
  await bot.launch();
  await bot.api('/api/messages', input);
  await bot.reply('durable:retain me');
  const messages = messagesOf(await bot.api('/api/messages'));
  assert.equal(messages.filter(m => m.role === 'user' && m.content === input.text).length, 1);
  assert.equal(messages.filter(m => m.role === 'assistant' && m.content === 'durable:retain me').length, 1);
});

test('a started external action becomes unknown after a crash and is not replayed', { timeout: 20_000 }, async t => {
  const bot = await fixture(t, `
    import { appendFile } from 'node:fs/promises';
    import { join } from 'node:path';
    export default api => {
      api.registerModel({ async complete({ messages }) {
        const result = messages.findLast(m => m.role === 'tool');
        return result ? { text: 'observed:' + result.content, toolCalls: [] } :
          { text: '', toolCalls: [{ id: 'review-action-1', name: 'shell', arguments: { command: "printf 'effect\\n' >> effects; kill -9 $PPID" } }] };
      }});
    };
  `);
  await bot.send('perform simulated effect');
  await bot.reply('outcome UNKNOWN');
  assert.equal(await readFile(join(bot.home, 'workspace', 'effects'), 'utf8'), 'effect\n');
  const value = await bot.api('/api/events');
  const events = Array.isArray(value) ? value : value.events;
  assert.equal(events.filter(event => event.kind === 'tool.started' && event.payload.name === 'shell').length, 1);
});

test('guardian kills a tracked shell group when the controller exits during a tool',{timeout:20000},async t=>{
  const command=`${JSON.stringify(process.execPath)} -e "require('node:fs').writeFileSync('shell-pid',String(process.pid));setInterval(()=>{},1000)"`;
  const bot=await fixture(t,`export default api=>api.registerModel({async complete({messages}){return messages.some(m=>m.role==='tool')?{text:'shell recovery complete',toolCalls:[]}:{text:'',toolCalls:[{id:'owned-shell-action',name:'shell',arguments:{command:${JSON.stringify(command)}}}]};}});`,{controllerPatch:source=>"import {writeFileSync} from 'node:fs';\nwriteFileSync(process.env.BOT_WORKSPACE+'/controller-pid',String(process.pid));\n"+source});
  await bot.send('start shell then crash controller');await until(()=>exists(join(bot.home,'workspace','shell-pid')),'shell subprocess to start');
  const pid=Number(await readFile(join(bot.home,'workspace','shell-pid'),'utf8')),controllerPid=Number(await readFile(join(bot.home,'workspace','controller-pid'),'utf8'));
  t.after(()=>{try{process.kill(pid,'SIGKILL');}catch{}});process.kill(controllerPid,'SIGKILL');
  await bot.reply('shell recovery complete');await until(()=>{try{process.kill(pid,0);return false;}catch(error){return error.code==='ESRCH';}},'tracked shell cleanup',5000);
});

test('idle does not call the model, and temporary model failures retain the event for retry', { timeout: 20_000 }, async t => {
  const bot = await fixture(t, `
    import { appendFile } from 'node:fs/promises';
    import { join } from 'node:path';
    export default api => {
      api.registerModel({ async complete({ messages }) {
        await appendFile(join(api.paths.workspace, 'model-calls'), 'call\\n');
        const attempts = await api.state.get('attempts', 0) + 1;
        await api.state.set('attempts', attempts);
        if (attempts < 3) throw Object.assign(new Error('fetch failed: temporary connection reset'), { code: 'ECONNRESET', retryable: true });
        return { text: 'network recovered:' + messages.findLast(m => m.role === 'user').content, toolCalls: [] };
      }});
    };
  `);
  const before = await bot.api('/api/status');
  await delay(400);
  assert.equal(await exists(join(bot.home, 'workspace', 'model-calls')), false);
  await bot.send('keep this event');
  await bot.reply('network recovered:keep this event');
  assert.equal((await bot.api('/api/status')).revision, before.revision);
  assert.equal(await readFile(join(bot.home, 'workspace', 'model-calls'), 'utf8'), 'call\ncall\ncall\n');
  const messages = messagesOf(await bot.api('/api/messages'));
  assert.equal(messages.filter(m => m.role === 'user' && m.content === 'keep this event').length, 1);
});

test('a delayed retry cannot be overtaken by a later input in the same conversation', { timeout: 20_000 }, async t => {
  const bot = await fixture(t, `
    export default api => {
      api.registerModel({ async complete({ messages }) {
        const attempts = await api.state.get('attempts', 0) + 1;
        await api.state.set('attempts', attempts);
        if (attempts === 1) throw Object.assign(new Error('fetch failed'), { code: 'ECONNRESET', retryable: true });
        return { text: 'answered:' + messages.findLast(m => m.role === 'user').content, toolCalls: [] };
      }});
    };
  `, { runtime: { retryMs: 600 } });
  await bot.send('first');
  await bot.send('second');
  await bot.reply('answered:second');
  const replies = messagesOf(await bot.api('/api/messages')).filter(message => message.role === 'assistant').map(message => message.content);
  assert.deepEqual(replies, ['answered:first', 'answered:second']);
});

test('blocked work is retained without preventing a later question from being answered', { timeout: 20_000 }, async t => {
  const bot = await fixture(t, `
    export default api => {
      api.registerModel({ async complete({ messages }) {
        const input = messages.findLast(m => m.role === 'user');
        if (input.content === 'first') throw Object.assign(new Error('Model output limit reached'), { blocked: true });
        return { text: 'answered:' + input.content, toolCalls: [] };
      }});
    };
  `);
  const before = await bot.api('/api/status');
  await bot.send('first');
  await until(async () => (await bot.api('/api/status')).state === 'blocked', 'blocked first input');
  await bot.send('second');
  await bot.reply('answered:second');
  const status = await bot.api('/api/status');
  assert.equal(status.pending, 1);
  assert.equal(status.revision, before.revision);
  const events = (await bot.api('/api/events')).events;
  assert.equal(events.filter(e => e.kind === 'model.unavailable').length, 1);
  assert.equal(events.filter(e => e.kind === 'runtime.ready').length, 1);
});

test('temporary model failure during context preparation retries without restarting the runtime', { timeout: 20_000 }, async t => {
  const bot = await fixture(t, `
    export default api => {
      api.registerModel({ async complete() {
        if (!await api.state.get('contextFailed',false)) {
          await api.state.set('contextFailed',true);
          throw Object.assign(new Error('summary provider temporarily unavailable'), { retryable: true });
        }
        return { text: 'context recovered', toolCalls: [] };
      }});
      api.registerContext(async request => {
        await request.model.complete(request);
        return { messages: request.messages };
      });
    };
  `);
  await bot.send('requires context');
  await bot.reply('context recovered');
});

test('a controller crash after committing the final response does not repeat completed input',{timeout:20000},async t=>{
  const bot=await fixture(t,`export default api=>api.registerModel({async complete({messages}){return {text:'done:'+messages.findLast(m=>m.role==='user').content,toolCalls:[]};}});`,{controllerPatch:source=>"import {existsSync,writeFileSync} from 'node:fs';\n"+source.replace("committed=true;}\n        else await rpc('append'", "committed=true;if(!existsSync(process.env.BOT_WORKSPACE+'/committed-crash')){writeFileSync(process.env.BOT_WORKSPACE+'/committed-crash','yes');process.exit(63);}}\n        else await rpc('append'")});
  await bot.send('first');await bot.reply('done:first');await bot.send('second');await bot.reply('done:second');
  assert.deepEqual(messagesOf(await bot.api('/api/messages')).filter(m=>m.role==='assistant').map(m=>m.content),['done:first','done:second']);
});

test('editable draft config cannot change the protected core or watchdog limits', { timeout: 20_000 }, async t => {
  const bot = await fixture(t);
  await bot.stop();
  const configPath = join(bot.home, 'drafts', 'config.json');
  const original = JSON.parse(await readFile(configPath, 'utf8'));
  const tampered = structuredClone(original);
  tampered.coreRoot = join(bot.home, 'workspace', 'replacement-core');
  tampered.runtime.operationTimeoutMs = 1e12;
  tampered.runtime.heartbeatTimeoutMs = 1e12;
  tampered.runtime.sandbox = 'off';
  await writeFile(configPath, JSON.stringify(tampered));
  let revision;
  try { revision = await stageDrafts(bot.home); }
  catch (error) {
    // Rejecting the edit is also valid, but an unrelated staging error is not.
    assert.match(error.message, /core|runtime|watchdog|protected|operator/i);
    return;
  }
  const candidate = JSON.parse(await readFile(join(bot.home, 'revisions', revision, 'config.json'), 'utf8'));
  assert.equal(candidate.coreRoot, original.coreRoot);
  assert.deepEqual(candidate.runtime, original.runtime);
});

test('protected runtime can edit apps but cannot write core, journal control, or saved controller dependencies', { timeout: 20_000 }, async t => {
  if (!await sandboxAvailable()) { t.skip('OS sandbox is not installed'); return; }
  const coreProbe = join(dirname(dirname(fileURLToPath(import.meta.url))), 'src', `.recovery-review-${process.pid}`);
  t.after(() => rm(coreProbe, { force: true }));
  const bot = await fixture(t, `
    import { writeFile, readFile } from 'node:fs/promises';
    import { join, dirname } from 'node:path';
    export default api => {
      api.registerModel({ async complete() {
        const result = {};
        const paths = {
          core: api.config.coreProbe,
          control: join(dirname(api.paths.workspace), 'control', 'forbidden-write'),
          revision: join(api.paths.module, 'forbidden-write'),
          workspace: join(api.paths.workspace, 'allowed-write'),
          drafts: join(api.paths.drafts, 'allowed-write')
        };
        for (const [name, path] of Object.entries(paths)) {
          try { await writeFile(path, 'probe'); result[name] = 'allowed'; }
          catch (error) { result[name] = error.code; }
        }
        try {
          await readFile(join(dirname(api.paths.workspace), 'control', 'review-secret'), 'utf8');
          result.controlRead = 'allowed';
        } catch (error) { result.controlRead = error.code; }
        if (process.platform === 'darwin') {
          // Signal zero checks permission without delivering a signal to the guardian.
          try { process.kill(api.config.guardianPid, 0); result.guardianSignal = 'allowed'; }
          catch (error) { result.guardianSignal = error.code; }
        }
        return { text: 'protection:' + JSON.stringify(result), toolCalls: [] };
      }});
    };
  `, { protected: true, moduleConfig: { coreProbe, guardianPid: process.pid } });
  await writeFile(join(bot.home, 'control', 'review-secret'), 'parent-only');
  await bot.send('probe protection');
  const reply = await bot.reply('protection:');
  const result = JSON.parse(reply.content.slice('protection:'.length));
  for (const key of ['core', 'revision']) {
    const blocked=process.platform==='linux'?['EROFS']:['EPERM','EACCES'];
    assert.ok(blocked.includes(result[key]), `${key} was not blocked: ${result[key]}`);
  }
  if(process.platform==='linux') {
    // Bubblewrap covers control with a private tmpfs. Writes there must never
    // affect the guardian's host files, even if the private mask is writable.
    assert.ok(['allowed','EACCES'].includes(result.control), `unexpected masked control write: ${result.control}`);
    assert.equal(await readFile(join(bot.home,'control','review-secret'),'utf8'),'parent-only');
    assert.equal(await exists(join(bot.home,'control','forbidden-write')),false);
    assert.equal(result.controlRead,'ENOENT');
  } else {
    assert.ok(['EPERM','EACCES'].includes(result.control), `control was not blocked: ${result.control}`);
    assert.ok(['EPERM','EACCES'].includes(result.controlRead), `controlRead was not blocked: ${result.controlRead}`);
  }
  if (process.platform === 'darwin') {
    assert.ok(['EPERM', 'EACCES'].includes(result.guardianSignal), `guardian signal permission was not blocked: ${result.guardianSignal}`);
  }
  assert.equal(result.workspace, 'allowed');
  assert.equal(result.drafts, 'allowed');
  assert.equal((await bot.api('/api/status')).protected, true);
});

for (const [name, source, reason] of [
  ['invalid import', "import './missing-entry.mjs'; export default () => {};", /missing-entry|Cannot find/],
  ['invalid startup', `export default api => {throw new Error('Invalid fixture configuration')};`, /Invalid fixture configuration/]
]) {
  test(`preflight rejects ${name} while the working version keeps serving`, { timeout: 20_000 }, async t => {
    const bot = await fixture(t, workingModel, { runtime: { startupTimeoutMs: 5000, heartbeatTimeoutMs: 5000 } });
    await bot.send('baseline');
    await bot.reply('reply=baseline');
    const before = await bot.api('/api/status');
    await bot.replace(source);
    await until(async () => {
      const { events } = await bot.api('/api/events');
      return events.find(event => event.kind === 'update.rejected');
    }, 'preflight rejection');
    const status = await bot.api('/api/status');
    assert.equal(status.revision, before.revision);
    assert.match(status.error, reason);
    const { events } = await bot.api('/api/events');
    assert.equal(events.some(event => event.kind === 'update.trial'), false);
    await bot.send('after rejected update');
    await bot.reply('schema=1; reply=after rejected update');
  });
}

test('browser credentials cannot stage updates and operator credentials can queue them', { timeout: 20_000 }, async t => {
  const bot = await fixture(t, workingModel, { runtime: { startupTimeoutMs: 5000, heartbeatTimeoutMs: 5000 } });
  await bot.send('baseline');
  await bot.reply('reply=baseline');
  const before = await bot.api('/api/status');
  for (const path of ['/api/update', '/api/core-update']) {
    const response = await bot.request(path, {}, true);
    assert.equal(response.status, 403);
    assert.match((await response.json()).error, /operator/);
  }
  assert.equal((await bot.api('/api/status')).revision, before.revision);
  const response = await bot.request('/api/update', {});
  assert.equal(response.status, 202);
  assert.equal((await response.json()).queued, true);
});

test('cancelling queued input preserves active work and cancelling active tools records unknown without replay', { timeout: 25_000 }, async t => {
  const bot = await fixture(t, `
    import { appendFile } from 'node:fs/promises';
    import { join } from 'node:path';
    export default api => {
      api.registerModel({ async complete({ messages }) {
        const latest = messages.findLast(message => message.role === 'user');
        if (latest.content === 'active cancelled input') return { text: '', toolCalls: [{ id: 'cancelled-effect', name: 'shell', arguments: {command: "printf 'effect\\n' >> cancel-effects; sleep 100"} }] };
        const outcome = messages.findLast(message => message.role === 'tool');
        return { text: 'next input:' + latest.content + '; outcome=' + (outcome?.content || 'none'), toolCalls: [] };
      }});
    };
  `, { runtime: { startupTimeoutMs: 5000, heartbeatTimeoutMs: 5000, operationTimeoutMs: 10000 } });
  const active = await bot.api('/api/messages', { id: 'active-cancel-test', text: 'active cancelled input' });
  await until(async () => (await bot.api('/api/events')).events.some(event => event.kind === 'tool.started' && event.payload.id === 'cancelled-effect'), 'active tool start');
  const queued = await bot.api('/api/messages', { id: 'pending-cancel-test', text: 'queued cancelled input' });
  const before = (await bot.api('/api/events')).events;
  assert.deepEqual(await bot.api('/api/cancel', { id: queued.id }), { id: queued.id, cancelled: true });
  await delay(150);
  const afterPending = (await bot.api('/api/events')).events;
  assert.equal((await bot.api('/api/status')).state, 'working');
  assert.equal(afterPending.filter(event => event.kind === 'runtime.ready').length, before.filter(event => event.kind === 'runtime.ready').length);
  assert.equal(afterPending.some(event => event.kind === 'runtime.recovered'), false);
  assert.deepEqual(await bot.api('/api/cancel', { id: active.id }), { id: active.id, cancelled: true });
  await bot.send('following cancellation');
  await bot.reply('next input:following cancellation; outcome=Recovery: outcome UNKNOWN');
  await bot.stop();
  await bot.launch();
  await bot.send('after restart');
  await bot.reply('next input:after restart; outcome=Recovery: outcome UNKNOWN');
  assert.equal(await readFile(join(bot.home, 'workspace', 'cancel-effects'), 'utf8'), 'effect\n');
  const { events } = await bot.api('/api/events');
  assert.equal(events.filter(event => event.kind === 'tool.started' && event.payload.id === 'cancelled-effect').length, 1);
  assert.equal(events.filter(event => event.kind === 'input.cancelled').length, 2);
  const messages = messagesOf(await bot.api('/api/messages'));
  assert.equal(messages.filter(message => message.role === 'user' && message.content === 'active cancelled input').length, 1);
  assert.equal(messages.some(message => message.role === 'user' && message.content === 'queued cancelled input'), false);
});

test('shell sessions survive idle and turn boundaries, then stop with the supervisor', { timeout: 20_000 }, async t => {
  const executionURL = pathToFileURL(join(dirname(dirname(fileURLToPath(import.meta.url))), 'src', 'execution.mjs')).href;
  const bot = await fixture(t, `
    import execution from ${JSON.stringify(executionURL)};
    export default api => {

      api.registerModel({ async complete({ messages }) {
        const user = messages.findLast(m => m.role === 'user');
        const later = messages.filter(m => m.id > user.id);
        if (later.some(m => m.role === 'tool')) {
          const result = JSON.parse(later.findLast(m => m.role === 'tool').content);
          return { text: result.outcome === 'running' ? 'yielded session started' : 'session finished: ' + result.stdout, toolCalls: [] };
        }
        if (user.content === 'poll session') {
          const previous = messages.findLast(m => m.role === 'tool' && m.toolName === 'shell');
          return { text: '', toolCalls: [{ id: 'poll-' + user.id, name: 'shell', arguments: { sessionId: JSON.parse(previous.content).sessionId, chars: 'hello\\n' } }] };
        }
        return { text: '', toolCalls: [{ id: 'start-' + user.id, name: 'shell', arguments: { command: 'printf "%s" $$ > yielded-pid; read line; printf "reply:%s" "$line"', yieldTimeMs: 0 } }] };
      }});
    };
  `);
  await bot.send('start session');
  await bot.reply('yielded session started');
  await until(() => exists(join(bot.home, 'workspace', 'yielded-pid')), 'yielded subprocess to start');
  const pid = Number(await readFile(join(bot.home, 'workspace', 'yielded-pid'), 'utf8'));
  // Longer than this fixture's operation deadline: only individual tool waits are bounded.
  await delay(2000);
  assert.doesNotThrow(() => process.kill(pid, 0));
  assert.equal((await bot.api('/api/status')).state, 'idle');
  await bot.send('poll session');
  await bot.reply('session finished: reply:hello');
  await bot.send('start another session');
  await until(async () => Number(await readFile(join(bot.home, 'workspace', 'yielded-pid'), 'utf8')) !== pid, 'second session to start');
  const secondPid = Number(await readFile(join(bot.home, 'workspace', 'yielded-pid'), 'utf8'));
  await bot.stop();
  await until(() => {
    try { process.kill(secondPid, 0); return false; } catch (error) { return error.code === 'ESRCH'; }
  }, 'yielded subprocess to be terminated by guardian');
});

test('chat credentials survive service restart while operator credentials rotate and remain restricted', { timeout: 20_000 }, async t => {
  const bot = await fixture(t);
  const original = JSON.parse(await readFile(join(bot.home, 'control/endpoint.json'), 'utf8'));
  await bot.stop();
  const next = await bot.launch();
  assert.equal(next.uiToken, original.uiToken);
  assert.notEqual(next.token, original.token);
  const request = (path, token, body) => fetch(new URL(path, next.url), {
    method: body === undefined ? 'GET' : 'POST',
    headers: { authorization: `Bearer ${token}`, 'content-type': 'application/json' },
    ...(body === undefined ? {} : { body: JSON.stringify(body) })
  });
  assert.equal((await request('/api/status', original.token)).status, 401);
  assert.equal((await request('/api/status', original.uiToken)).status, 200);
  assert.equal((await request('/api/update', original.uiToken, {})).status, 403);
  assert.equal((await request('/api/core-update', original.uiToken, {})).status, 403);
  const input = { id: 'chat-across-service-restart', text: 'still connected' };
  assert.equal((await request('/api/messages', original.uiToken, input)).status, 202);
  assert.equal((await request('/api/messages', original.uiToken, input)).status, 202);
  await bot.reply('reply=still connected');
  assert.equal(messagesOf(await bot.api('/api/messages')).filter(m => m.role === 'user' && m.content === input.text).length, 1);
});

test('migrated open-chat credentials retain chat-only access, and invalid credentials fail startup cleanly', { timeout: 20_000 }, async t => {
  const bot = await fixture(t);
  const path = join(bot.home, 'control/chat-credentials.json');
  const original = JSON.parse(await readFile(path, 'utf8'));
  await bot.stop();
  await writeFile(path, JSON.stringify({ token: 'a'.repeat(64), legacyTokens: [original.token] }));
  const next = await bot.launch();
  const request = (method, path) => fetch(new URL(path, next.url), {
    method, headers: { authorization: `Bearer ${original.token}`, 'content-type': 'application/json' },
    ...(method === 'POST' ? { body: '{}' } : {})
  });
  assert.equal((await request('GET', '/api/status')).status, 200);
  assert.equal((await request('POST', '/api/update')).status, 403);
  await bot.stop();
  await writeFile(path, JSON.stringify({ token: 'invalid', legacyTokens: [] }));
  await assert.rejects(bot.launch(), /Invalid persistent chat credentials/);
  await writeFile(path, JSON.stringify(original));
  await bot.launch();
  assert.equal((await bot.api('/api/status')).error, null);
});
