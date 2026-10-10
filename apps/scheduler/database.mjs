import { DatabaseSync } from 'node:sqlite';
import { mkdir } from 'node:fs/promises';
import { join } from 'node:path';

// Serialize watch/create/cancel across processes without extending the core API.
export async function transaction(api, operation) {
  const folder=join(api.paths.workspace,'scheduler');await mkdir(folder,{recursive:true});
  const db=new DatabaseSync(join(folder,'scheduler.sqlite'));
  try {
    db.exec('PRAGMA busy_timeout=20000; PRAGMA journal_mode=WAL; CREATE TABLE IF NOT EXISTS schedules(id INTEGER PRIMARY KEY CHECK(id=1), value TEXT NOT NULL)');
    db.exec('BEGIN IMMEDIATE');
    const row=db.prepare('SELECT value FROM schedules WHERE id=1').get();
    // Preserve the old core namespace for rollback; import it only once.
    let schedules=row?JSON.parse(row.value):await api.state.get('schedules',[]);
    const result=await operation({...api,state:{
      get:async()=>structuredClone(schedules),
      set:async(_key,value)=>{schedules=structuredClone(value);},
    }});
    db.prepare('INSERT INTO schedules(id,value) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value').run(JSON.stringify(schedules));
    db.exec('COMMIT');return result;
  } catch(error) {if(db.isTransaction)db.exec('ROLLBACK');throw error;}
  finally {db.close();}
}
