import { summarizeUsage } from './usage.mjs';
import { discover } from './discovery.mjs';
export function commands(api) {
  const read = () => discover([api.paths.apps, api.paths.workspace + '/skills']);
  return {
    usage: { description: "Summarize provider-reported usage and cost for saved conversation responses; excludes context-summary calls. Missing metrics are unavailable, not zero.", inputSchema:{type:"object",properties:{},additionalProperties:false}, execute:async()=>summarizeUsage(await api.history()) },
    list: { description: 'List local skills and their paths. Read a matching guide through shell before using a capability.', inputSchema: { type: 'object', properties: {}, additionalProperties: false }, execute: async () => (await read()).map(({content,...entry})=>entry) },
    read: { description: 'Read one local skill by name.', inputSchema: { type: 'object', properties: { name: {type:'string',pattern:'^[a-z][a-z0-9-]{0,63}$'} }, required:['name'],additionalProperties:false }, execute: async ({name}) => { const result=(await read()).find(entry=>entry.name===name); if(!result)throw new Error('Unknown skill: '+name); return result; } },
  };
}
