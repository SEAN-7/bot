import { runCLI } from '../_lib/cli.mjs';
import { createModel } from './behavior.mjs';
await runCLI({operations:{complete:(api,input)=>createModel(api).complete(input),describe:(api,input)=>createModel(api).describe?.(input.settings)??null,list:(api)=>createModel(api).listModels?.()??[]}});
