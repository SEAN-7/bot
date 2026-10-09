import { DatabaseSync } from 'node:sqlite';
import { mkdirSync, chmodSync } from 'node:fs';
import { join } from 'node:path';
import { createHash } from 'node:crypto';

export const notification = id => `Terminal chat message received (${id}). Read it with bot-app chat read and reply using bot-app chat send. Chat messages and delivery state belong to the chat app.`;
export const isNotification = text => /^Terminal chat message received \(chat:[A-Za-z0-9-]+\)\./.test(text);
export class ChatStore {
  constructor(workspace) {
    const folder=join(workspace,'chat');mkdirSync(folder,{recursive:true,mode:0o700});
    const file=join(folder,'chat.sqlite');this.db=new DatabaseSync(file);chmodSync(file,0o600);
    this.db.exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;
      CREATE TABLE IF NOT EXISTS messages(id INTEGER PRIMARY KEY, key TEXT UNIQUE NOT NULL, sender TEXT NOT NULL, text TEXT NOT NULL, timestamp INTEGER NOT NULL, event_id TEXT, notified INTEGER NOT NULL DEFAULT 1);`);
  }
  save(key,sender,text,{timestamp=Date.now(),eventId=null,notified=true}={}) {
    const existing=this.db.prepare('SELECT * FROM messages WHERE key=?').get(key);
    if(existing){if(existing.text!==text)throw new Error('Message ID already used for different text');return {id:existing.id,duplicate:true};}
    const result=this.db.prepare('INSERT INTO messages(key,sender,text,timestamp,event_id,notified) VALUES(?,?,?,?,?,?)').run(key,sender,text,timestamp,eventId,Number(notified));
    return {id:Number(result.lastInsertRowid)};
  }
  accept(eventId,text) {return this.save(eventId,'human',text,{eventId,notified:false});}
  send(callId,text,eventId) {if(!callId)throw new Error('Sending requires a supervised shell action');return this.save('send:'+callId,'bot',text,{eventId});}
  notified(eventId){this.db.prepare('UPDATE messages SET notified=1 WHERE event_id=? AND sender=?').run(eventId,'human');}
  pending(){return this.db.prepare("SELECT * FROM messages WHERE sender='human' AND notified=0 ORDER BY id").all();}
  import(history) {
    this.db.exec('BEGIN IMMEDIATE');
    try {
      for(const message of history) {
        const human=message.role==='user'&&(!message.metadata?.source||message.metadata.source==='chat')&&!isNotification(message.content);
        const sent=message.role==='channel'&&message.metadata?.module==='chat';
        const legacy=message.role==='assistant'&&!message.metadata?.modelOutput&&message.content;
        if(!human&&!sent&&!legacy)continue;
        const id=message.id??createHash('sha256').update(JSON.stringify(message)).digest('hex');
        this.save('legacy:'+id,human?'human':'bot',message.content,{timestamp:message.timestamp,eventId:message.metadata?.eventId});
      }
      this.db.exec('COMMIT');
    }catch(error){this.db.exec('ROLLBACK');throw error;}
  }
  read({after,limit=20,eventId}={}) {
    const input=eventId?this.db.prepare("SELECT id FROM messages WHERE event_id=? AND sender='human'").get(eventId):null;
    const rows=this.db.prepare('SELECT id,sender,text,timestamp FROM messages WHERE (? IS NULL OR id<=? OR event_id=?) AND (? IS NULL OR id>?) ORDER BY id').all(input?.id??null,input?.id??null,eventId??null,after??null,after??null);
    const candidates=after===undefined?rows.slice(-limit).reverse():rows.slice(0,limit);
    const messages=[];let bytes=0;
    for(const message of candidates){const size=Buffer.byteLength(JSON.stringify(message));if(messages.length&&bytes+size>200000)break;messages.push(message);bytes+=size;}
    if(after===undefined)messages.reverse();
    return {messages,nextAfter:messages.at(-1)?.id??after??0,hasEarlier:!!messages.length&&this.db.prepare('SELECT MIN(id) AS id FROM messages').get().id<messages[0].id,hasMore:after!==undefined&&rows.length>messages.length};
  }
  allAfter(after=0){return this.db.prepare('SELECT * FROM messages WHERE id>? ORDER BY id LIMIT 500').all(after);}
  close(){this.db.close();}
}
