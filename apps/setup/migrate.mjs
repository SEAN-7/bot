// Explicit app-owned configuration conversion. It never replaces app programs.
export function migrateConfig(config) {
  if(config.controller!=='agent'||config.apps.agent.hooks)return config;
  const result=structuredClone(config),agent=result.apps.agent;
  result.controllerProtocol=2;
  const model=agent.modelApp,context=agent.contextApp||'context';
  const prepare=agent.contextApp||agent.contributors?.length||agent.projections?.length;
  result.apps[model]={...result.model,...result.apps[model]};
  result.apps[context]={...result.apps[context],enabled:!!prepare,compaction:!!agent.contextApp,modelApp:model,contributors:agent.contributors||[],projections:agent.projections||[]};
  result.apps.agent={enabled:true,...(agent.stateNamespace?{stateNamespace:agent.stateNamespace}:{}),hooks:{...(prepare?{prepare:['bot-app',context,'prepare','--stdin']} : {}),complete:['bot-app',model,'complete','--stdin']},
    services:[...new Set([...(agent.services||[]),...(agent.tickApps||[])])].map(id=>['bot-app',id,'watch'])};
  result.apps['terminal-chat']={enabled:!!result.apps.chat?.enabled,adapter:['bot-app','chat','acp']};
  result.clients={chat:['node','apps/terminal-chat/main.mjs','chat'],acp:['node','apps/terminal-chat/main.mjs','acp']};
  result.preset=model==='demo-model'?'demo':'custom';delete result.model;
  return result;
}
