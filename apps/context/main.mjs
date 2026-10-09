import { runCLI } from '../_lib/cli.mjs';
import { commands } from './commands.mjs';
import { prepareContext } from './prepare.mjs';
import { invoke } from '../_lib/invoke.mjs';
await runCLI({commands,operations:{prepare:async(api,input)=>prepareContext(api,{...input,model:{describe:settings=>invoke(input.modelApp,'describe',{settings}),complete:request=>invoke(input.modelApp,'complete',request)}})}});
