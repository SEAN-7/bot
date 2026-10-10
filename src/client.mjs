import { readFile, realpath } from 'node:fs/promises';
import { join } from 'node:path';
import { createHash } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { launchProtected, stopRuntime } from './sandbox.mjs';
import { commandAt } from './app-package.mjs';

// A client gets durable-input/status access, never the operator credential.
export async function launchClient({home,endpoint,command,unsafe=false}) {
  home=await realpath(home);
  const clients=JSON.parse(await readFile(join(home,'control','clients.json'),'utf8'));
  if(!clients[command])throw new Error('No configured client command: '+command);
  const root=fileURLToPath(new URL('../',import.meta.url));
  const launch=commandAt(root,clients[command]);
  const {child}=await launchProtected({home,unsafe,cwd:root,command:launch.command,args:launch.args,environment:{
    BOT_CLIENT_URL:endpoint.url,BOT_CLIENT_TOKEN:endpoint.clientToken||endpoint.uiToken,
    BOT_CLIENT_SESSION:createHash('sha256').update(home).digest('hex').slice(0,24),
    BOT_NODE:process.execPath,BOT_APP_LAUNCHER:join(root,'src','app-cli.mjs'),BOT_APPS:join(home,'drafts','apps'),BOT_DRAFTS:join(home,'drafts'),BOT_WORKSPACE:join(home,'workspace'),
    BOT_UPSTREAM_ROOT:root,PATH:join(root,'src','bin')+':'+process.env.PATH,
  }});
  const interrupt=()=>child.kill('SIGINT'),terminate=()=>child.kill('SIGTERM');
  process.on('SIGINT',interrupt);process.on('SIGTERM',terminate);
  try {return await new Promise((resolve,reject)=>{child.once('error',reject);child.once('exit',(code,signal)=>resolve(code??(signal==='SIGINT'?130:1)));});}
  finally {process.removeListener('SIGINT',interrupt);process.removeListener('SIGTERM',terminate);await stopRuntime(child);}
}
