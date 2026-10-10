import { runCLI } from '../_lib/cli.mjs';
import { commands } from './commands.mjs';
import { prepareContext } from './prepare.mjs';
import { prepareSnapshot } from './request.mjs';
import { invoke } from '../_lib/invoke.mjs';
import { appClient } from '../_lib/client.mjs';
await runCLI({commands,operations:{prepare:async(api,input)=>prepareSnapshot({
  options:api.config,tools:input.tools,settings:input.settings||{},state:api.state,invoke,
  rpc:(method,args)=>method==='history'?api.history():appClient(args.module)('contribute',{key:args.key,content:args.content}),
  prepare:api.config.compaction===false?null:request=>prepareContext(api,{...request,model:{
    describe:settings=>invoke(api.config.modelApp,'describe',{settings}),
    complete:request=>invoke(api.config.modelApp,'complete',request),
  }}),
})}});
