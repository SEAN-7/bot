import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, writeFile, readdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { commands } from '../apps/memory/commands.mjs';
import { readIndex, writeIndex, measure } from '../apps/memory/index.mjs';
import { contribute } from '../apps/memory/behavior.mjs';

async function fixture(t, legacy = null, config = {}) {
  const workspace = await mkdtemp(join(tmpdir(), 'bot-memory-index-'));
  t.after(() => rm(workspace, { recursive: true, force: true }));
  return { paths: { workspace }, config,
    state: { get: async () => structuredClone(legacy), set: () => assert.fail('Legacy state must remain untouched') } };
}

test('index migration preserves small facts, all oversized legacy content, and an existing index', async t => {
  for (const text of ['Use concise replies.', '古い記録'.repeat(1500)]) {
    const api = await fixture(t, { revision: 7, USER: text, notes: 'Retain these notes.' });
    const [one, two] = await Promise.all([readIndex(api), readIndex(api)]);
    assert.equal(one.markdown, two.markdown); assert.ok(one.fits);
    const archive = (await readdir(join(api.paths.workspace, 'memory'))).find(name => name.startsWith('legacy-'));
    const detail = await readFile(join(api.paths.workspace, 'memory', archive), 'utf8');
    assert.ok(detail.includes(text)); assert.match(detail, /Retain these notes/);
    assert.ok(one.markdown.includes(archive));
    if (text.length < 100) assert.ok(one.markdown.includes(text));
    await writeIndex(api, '# Projects\n- Records and decisions: memory/projects.md\n');
    assert.equal((await readIndex(api)).markdown, '# Projects\n- Records and decisions: memory/projects.md\n');
    assert.equal((await readdir(join(api.paths.workspace, 'memory'))).filter(n => n.endsWith('.tmp')).length, 0);
  }
});

test('raw Markdown edits enforce an explicit UTF-8 budget without truncation and can repair invalid direct edits', async t => {
  const api = await fixture(t, null, { maxIndexTokens: 128 }), cmd = commands(api);
  assert.equal(cmd.write.inputFormat, 'text'); assert.equal(cmd.show.outputFormat, 'text');
  const text = '# Index\n- λ & "quotes"\n- Emoji: 🦊\n';
  await cmd.write.execute(text);
  assert.equal(await cmd.show.execute(), text);
  const before = await cmd.status.execute();
  assert.equal(before.estimatedTokens, Math.ceil(Buffer.byteLength(text) / 4));
  assert.equal(before.estimator, 'ceil(UTF-8 bytes / 4)');
  assert.equal((await cmd.check.execute('🦊'.repeat(129))).fits, false);
  await assert.rejects(cmd.write.execute('🦊'.repeat(129)), /exceeds.*by 1/);
  assert.deepEqual(await cmd.status.execute(), before);
  await cmd.write.execute('🦊'.repeat(128));
  assert.equal((await cmd.status.execute()).estimatedTokens, 128);
  await writeFile(before.path, 'x'.repeat(1000));
  await assert.rejects(readIndex(api), /exceeds/);
  await cmd.write.execute(text);
  assert.equal(await cmd.show.execute(), text);
  await cmd.write.execute('');
  assert.equal(await cmd.show.execute(), '');
  assert.match((await contribute(api))[0].content, /No background process/);
  assert.throws(() => measure({ config: { maxIndexTokens: -1 } }, ''), /maxIndexTokens/);
});

test('first replacement preserves legacy facts even without a prior read and publishes fresh contributions', async t => {
  const api = await fixture(t, { revision: 2, USER: 'Old preference', notes: 'Old project' });
  await writeIndex(api, '# Current index\n');
  const folder = join(api.paths.workspace, 'memory');
  const archive = (await readdir(folder)).find(name => name.startsWith('legacy-'));
  assert.match(await readFile(join(folder, archive), 'utf8'), /Old preference/);
  const first = (await contribute(api))[0];
  assert.equal(first.refresh, 'always'); assert.equal(first.placement, 'system');
  await writeIndex(api, '# Updated index\n');
  const second = (await contribute(api))[0];
  assert.notEqual(first.key, second.key); assert.match(second.content, /Updated index/);
  assert.doesNotMatch(second.content, /Current index/);
});
