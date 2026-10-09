import { DatabaseSync } from 'node:sqlite';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { randomUUID } from 'node:crypto';

export class Store {
  constructor(home) {
    mkdirSync(join(home, 'control'), { recursive: true, mode: 0o700 });
    this.db = new DatabaseSync(join(home, 'control', 'journal.sqlite'));
    this.db.exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;
      CREATE TABLE IF NOT EXISTS journal(seq INTEGER PRIMARY KEY, kind TEXT NOT NULL, payload TEXT NOT NULL, timestamp INTEGER NOT NULL);
      CREATE TABLE IF NOT EXISTS messages(id INTEGER PRIMARY KEY, role TEXT NOT NULL, data TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS inbox(id TEXT PRIMARY KEY, text TEXT NOT NULL, metadata TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', retry_at INTEGER NOT NULL DEFAULT 0, timestamp INTEGER NOT NULL);
      CREATE TABLE IF NOT EXISTS actions(id TEXT PRIMARY KEY, event_id TEXT NOT NULL, name TEXT NOT NULL, args TEXT NOT NULL, status TEXT NOT NULL, result TEXT);
      CREATE TABLE IF NOT EXISTS state(module TEXT NOT NULL, key TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY(module,key));
      CREATE TABLE IF NOT EXISTS contributions(key TEXT PRIMARY KEY);
      CREATE TABLE IF NOT EXISTS kv(key TEXT PRIMARY KEY, value TEXT NOT NULL);`);
  }
  transaction(fn) {
    this.db.exec('BEGIN IMMEDIATE');
    try { const result = fn(); this.db.exec('COMMIT'); return result; }
    catch (error) { this.db.exec('ROLLBACK'); throw error; }
  }
  record(kind, payload) {
    const timestamp = Date.now();
    const result = this.db.prepare('INSERT INTO journal(kind,payload,timestamp) VALUES(?,?,?)').run(kind, JSON.stringify(payload), timestamp);
    return { seq: Number(result.lastInsertRowid), kind, payload, timestamp };
  }
  append(message) {
    const data = { ...message, timestamp: message.timestamp || Date.now() };
    const entry = this.record('message', data);
    this.db.prepare('INSERT INTO messages(id,role,data) VALUES(?,?,?)').run(entry.seq, data.role, JSON.stringify(data));
    return { ...data, id: entry.seq };
  }
  history() { return this.db.prepare('SELECT id,data FROM messages ORDER BY id').all().map(row => ({ ...JSON.parse(row.data), id: row.id })); }
  chatSnapshot() { return { messages: this.history(), after: this.db.prepare('SELECT COALESCE(MAX(seq),0) AS seq FROM journal').get().seq }; }
  cancel(id) {
    if(typeof id!=='string'||!id)throw new Error('Input ID is required');
    return this.transaction(()=>{
      const row=this.db.prepare('SELECT status FROM inbox WHERE id=?').get(id);
      if(!row||row.status==='done')return {id,cancelled:false};
      this.db.prepare("UPDATE inbox SET status='done' WHERE id=?").run(id);
      this.record('input.cancelled',{id});return {id,cancelled:true};
    });
  }
  events(after = 0) { return this.db.prepare('SELECT * FROM journal WHERE seq>? ORDER BY seq LIMIT 500').all(after).map(row => ({ ...row, payload: JSON.parse(row.payload) })); }
  enqueue({ id = randomUUID(), text, metadata = {} }) {
    if (typeof id !== 'string' || !id || id.length > 500 || typeof text !== 'string' || !text.trim() || text.length > 100_000) throw new Error('Invalid input event');
    return this.transaction(() => {
      const existing = this.db.prepare('SELECT id,text FROM inbox WHERE id=?').get(id);
      if (existing) {
        if (existing.text !== text) throw new Error('Event ID already used for different text');
        return { id, duplicate: true };
      }
      this.db.prepare('INSERT INTO inbox(id,text,metadata,timestamp) VALUES(?,?,?,?)').run(id, text, JSON.stringify(metadata), Date.now());
      this.record('input.accepted', { id, text, metadata });
      return { id, duplicate: false };
    });
  }
  next() {
    return this.transaction(() => {
      const row = this.db.prepare("SELECT * FROM inbox WHERE status='pending' ORDER BY timestamp,rowid LIMIT 1").get();
      if (!row || row.retry_at > Date.now()) return null;
      this.db.prepare("UPDATE inbox SET status='processing' WHERE id=?").run(row.id);
      const metadata = JSON.parse(row.metadata);
      const seen = this.db.prepare("SELECT 1 FROM journal WHERE kind='input.started' AND json_extract(payload,'$.id')=?").get(row.id);
      if (!seen) {
        this.append({ role: 'user', content: row.text, metadata: { ...metadata, eventId: row.id } });
        this.record('input.started', { id: row.id });
      }
      return { id: row.id, text: row.text, metadata };
    });
  }
  complete(id) { this.transaction(() => { this.db.prepare("UPDATE inbox SET status='done' WHERE id=?").run(id); this.record('input.completed', { id }); }); }
  commitReply(id,message) {
    return this.transaction(()=>{
      const row=this.db.prepare('SELECT status FROM inbox WHERE id=?').get(id);
      if(!row||row.status!=='processing')throw new Error('Input is not active');
      const saved=this.append(message);
      this.db.prepare("UPDATE inbox SET status='done' WHERE id=?").run(id);
      this.record('input.completed',{id});return saved;
    });
  }
  retry(id, delayMs) { this.db.prepare("UPDATE inbox SET status='pending',retry_at=? WHERE id=?").run(Date.now() + delayMs, id); }
  pending() { return this.db.prepare("SELECT COUNT(*) AS n FROM inbox WHERE status!='done'").get().n; }
  resume() { this.db.prepare("UPDATE inbox SET retry_at=0 WHERE status='pending'").run(); }
  contribute(key, content, module) {
    return this.transaction(() => {
      const scoped = key ? `${module}:${key}` : randomUUID();
      if (this.db.prepare('SELECT 1 FROM contributions WHERE key=?').get(scoped)) return null;
      this.db.prepare('INSERT INTO contributions(key) VALUES(?)').run(scoped);
      return this.append({ role: 'context', content, metadata: { module, key: scoped } });
    });
  }
  startAction(id, eventId, name, args) {
    this.transaction(() => {
      this.db.prepare("INSERT INTO actions(id,event_id,name,args,status) VALUES(?,?,?,?,'started')").run(id,eventId,name,JSON.stringify(args));
      this.record('tool.started', { id, eventId, name, arguments: args });
    });
  }
  finishAction(id, result, isError = false) {
    return this.transaction(() => {
      const action = this.db.prepare('SELECT * FROM actions WHERE id=?').get(id);
      if (!action || action.status !== 'started') throw new Error('Tool action is not pending');
      this.db.prepare("UPDATE actions SET status='completed',result=? WHERE id=?").run(JSON.stringify(result),id);
      this.record('tool.completed', { id, isError });
      return this.append({ role: 'tool', content: typeof result === 'string' ? result : JSON.stringify(result ?? null), toolCallId: id, toolName: action.name, isError });
    });
  }
  publish(module, callId, content, eventId) {
    if(typeof content!=='string'||!content.trim()||content.length>100_000)throw new Error('Invalid publication');
    return this.transaction(()=>{
      const action=this.db.prepare('SELECT event_id,status FROM actions WHERE id=?').get(callId);
      if(!action||action.status!=='started'||action.event_id!==eventId)throw new Error('Publication requires an active tool action');
      const existing=this.history().find(m=>m.role==='channel'&&m.metadata?.module===module&&m.metadata?.callId===callId);
      if(existing) {
        if(existing.content!==content)throw new Error('Tool action already published different content');
        return {id:existing.id};
      }
      const saved=this.append({role:'channel',content,metadata:{module,callId,eventId}});
      return {id:saved.id};
    });
  }
  recover(reason) {
    return this.transaction(() => {
      const unresolved = [];
      const history = this.history();
      const results = new Set(history.filter(m => m.role === 'tool').map(m => m.toolCallId));
      for (const message of history) for (const call of message.toolCalls || []) {
        if (results.has(call.id)) continue;
        const action = this.db.prepare('SELECT * FROM actions WHERE id=?').get(call.id);
        const unknown = action?.status === 'started' || action?.status === 'unknown';
        if (unknown) this.db.prepare("UPDATE actions SET status='unknown' WHERE id=?").run(call.id);
        const content = unknown ? 'Recovery: outcome UNKNOWN. This action may already have happened. Investigate its external state before deciding whether to repeat it.' : 'Recovery: this requested action was NOT EXECUTED before interruption.';
        this.append({ role:'tool', content, toolCallId:call.id, toolName:call.name, isError:true });
        unresolved.push({ id:call.id, name:call.name, outcome:unknown ? 'unknown' : 'not-executed' });
      }
      this.db.prepare("UPDATE inbox SET status='pending',retry_at=0 WHERE status='processing'").run();
      reason ||= unresolved.length ? 'Interrupted actions reconciled after supervisor restart' : null;
      if (reason) {
        this.record('runtime.recovered', { reason, unresolved });
        this.append({ role:'context', content:`Recovery report: ${reason}${unresolved.length ? '\nInterrupted actions: ' + JSON.stringify(unresolved) : ''}`, metadata:{ recovery:true } });
      }
      return unresolved;
    });
  }
  getState(module, key, fallback = null) { const row=this.db.prepare('SELECT value FROM state WHERE module=? AND key=?').get(module,key); return row ? JSON.parse(row.value) : fallback; }
  setState(module,key,value) { this.db.prepare('INSERT INTO state(module,key,value) VALUES(?,?,?) ON CONFLICT(module,key) DO UPDATE SET value=excluded.value').run(module,key,JSON.stringify(value)); }
  deleteState(module,key) { this.db.prepare('DELETE FROM state WHERE module=? AND key=?').run(module,key); }
  snapshot() { return this.db.prepare('SELECT * FROM state').all(); }
  restore(snapshot) { this.transaction(() => { this.db.exec('DELETE FROM state; DELETE FROM contributions'); for(const row of snapshot) this.db.prepare('INSERT INTO state(module,key,value) VALUES(?,?,?)').run(row.module,row.key,row.value); }); }
  get(key, fallback=null) { const row=this.db.prepare('SELECT value FROM kv WHERE key=?').get(key); return row ? JSON.parse(row.value) : fallback; }
  set(key,value) { this.db.prepare('INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value').run(key,JSON.stringify(value)); }
  close() { this.db.close(); }
}
