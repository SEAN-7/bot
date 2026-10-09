import { DatabaseSync } from 'node:sqlite';
import { mkdirSync, chmodSync } from 'node:fs';
import { join } from 'node:path';

export class TelegramStore {
  constructor(folder) {
    mkdirSync(folder, {recursive:true,mode:0o700});
    const file = join(folder, 'telegram.sqlite');
    this.db = new DatabaseSync(file); chmodSync(file, 0o600);
    this.db.exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;
      CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY, value TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS messages(chat_id INTEGER, message_id INTEGER, sender TEXT, data TEXT NOT NULL, PRIMARY KEY(chat_id,message_id));
      CREATE TABLE IF NOT EXISTS deliveries(id TEXT PRIMARY KEY, text TEXT NOT NULL, reply_to INTEGER, status TEXT NOT NULL, message_id INTEGER, error TEXT);`);
  }
  get(key, fallback=null) { const row=this.db.prepare('SELECT value FROM settings WHERE key=?').get(key); return row?JSON.parse(row.value):fallback; }
  set(key, value) { this.db.prepare('INSERT INTO settings VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value').run(key,JSON.stringify(value)); }
  message(message, sender) {
    this.db.prepare('INSERT INTO messages VALUES(?,?,?,?) ON CONFLICT(chat_id,message_id) DO UPDATE SET data=excluded.data').run(message.chat.id,message.message_id,sender,JSON.stringify(message));
  }
  read({ after, limit=20, through }={}) {
    const binding=this.get('binding'); if(!binding)return {messages:[],paired:false};
    const rows=after===undefined
      ? this.db.prepare("SELECT * FROM messages WHERE chat_id=? AND (? IS NULL OR message_id<=? OR sender='bot') ORDER BY message_id DESC LIMIT ?").all(binding.chatId,through??null,through??null,limit).reverse()
      : this.db.prepare("SELECT * FROM messages WHERE chat_id=? AND (? IS NULL OR message_id<=? OR sender='bot') AND message_id>? ORDER BY message_id LIMIT ?").all(binding.chatId,through??null,through??null,after,limit);
    return {paired:true,messages:rows.map(row=>{const m=JSON.parse(row.data);return {id:m.message_id,sender:row.sender,text:m.text||m.caption||'',timestamp:m.date*1000,replyTo:m.reply_to_message?.message_id,attachments:Object.keys(m).filter(key=>['photo','document','audio','voice','video','sticker'].includes(key))};}),nextAfter:rows.at(-1)?.message_id??after??0};
  }
  delivery(id) { return this.db.prepare('SELECT * FROM deliveries WHERE id=?').get(id); }
  startDelivery(id, text, replyTo) { this.db.prepare("INSERT INTO deliveries(id,text,reply_to,status) VALUES(?,?,?,'started')").run(id,text,replyTo??null); }
  finishDelivery(id, status, message, error) {
    this.db.exec('BEGIN IMMEDIATE');
    try {
      if(message)this.message(message,'bot');
      this.db.prepare('UPDATE deliveries SET status=?,message_id=?,error=? WHERE id=?').run(status,message?.message_id??null,error??null,id);
      this.db.exec('COMMIT');
    } catch(error) {this.db.exec('ROLLBACK');throw error;}
  }
  status() { return {configured:true,binding:this.get('binding'),offset:this.get('offset',0),watch:this.get('watch'),uncertain:this.db.prepare("SELECT id,status FROM deliveries WHERE status IN ('started','unknown')").all()}; }
  close(){this.db.close();}
}
