// Explicit opt-in: isolated homes, installed code, stub model, no live inbox traffic.
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, chmod, chown, writeFile, readFile, readdir, symlink, rm } from 'node:fs/promises';
import { execFile, spawn } from 'node:child_process';
import { setTimeout as delay } from 'node:timers/promises';
import { promisify } from 'node:util';
import { pathToFileURL } from 'node:url';
const exec = promisify(execFile);
const root = process.env.FILE_HARNESS_ROOT;
const enabled = process.platform === 'linux' && process.getuid?.() === 0 && !!root && process.env.FILE_HARNESS_TEST_USER === 'bot-next';

test('installed host: isolated UID, protected journal/control, no secrets, full output, cgroup timeout, snapshot', { skip: !enabled }, async t => {
  const { Host } = await import(pathToFileURL(`${root}/harness/host.mjs`));
  const uid = Number((await exec('id', ['-u', 'bot-next'])).stdout.trim());
  const gid = Number((await exec('id', ['-g', 'bot-next'])).stdout.trim());
  const base = await mkdtemp('/tmp/bot-next-isolated-');
  t.after(() => rm(base, { recursive: true, force: true }));
  await chmod(base, 0o755);
  const home = `${base}/home`, state = `${base}/host`;
  await mkdir(home); await chmod(home, 0o700); await chown(home, uid, gid);
  await mkdir(state, { mode: 0o755 });
  for (const name of ['inbox', 'jobs']) { await mkdir(`${home}/${name}`, { mode: 0o700 }); await chown(`${home}/${name}`, uid, gid); }
  await writeFile(`${home}/README.md`, '# Isolated fixture\n'); await chown(`${home}/README.md`, uid, gid);
  await writeFile(`${home}/TASK.md`, '# In flight\nIsolated task state\n'); await chown(`${home}/TASK.md`, uid, gid);
  const config = { home, state, user: 'bot-next', uid, gid, clipBytes: 64, maxOutputBytes: 65536, inboxBytes: 1024, shellSeconds: 1 };
  const host = new Host(config); await host.init();
  await symlink(`${state}/journal`, `${home}/journal`);
  await host.event('fixture', { content: 'readable evidence' });
  const journal = `${state}/journal/${(await readdir(`${state}/journal`)).find(x => x.endsWith('.jsonl'))}`;
  await writeFile(`${state}/private`, 'private', { mode: 0o600 });
  process.env.HOST_TEST_SECRET = 'must-not-be-forwarded';
  const isolated = await host.shell(`test "$(id -u)" = ${uid} && test -z "$HOST_TEST_SECRET" && test -r ${journal} && test ! -w ${journal} && test ! -w ${state} && test ! -r ${state}/private && printf boundary-ok`);
  delete process.env.HOST_TEST_SECRET;
  assert.equal(isolated.code, 0, JSON.stringify(isolated)); assert.equal(isolated.output, 'boundary-ok');
  const full = await host.shell("head -c 20000 /dev/zero | tr '\\0' x");
  assert.equal(full.code, 0); assert.equal(full.output.length, 64); assert.equal(full.clipped, true);
  assert.equal((await readFile(full.fullOutput)).length, 20000);
  const began = Date.now();
  const timeout = await host.shell('setsid sleep 30 & printf "$!" > child.pid; wait');
  assert.notEqual(timeout.code, 0); assert.ok(Date.now() - began < 10000);
  const pid = Number(await readFile(`${home}/child.pid`, 'utf8'));
  let processState = ''; try { processState = (await exec('ps', ['-p', String(pid), '-o', 'stat='])).stdout.trim(); } catch {}
  assert.ok(!processState || processState.startsWith('Z'), `escaped descendant alive: ${pid} ${processState}`);
  await writeFile(`${home}/.reset`, ''); assert.equal(await host.resetRequested(), true);
  const previous = host.journalPath(); assert.equal(previous, journal);
  const snapshot = await host.snapshot();
  const files = (await exec('tar', ['-tf', snapshot])).stdout;
  assert.match(files, /README.md/); assert.match(files, /TASK.md/); assert.doesNotMatch(files, /\.\/journal/); assert.doesNotMatch(files, /\.\/inbox|\.\/\.reset/);
  await host.clearReset(); assert.equal(await host.resetRequested(), false);
  const denied = await host.shell(`test ! -r ${snapshot} && test ! -w ${state}/journal && printf snapshot-protected`);
  assert.equal(denied.code, 0);
  const { Harness } = await import(pathToFileURL(`${root}/harness/loop.mjs`));
  const contexts = [];
  const stub = { contextWindow: 10000, maxTokens: 100, reservation: .1, complete: async context => {
    contexts.push(structuredClone(context));
    return { role: 'assistant', content: contexts.length === 1
      ? [{ type: 'toolCall', name: 'sh', id: 'save', arguments: { command: "printf 'Latest VM task state' > TASK.md; touch .reset" } }]
      : [{ type: 'text', text: 'Done' }], stopReason: contexts.length === 1 ? 'toolUse' : 'stop', usage: { input: 500, output: 5, cost: { total: .01 } } };
  } };
  const loop = new Harness(host, stub, { maxSteps: 5, maxRunUsd: 2, maxDayUsd: 10 });
  await loop.boot(); await host.enqueue('owner', 'Isolated reset fixture'); await loop.run();
  assert.equal(contexts.length, 2); assert.match(contexts[1].systemPrompt, /Latest VM task state/);
  assert.equal(contexts[1].messages[0].content, `Reset. Previous session: ${journal}`);
  assert.equal(await host.resetRequested(), false);
  config.maxOutputBytes = 1024;
  const capped = await host.shell('yes overflowing');
  assert.match(capped.error, /incomplete/); assert.ok((await readFile(capped.fullOutput)).length <= 1024);
  // Boot the real entrypoint in a paused temporary home. No API credential and
  // no model request: this exercises recovery, startup readiness and the flock.
  Object.assign(config, { provider: 'anthropic', model: 'claude-haiku-5-5', maxSteps: 10, maxRunUsd: 2, maxDayUsd: 10 });
  const configPath = `${base}/config.json`; await writeFile(configPath, JSON.stringify(config));
  await writeFile(`${state}/PAUSE`, 'isolated fixture');
  await host.save({ active: { id: 'isolated-recovery', steps: 3, spent: .5 } });
  const priorRequests = (await readFile(journal, 'utf8')).match(/"type":"request"/g)?.length || 0;
  const child = spawn('/usr/bin/flock', ['--nonblock', `${state}/lock`, process.execPath, `${root}/harness/main.mjs`, configPath], { stdio: 'ignore', env: { PATH: '/usr/bin:/bin' } });
  t.after(() => child.kill('SIGTERM'));
  let ready;
  for (let i = 0; i < 50; i++) {
    try { ready = JSON.parse(await readFile(`${state}/ready.json`, 'utf8')); break; } catch { await delay(100); }
  }
  assert.ok(ready?.pid, 'real entrypoint did not become ready');
  const contender = await exec('/usr/bin/flock', ['--nonblock', `${state}/lock`, '/bin/true']).then(() => 0, error => error.code);
  assert.equal(contender, 1);
  const recovered = await host.load(); assert.equal(recovered.active.steps, 3); assert.equal(recovered.active.spent, .5);
  const resumed = await host.take(); assert.ok(resumed.some(item => item.source === 'recovery'));
  const liveLog = await readFile(journal, 'utf8'); assert.equal(liveLog.match(/"type":"request"/g)?.length || 0, priorRequests);
  process.kill(ready.pid, 'SIGTERM'); await new Promise(resolve => child.once('exit', resolve));
});
