import { runCLI } from '../_lib/cli.mjs';
import { commands } from './commands.mjs';
import { createContext } from './behavior.mjs';
import { invoke } from '../_lib/invoke.mjs';
await runCLI({commands,operations:{prepare:async(api,input)=>createContext(api)({...input,model:{describe:settings=>invoke(input.modelApp,'describe',{settings}),complete:request=>invoke(input.modelApp,'complete',request)}})}});
