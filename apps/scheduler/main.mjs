import { runCLI } from '../_lib/cli.mjs';
import { commands } from './commands.mjs';
import { tick, watch } from './behavior.mjs';
import { transaction } from './database.mjs';
await runCLI({
  commands:api=>Object.fromEntries(Object.entries(commands(api)).map(([name,entry])=>[name,{...entry,execute:(input,context)=>transaction(api,locked=>commands(locked)[name].execute(input,context))}])),
  operations:{
    tick:api=>transaction(api,locked=>tick(locked)),
    watch:async api=>{
      const controller=new AbortController();for(const signal of ['SIGINT','SIGTERM'])process.once(signal,()=>controller.abort());
      await watch(api,{signal:controller.signal,run:()=>transaction(api,locked=>tick(locked,{signal:controller.signal}))});
    },
  },
});
