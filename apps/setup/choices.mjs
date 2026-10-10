import { createInterface } from 'node:readline/promises';
import { FEATURES, KEYS } from './config.mjs';
async function ask(question) {
  const rl = createInterface({ input: process.stdin, output: process.stderr });
  try { return (await rl.question(question)).trim(); } finally { rl.close(); }
}
async function secret(question) {
  if (!process.stdin.isTTY) throw new Error('Provide credentials in the environment for noninteractive setup');
  process.stderr.write(question);
  const prior = process.stdin.isRaw;
  process.stdin.setRawMode(true); process.stdin.resume();
  return new Promise((resolveSecret, reject) => {
    let text = '';
    const finish = (error) => {
      process.stdin.removeListener('data', onData); process.stdin.setRawMode(prior); process.stdin.pause(); process.stderr.write('\n');
      if (error) reject(error); else resolveSecret(text);
    };
    const onData = chunk => {
      for (const char of chunk.toString()) {
        if (char === '\u0003') { finish(new Error('Setup cancelled')); return; }
        if (char === '\r' || char === '\n') { finish(); return; }
        if (char === '\u007f' || char === '\b') text = text.slice(0, -1);
        else if (char >= ' ') text += char;
      }
    };
    process.stdin.on('data', onData);
  });
}
export async function setupChoices(options, { env = process.env, question = ask, password = secret, interactive = process.stdin.isTTY, print = console.error, catalogue = [] } = {}) {
  let provider = options.provider, model = options.model;
  const disabled = new Set([ ...(options.disable || '').split(',').filter(Boolean)]);
  for (const feature of disabled) if (!FEATURES.includes(feature)) throw new Error(`Unknown feature ${feature}`);
  for (const feature of (options.enable || '').split(',').filter(Boolean)) {
    if (!FEATURES.includes(feature)) throw new Error(`Unknown feature ${feature}`);
    disabled.delete(feature);
  }
  if (!provider && !options.yes && interactive) {
    print('Providers: demo (no paid calls), ' + [...new Set(catalogue.map(item => item.provider))].join(', '));
    provider = await question('Provider [demo]: ') || 'demo';
  }
  provider ||= 'demo';
  if (!/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(provider)) throw new Error('Invalid provider');
  if (provider !== 'demo' && !model && interactive && !options.yes) {
    const available = catalogue.filter(item => item.provider === provider);
    print(available.map((item, index) => `${index + 1}. ${item.id}${item.name ? ' — ' + item.name : ''}`).join('\n'));
    const selection = await question('Model ID or number: ');
    model = available[Number(selection) - 1]?.id || selection;
  }
  if (provider !== 'demo' && catalogue.length && !catalogue.some(item => item.provider === provider && item.id === model)) throw new Error('Provider/model combination is not in the bundled registry');
  if (provider !== 'demo' && (!model || /[\r\n\0]/.test(model))) throw new Error('A real provider requires --model or an interactive model selection');
  if (!options.yes && interactive) for (const feature of FEATURES) {
    const answer = await question(`Enable ${feature}? ${disabled.has(feature) ? '[y/N]' : '[Y/n]'}: `);
    if (/^n(?:o)?$/i.test(answer)) disabled.add(feature);
    if (/^y(?:es)?$/i.test(answer)) disabled.delete(feature);
  }
  const credentials = {};
  if (provider !== 'demo') {
    const key = options['credential-env'] || KEYS[provider];
    if (!key || !/^[A-Z][A-Z0-9_]{0,63}$/.test(key) || ['GH_TOKEN', 'GITHUB_TOKEN', 'BOT_CONTROL_TOKEN', 'NODE_OPTIONS'].includes(key)) throw new Error('For this provider specify --credential-env with its API-key variable name');
    const value = env[key] || (interactive && !options.yes ? await password(`${key} (hidden): `) : '');
    if (!value) throw new Error(`Provide ${key} in the environment`);
    credentials[key] = value;
  }
  for(const value of Object.values(credentials))if(/[\x00-\x1f\x7f]/.test(value))throw new Error('Invalid credential environment value');
  return { provider, model: provider === 'demo' ? 'deterministic-demo' : model, disabled: [...disabled], credentials };
}
