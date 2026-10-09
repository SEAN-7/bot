import { readdir, readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

// Only tests translate historical fixture factories. The shipped runtime cannot load them.
export function fixtureSource({modules,apiVersion,...config}) {
  const enabled=Object.entries(modules||{}).filter(([,options])=>options.enabled);
  const model=enabled.find(([id])=>!['chat','memory','context','skills','scheduler'].includes(id))?.[0]||'demo-model';
  return {...config,apps:{...Object.fromEntries(Object.entries(modules||{}).map(([id,options])=>[id,{...options,stateNamespace:id}])),agent:{enabled:true,modelApp:model,contextApp:null,contributors:[],projections:modules?.chat?.enabled?['chat']:[],tickApps:[]}}};
}
export async function fixtureApps(directory) {
  for(const entry of await readdir(directory,{withFileTypes:true}))if(entry.isDirectory()) {
    const folder=join(directory,entry.name),manifest=JSON.parse(await readFile(join(folder,'module.json'),'utf8'));
    await writeFile(join(folder,'app.json'),JSON.stringify({id:manifest.id,version:manifest.version,description:'Isolated process fixture',command:['node','main.mjs'],preflight:['--preflight']}));
    await writeFile(join(folder,'SKILL.md'),`---\nname: ${manifest.id}\ndescription: Isolated process test fixture.\n---\n`);
    await writeFile(join(folder,'main.mjs'),`import factory from './index.mjs';
import {runCLI} from '../_lib/cli.mjs';
await runCLI({operations:{complete:async(api,input)=>{let model,context;const hooks=new Map();await factory({...api,registerModel:value=>model=value,registerContext:value=>context=value,on:(name,fn)=>hooks.set(name,fn)});await hooks.get('beforeModel')?.(input);if(context)await context({...input,model});const result=await model.complete(input);await hooks.get('afterTurn')?.();return result;}}});\n`);
    // Preflight evaluates the fixture's startup against scratch state, without provider completion.
    const main=await readFile(join(folder,'main.mjs'),'utf8');
    await writeFile(join(folder,'main.mjs'),main.replace("await runCLI({operations:","await runCLI({preflight:async(api)=>factory({...api,registerModel:()=>{},registerContext:()=>{},on:()=>{}}),operations:"));
  }
  return directory;
}
