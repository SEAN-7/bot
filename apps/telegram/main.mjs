import { runCLI } from '../_lib/cli.mjs';
import { commands } from './commands.mjs';
import { project, watch } from './behavior.mjs';

if(['configure','inspect','authorize'].includes(process.argv[2])) {
  const {operator}=await import('./operator.mjs');await operator(process.argv.slice(2));
} else {
  await runCLI({commands,operations:{project:(_api,input)=>project(input.messages),watch:async api=>{
    const controller=new AbortController();
    for(const signal of ['SIGTERM','SIGINT'])process.once(signal,()=>controller.abort());
    await watch(api,{signal:controller.signal});
  }}});
}
