import { readFile, writeFile, rename } from 'node:fs/promises';
import { join } from 'node:path';
import { randomUUID } from 'node:crypto';
import { createConfig, configure } from './config.mjs';
import { migrateConfig } from './migrate.mjs';
import { setupChoices } from './choices.mjs';
const [mode,argument='{}',destination]=process.argv.slice(2);
try {
  const options=JSON.parse(argument);
  if(mode==='config') {
    let selection=options;
    if(!options.yes&&process.stdin.isTTY&&!options.provider) {
      const {builtinModels}=await import('@earendil-works/pi-ai/providers/all');
      const picked=await setupChoices(options,{catalogue:builtinModels().getModels(),password:async()=>{throw new Error('Source setup uses exported provider credentials; set the API-key environment variable before setup and serve.');}});
      selection={...options,provider:picked.provider,model:picked.model,disable:picked.disabled.join(',')};
      Object.assign(process.env,picked.credentials);
    }
    console.log(JSON.stringify(createConfig(selection)));
  } else if(mode==='choices') {
    const {builtinModels}=await import('@earendil-works/pi-ai/providers/all');
    const result=await setupChoices(options,{catalogue:builtinModels().getModels()});
    await writeFile(destination,JSON.stringify(result),{mode:0o600,flag:'wx'});
  } else if(mode==='configure'||mode==='migrate') {
    const file=join(destination,'drafts','config.json');
    const existing=JSON.parse(await readFile(file,'utf8'));
    const next=mode==='migrate'?migrateConfig(existing):configure(existing,{...options,credentials:process.env});
    const temporary=`${file}.${randomUUID()}.tmp`;
    await writeFile(temporary,JSON.stringify(next,null,2)+'\n',{mode:0o600});await rename(temporary,file);
  } else if(mode==='--help')console.log('setup config [JSON] | choices [JSON] OUTPUT | configure [JSON] HOME | migrate {} HOME');
  else throw new Error('Unknown setup command');
} catch(error){console.error(error.message);process.exitCode=1;}
