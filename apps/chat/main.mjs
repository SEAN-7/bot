import { runCLI } from '../_lib/cli.mjs';
import { commands } from './commands.mjs';
import { project, watch } from './behavior.mjs';
if(process.argv[2]==='acp')await import('./acp.mjs');
else await runCLI({commands,operations:{project:(_api,input)=>project(input.messages),watch:async api=>{
  const controller=new AbortController();for(const signal of ['SIGTERM','SIGINT'])process.once(signal,()=>controller.abort());
  await watch(api,{signal:controller.signal});
}}});
