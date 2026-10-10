import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';

// Reuse one botctl process per app invocation; the public interface remains a CLI.
export function appClient(app,env=process.env) {
  const child=spawn(env.BOT_NODE||process.execPath,[env.BOT_CORE_CLI,app,'--session'],{env,stdio:['pipe','pipe','pipe']});
  const pending=new Map();let sequence=0,closed=false,errorText='';
  const referenced=value=>{child[value?'ref':'unref']();for(const stream of [child.stdin,child.stdout,child.stderr])stream[value?'ref':'unref']?.();};
  const fail=error=>{closed=true;for(const request of pending.values())request.reject(error);pending.clear();child.kill();referenced(false);};
  child.once('error',fail);child.stdin.on('error',fail);
  child.stderr.on('data',chunk=>{errorText=(errorText+chunk).slice(-8000);});
  child.once('close',()=>fail(new Error(errorText||'Core CLI closed')));
  createInterface({input:child.stdout}).on('line',line=>{
    try {
      const message=JSON.parse(line),request=pending.get(message.id);
      if(!request)throw new Error('Unexpected core CLI response');
      pending.delete(message.id);
      if(message.error)request.reject(new Error(message.error));else request.resolve(message.result);
      if(!pending.size)referenced(false);
    }catch(error){fail(error);}
  });
  referenced(false);
  return (operation,args={})=>new Promise((resolve,reject)=>{
    if(closed)return reject(new Error('Core CLI closed'));
    const id=++sequence;pending.set(id,{resolve,reject});referenced(true);
    child.stdin.write(JSON.stringify({id,operation,args})+'\n');
  });
}
