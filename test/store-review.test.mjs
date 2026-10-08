import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { Store } from '../src/store.mjs';

test('restored memory can append its old contribution after failed candidate memory', async t => {
  const home = await mkdtemp(join(tmpdir(), 'bot-store-review-'));
  const store = new Store(home);
  t.after(async () => { store.close(); await rm(home, { recursive: true, force: true }); });
  store.setState('memory', 'notes', { revision: 1, text: 'trusted memory' });
  assert.ok(store.contribute('revision-1', 'trusted memory', 'memory'));
  assert.equal(store.contribute('revision-1', 'trusted memory', 'memory'), null);
  const snapshot = store.snapshot();
  store.setState('memory', 'notes', { revision: 2, text: 'candidate memory' });
  assert.ok(store.contribute('revision-2', 'candidate memory', 'memory'));
  store.restore(snapshot);
  assert.deepEqual(store.getState('memory', 'notes'), { revision: 1, text: 'trusted memory' });
  assert.ok(store.contribute('revision-1', 'trusted memory', 'memory'));
  assert.equal(store.contribute('revision-1', 'trusted memory', 'memory'), null);
  assert.deepEqual(store.history().map(message => message.content), ['trusted memory', 'candidate memory', 'trusted memory']);
});

test('resume makes blocked pending input available after an operator restart', async t => {
  const home = await mkdtemp(join(tmpdir(), 'bot-store-resume-'));
  let store = new Store(home);
  t.after(async () => { store.close(); await rm(home, { recursive: true, force: true }); });
  store.enqueue({ id: 'blocked', text: 'retry after credentials change' });
  assert.equal(store.next().id, 'blocked');
  store.retry('blocked', 86400000);
  assert.equal(store.next(), null);
  store.close();
  store = new Store(home);
  store.resume();
  assert.equal(store.next().id, 'blocked');
  assert.equal(store.history().filter(message => message.role === 'user').length, 1);
});
