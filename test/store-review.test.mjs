import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { Store } from '../src/store.mjs';

test('scoped state restoration leaves durable contribution identities intact', async t => {
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
  assert.equal(store.contribute('revision-1', 'trusted memory', 'memory'), null);
  assert.deepEqual(store.history().map(message => message.content), ['trusted memory', 'candidate memory']);
});

test('resume makes blocked pending input available after an operator restart', async t => {
  const home = await mkdtemp(join(tmpdir(), 'bot-store-resume-'));
  let store = new Store(home);
  t.after(async () => { store.close(); await rm(home, { recursive: true, force: true }); });
  store.enqueue({ id: 'blocked', text: 'retry after credentials change' });
  assert.equal(store.next().id, 'blocked');
  store.retry('blocked', 86400000, true);
  assert.equal(store.next(), null);
  store.close();
  store = new Store(home);
  store.resume();
  assert.equal(store.next().id, 'blocked');
  assert.equal(store.history().filter(message => message.role === 'user').length, 1);
});

test('blocked input survives restart, allows later input, and retries when due', async t => {
  const home = await mkdtemp(join(tmpdir(), 'bot-store-blocked-'));
  let store = new Store(home);
  t.after(async () => { store.close(); await rm(home, { recursive: true, force: true }); });
  store.enqueue({ id: 'first', text: 'unfinished work' });
  assert.equal(store.next().id, 'first');
  store.retry('first', 86400000, true);
  store.close();
  store = new Store(home);
  store.enqueue({ id: 'second', text: 'follow-up question' });
  assert.equal(store.next().id, 'second');
  store.commitReply('second', { role: 'assistant', content: 'answer to question' });
  assert.equal(store.pending(), 1);
  assert.equal(store.next(), null);
  // Move the persisted deadline into the past without sleeping for a day.
  store.db.prepare('UPDATE inbox SET retry_at=0 WHERE id=?').run('first');
  assert.equal(store.next().id, 'first');
  assert.equal(store.history().filter(m => m.role === 'user' && m.metadata?.eventId === 'first').length, 1);
  assert.equal(store.history().at(-1).metadata.resumedInput, true);
  assert.match(store.history().at(-1).content, /unfinished work/);
});

test('clean restart still reports unfinished tool outcomes without repeating recovery', async t => {
  const home = await mkdtemp(join(tmpdir(), 'bot-store-interrupted-'));
  const store = new Store(home);
  t.after(async () => { store.close(); await rm(home, { recursive: true, force: true }); });
  store.append({ role: 'assistant', content: '', toolCalls: [{ id: 'unfinished', name: 'shell', arguments: {} }] });
  store.startAction('unfinished', 'input', 'shell', {});
  assert.equal(store.recover(null)[0].outcome, 'unknown');
  const report = store.history().find(message => message.metadata?.recovery);
  assert.match(report.content, /Interrupted actions reconciled/);
  assert.match(report.content, /unknown/);
  assert.deepEqual(store.recover(null), []);
  assert.equal(store.history().filter(message => message.metadata?.recovery).length, 1);
});

test('context admission completes delivered inputs together and leaves later arrivals pending', async t => {
  const home=await mkdtemp(join(tmpdir(),'bot-store-admission-'));
  const store=new Store(home);
  t.after(async()=>{store.close();await rm(home,{recursive:true,force:true});});
  store.enqueue({id:'first',text:'start work'});store.next();
  store.append({role:'assistant',content:'',toolCalls:[{id:'work',name:'shell',arguments:{}}]});
  store.startAction('work','first','shell',{});
  store.enqueue({id:'followup',text:'change direction'});
  store.finishAction('work','result saved immediately');
  assert.equal(store.history().at(-1).role,'tool');
  assert.deepEqual(store.admitPending().map(input=>input.id),['followup']);
  assert.deepEqual(store.admitPending(),[]);
  store.enqueue({id:'later',text:'arrived during the model call'});
  store.commitReply('first',{role:'assistant',content:'handled follow-up'},['first','followup']);
  assert.equal(store.pending(),1);
  assert.equal(store.next().id,'later');
  assert.deepEqual(store.history().filter(m=>m.role==='user').map(m=>m.content),['start work','change direction','arrived during the model call']);
});

test('admitted notifications survive interruption without duplicating human input or replaying tools', async t => {
  const home=await mkdtemp(join(tmpdir(),'bot-store-admission-recovery-'));
  let store=new Store(home);
  t.after(async()=>{store.close();await rm(home,{recursive:true,force:true});});
  store.enqueue({id:'first',text:'start work'});store.next();
  store.enqueue({id:'followup',text:'new instruction'});store.admitPending();
  store.append({role:'assistant',content:'',toolCalls:[{id:'interrupted',name:'shell',arguments:{}}]});
  store.startAction('interrupted','first','shell',{});
  store.close();store=new Store(home);
  assert.equal(store.recover()[0].outcome,'unknown');
  assert.equal(store.next().id,'first');
  assert.deepEqual(store.admitPending().map(input=>input.id),['followup']);
  assert.equal(store.history().filter(m=>m.role==='user').length,2);
  store.commitReply('first',{role:'assistant',content:'reconciled'},['first','followup']);
  assert.equal(store.pending(),0);
  assert.equal(store.db.prepare("SELECT status FROM actions WHERE id='interrupted'").get().status,'unknown');
});
