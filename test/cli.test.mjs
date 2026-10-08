import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { createConfig, parseArgs, resolveHome } from '../src/cli.mjs';
import demoModel from '../defaults/demo-model/index.mjs';

const exec = promisify(execFile);

test('home choice is consistent and explicit paths override BOT_HOME', () => {
  const env = { BOT_HOME: '/tmp/environment-bot' };
  assert.equal(resolveHome({ home: 'chosen' }, { cwd: '/tmp/root', env, demo: true }), '/tmp/root/chosen');
  assert.equal(resolveHome({}, { cwd: '/tmp/root', env, demo: true }), '/tmp/environment-bot');
  assert.equal(resolveHome({}, { cwd: '/tmp/root', env: {} }), '/tmp/root/.bot');
  assert.equal(resolveHome({}, { cwd: '/tmp/root', env: {}, demo: true }), '/tmp/root/.bot-demo');
});

test('setup defaults to a no-cost model and applies feature choices without secrets', () => {
  const { options } = parseArgs(['setup', '--disable=execution,scheduler', '--enable', 'scheduler']);
  const config = createConfig(options, { OPENAI_API_KEY: 'must-not-be-written' });
  assert.equal(config.model.provider, 'demo');
  assert.equal(config.modules['demo-model'].enabled, true);
  assert.equal(config.modules['pi-model'].enabled, false);
  assert.equal(config.modules.execution.enabled, false);
  assert.equal(config.modules.scheduler.enabled, true);
  assert.equal(JSON.stringify(config).includes('must-not-be-written'), false);
});

test('real providers require explicit model IDs and known provider credentials', () => {
  assert.throws(() => createConfig({ provider: 'openai' }, {}), /explicit model/);
  assert.throws(() => createConfig({ provider: 'openai', model: 'chosen' }, {}), /OPENAI_API_KEY/);
  const config = createConfig({ provider: 'openai', model: 'chosen' }, { OPENAI_API_KEY: 'test-only' });
  assert.equal(config.modules['pi-model'].model, 'chosen');
  assert.equal(config.modules['demo-model'].enabled, false);
  assert.throws(() => createConfig({ provider: 'google', model: 'chosen' }, { GOOGLE_API_KEY: 'unsupported-alias' }), /GEMINI_API_KEY/);
  assert.equal(createConfig({ provider: 'google', model: 'chosen' }, { GEMINI_API_KEY: 'test-only' }).modules['pi-model'].enabled, true);
  assert.throws(() => createConfig({ disable: 'made-up-feature' }, {}), /Unknown feature/);
  assert.throws(() => createConfig({ port: '70000' }, {}), /--port/);
  assert.throws(() => parseArgs(['start', '--home']), /needs a value/);
  assert.throws(() => parseArgs(['start', '--unsafe-development=false']), /does not take a value/);
});

test('setup preserves an existing installation instead of overwriting it', async () => {
  const root = await mkdtemp(join(tmpdir(), 'bot-cli-test-'));
  const home = join(root, 'installation');
  const cli = new URL('../src/cli.mjs', import.meta.url).pathname;
  try {
    await exec(process.execPath, [cli, 'setup', '--home', home, '--yes'], { env: { ...process.env, BOT_HOME: join(root, 'wrong-home') } });
    const before = await readFile(join(home, 'control', 'releases.json'), 'utf8');
    const config = JSON.parse(await readFile(join(home, 'drafts', 'config.json'), 'utf8'));
    assert.equal(config.model.provider, 'demo');
    await assert.rejects(exec(process.execPath, [cli, 'setup', '--home', home, '--yes']), error => /will not overwrite/.test(error.stderr));
    assert.equal(await readFile(join(home, 'control', 'releases.json'), 'utf8'), before);
  } finally { await rm(root, { recursive: true, force: true }); }
});

test('interactive chat commands reject nonterminals before creating a home', async () => {
  const root = await mkdtemp(join(tmpdir(), 'bot-cli-nontty-'));
  const home = join(root, 'not-created');
  const cli = new URL('../src/cli.mjs', import.meta.url).pathname;
  try {
    for (const command of ['start', 'demo', 'chat']) {
      await assert.rejects(exec(process.execPath, [cli, command, '--home', home]), error => /interactive terminal.*bot serve/.test(error.stderr));
    }
    await assert.rejects(readFile(join(home, 'drafts', 'config.json')), { code: 'ENOENT' });
    const { stdout } = await exec(process.execPath, [cli, '--help']);
    assert.match(stdout, /serve\s+Run the supervisor without a frontend/);
    assert.match(stdout, /chat\s+Attach Toad/);
  } finally { await rm(root, { recursive: true, force: true }); }
});

test('demo explicitly identifies itself, never requests tool execution, and honors cancellation', async () => {
  let model;
  demoModel({ registerModel(value) { model = value; } });
  const request = { messages: [{ role: 'user', content: 'Please run a command' }], signal: new AbortController().signal };
  const first = await model.complete(request);
  assert.deepEqual(first, await model.complete(request));
  assert.match(first.text, /Demo mode · no AI connected/);
  assert.deepEqual(first.toolCalls, []);
  const controller = new AbortController(); controller.abort();
  await assert.rejects(model.complete({ ...request, signal: controller.signal }), { name: 'AbortError' });
});
