export const FEATURES = ['chat', 'memory', 'context', 'scheduler', 'skills', 'web-search'];
export const KEYS = { openai: 'OPENAI_API_KEY', anthropic: 'ANTHROPIC_API_KEY', google: 'GEMINI_API_KEY', groq: 'GROQ_API_KEY', openrouter: 'OPENROUTER_API_KEY', mistral: 'MISTRAL_API_KEY', deepseek: 'DEEPSEEK_API_KEY', xai: 'XAI_API_KEY', cerebras: 'CEREBRAS_API_KEY' };
export function createConfig(options, env = process.env) {
  const provider = options.provider || 'demo';
  const model = provider === 'demo' ? 'deterministic-demo' : options.model;
  if (!model) throw new Error('Choose an explicit model with --model MODEL when using a real provider.');
  const credentialNames = KEYS[provider] ? [KEYS[provider]] : null;
  if (credentialNames && !credentialNames.some(name => env[name])) {
    throw new Error(`Provider ${provider} requires ${credentialNames.join(' or ')} in your environment. Setup never stores API keys.`);
  }
  const apps = Object.fromEntries(FEATURES.map(id => [id, { enabled: true }]));
  for (const [flag, enabled] of [['disable', false], ['enable', true]]) {
    for (const id of (options[flag] || '').split(',').filter(Boolean)) {
      if (!FEATURES.includes(id)) throw new Error(`Unknown feature ${id}. Choose ${FEATURES.join(', ')}.`);
      apps[id] = { enabled };
    }
  }
  apps.updates={enabled:true};
  apps['model-provider']={enabled:provider!=='demo',...(provider!=='demo'?{provider,model}:{})};
  apps['demo-model']={enabled:provider==='demo'};
  const modelApp=provider==='demo'?'demo-model':'model-provider';
  const compact=apps.context.enabled;
  apps.context={...apps.context,enabled:compact||apps.memory.enabled||apps.skills.enabled||apps.chat.enabled,compaction:compact,modelApp,
    contributors:['memory','skills'].filter(id=>apps[id].enabled),projections:apps.chat.enabled?['chat']:[]};
  apps.agent={enabled:true,hooks:{...(apps.context.enabled?{prepare:['bot-app','context','prepare','--stdin']} : {}),complete:['bot-app',modelApp,'complete','--stdin']},
    services:['chat','scheduler'].filter(id=>apps[id].enabled).map(id=>['bot-app',id,'watch'])};
  apps['terminal-chat']={enabled:apps.chat.enabled,adapter:['bot-app','chat','acp']};
  const port = Number(options.port || 4317);
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('--port must be an integer from 1 to 65535.');
  return {
    apiVersion: 3, appUpdates: 'direct', controller: 'agent', apps, preset:provider==='demo'?'demo':'custom', clients:{chat:['node','apps/terminal-chat/main.mjs','chat'],acp:['node','apps/terminal-chat/main.mjs','acp']},
    runtime: { port, startupTimeoutMs: 10000, heartbeatTimeoutMs: 10000, operationTimeoutMs: 120000, trialMs: 3000, maxRestarts: 2, retryMs: 3000, sandbox: 'auto' },
  };
}

export function configure(existing,selections) {
  const next=createConfig({provider:selections.provider,model:selections.model,disable:selections.disabled.join(',')},selections.credentials);
  const apps={...existing.apps};
  for(const [id,settings] of Object.entries(next.apps))apps[id]={...apps[id],...settings};
  const result={...existing,...next,apps,runtime:existing.runtime||next.runtime};
  delete result.model;delete result.modules;
  return result;
}
