import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
const mode=process.argv[2];
try {
  const config=JSON.parse(await readFile(join(process.env.BOT_DRAFTS,'config.json'),'utf8'));
  const options=config.apps['terminal-chat'];
  if(!options?.enabled)throw new Error('Terminal chat is disabled');
  if(mode==='acp') {
    const {runACP}=await import('./transport.mjs');process.exitCode=await runACP(options.adapter);
  } else if(mode==='chat') {
    if(!process.stdin.isTTY||!process.stdout.isTTY)throw new Error('Terminal chat needs an interactive terminal. Run bot serve for headless operation.');
    const {runTerminalChat}=await import('./terminal-chat.mjs');
    process.exitCode=await runTerminalChat({command:process.execPath,args:[fileURLToPath(import.meta.url),'acp'],cwd:process.env.BOT_WORKSPACE,env:process.env});
  } else console.log('terminal-chat chat | acp');
} catch(error){console.error(error.message);process.exitCode=1;}
