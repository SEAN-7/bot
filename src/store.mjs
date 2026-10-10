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
      // Deferred blocked work must not hold up new input. Temporary retries
      // retain their order; blocked work rejoins the queue when its retry is due.
      const row = this.db.prepare("SELECT * FROM inbox WHERE status='pending' OR (status='blocked' AND retry_at<=?) ORDER BY timestamp,rowid LIMIT 1").get(Date.now());
      if (!row || row.retry_at > Date.now()) return null;
      return this.startInput(row);
    });
  }
  // Called at model-context boundaries, after outstanding tool results are saved.
  // One serialized conversation can incorporate new input without ending its work.
  admitPending() {
    return this.transaction(() => this.db.prepare("SELECT * FROM inbox WHERE status='pending' AND retry_at<=? ORDER BY timestamp,rowid").all(Date.now()).map(row => this.startInput(row)));
  }
  startInput(row) {
    this.db.prepare("UPDATE inbox SET status='processing' WHERE id=?").run(row.id);
    const metadata = JSON.parse(row.metadata);
    const seen = this.db.prepare("SELECT 1 FROM journal WHERE kind='input.started' AND json_extract(payload,'$.id')=?").get(row.id);
    if (!seen) {
      this.append({ role: 'user', content: row.text, metadata: { ...metadata, eventId: row.id } });
      this.record('input.started', { id: row.id });
    } else if (this.history().findLast(m => m.role === 'user' || m.metadata?.resumedInput)?.metadata?.eventId !== row.id) {
      // Keep the original human input unique while identifying resumed work
      // after a newer input has been processed.
      this.append({ role: 'context', content: `Resuming unfinished input ${row.id}:\n${row.text}`, metadata: { ...metadata, eventId: row.id, input: true, resumedInput: true } });
    }
    return { id: row.id, text: row.text, metadata };
  }
  complete(id) { this.transaction(() => { this.db.prepare("UPDATE inbox SET status='done' WHERE id=?").run(id); this.record('input.completed', { id }); }); }
  acknowledge(inputIds) {
    return this.transaction(()=>{
      for(const id of inputIds) {
        const row=this.db.prepare('SELECT status FROM inbox WHERE id=?').get(id);
        if(!row||row.status!=='processing')throw new Error('Input is not active');
      }
      for(const id of inputIds) {
        this.db.prepare("UPDATE inbox SET status='done' WHERE id=?").run(id);
        // Delivery is a receipt, not a claim that the human's objective is done.
        this.record('input.delivered',{id});
      }
    });
  }
  commitReply(id,message,inputIds=[id]) {
    return this.transaction(()=>{
      const ids=[...new Set([id,...inputIds])];
      for(const inputId of ids) {
        const row=this.db.prepare('SELECT status FROM inbox WHERE id=?').get(inputId);
        if(!row||row.status!=='processing')throw new Error('Input is not active');
      }
      const saved=this.append(message);
      for(const inputId of ids) {
        this.db.prepare("UPDATE inbox SET status='done' WHERE id=?").run(inputId);
        this.record('input.completed',{id:inputId});
      }
      return saved;
    });
  }
  retry(id, delayMs, blocked = false) { this.db.prepare('UPDATE inbox SET status=?,retry_at=? WHERE id=?').run(blocked ? 'blocked' : 'pending', Date.now() + delayMs, id); }
  pending() { return this.db.prepare("SELECT COUNT(*) AS n FROM inbox WHERE status!='done'").get().n; }
  resume() { this.db.prepare("UPDATE inbox SET status='pending',retry_at=0 WHERE status IN ('pending','blocked')").run(); }
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
      const calls = new Map(history.flatMap(message => (message.toolCalls || []).map(call => [call.id, call])));
      const sessions = new Map();
      const settledSessions = new Set(history.map(message => message.metadata?.shellSessionUnknown).filter(Boolean));
      // A completion may be durable in the inbox before the controller admits
      // it to history. Do not report that already-finished session as unknown.
      for (const row of this.db.prepare("SELECT metadata FROM inbox WHERE json_extract(metadata,'$.source')='shell'").all()) settledSessions.add(JSON.parse(row.metadata).sessionId);
      for (const message of history) if (message.role === 'tool' && message.toolName === 'shell') {
        let result; try { result = JSON.parse(message.content); } catch { continue; }
        const sessionId = result?.sessionId || calls.get(message.toolCallId)?.arguments?.sessionId;
        if (!sessionId) continue;
        if (result?.outcome === 'running') sessions.set(sessionId, sessions.get(sessionId) || message.toolCallId);
        else sessions.delete(sessionId);
      }
      for (const [sessionId, callId] of sessions) if (!settledSessions.has(sessionId)) {
        this.db.prepare("UPDATE actions SET status='unknown' WHERE id=?").run(callId);
        this.append({ role:'context', content:`Recovery: outcome UNKNOWN. Shell session ${sessionId} was interrupted by runtime shutdown. Its effects may already have happened; inspect external state before repeating the command.`, metadata:{recovery:true,shellSessionUnknown:sessionId} });
        unresolved.push({id:callId,name:'shell',sessionId,outcome:'unknown'});
      }
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
