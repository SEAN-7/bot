import { runCLI } from '../_lib/cli.mjs';
import { createModel } from './behavior.mjs';
await runCLI({preflight:api=>createModel(api.config).describe({}),operations:{complete:(api,input)=>createModel(api.config).complete(input),describe:(api,input)=>createModel(api.config).describe?.(input.settings)??null,list:(api)=>createModel(api.config).listModels?.()??[]}});
