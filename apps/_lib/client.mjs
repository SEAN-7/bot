import { readFile } from 'node:fs/promises';
import { request } from 'node:http';

export function appClient(app, env = process.env) {
  if (env.BOT_PREFLIGHT_FILE) {
    let snapshot;
    return async (method, args = {}) => {
      snapshot ??= JSON.parse(await readFile(env.BOT_PREFLIGHT_FILE, 'utf8'));
      const options = snapshot.config.apps[app];
      if (!options?.enabled) throw new Error('Unknown or disabled app');
      const namespace = options.stateNamespace || `app:${app}`;
      if (method === 'config') return options;
      if (method === 'history') return [];
      if (method === 'state.get') {
        const row = snapshot.state.find(row => row.module === namespace && row.key === args.key);
        return row ? JSON.parse(row.value) : args.fallback ?? null;
      }
      if (method === 'state.set' || method === 'state.delete') {
        snapshot.state = snapshot.state.filter(row => row.module !== namespace || row.key !== args.key);
        if (method === 'state.set') snapshot.state.push({module: namespace, key: args.key, value: JSON.stringify(args.value)});
        return;
      }
      throw new Error('External action prohibited during preflight');
    };
  }
  return (method, args = {}) => new Promise((resolve, reject) => {
    if (!env.BOT_APP_URL || !env.BOT_APP_TOKEN) return reject(new Error('This command needs a supervised bot shell session.'));
    const body = JSON.stringify({ app, method, args, callId: env.BOT_CALL_ID });
    const endpoint = new URL(env.BOT_APP_URL);
    if(endpoint.protocol!=='http:'||endpoint.hostname!=='127.0.0.1')return reject(new Error('Invalid local app bridge'));
    const req = request(endpoint, { method: 'POST', headers: {
      authorization: `Bearer ${env.BOT_APP_TOKEN}`, 'content-type': 'application/json', 'content-length': Buffer.byteLength(body),
    } }, res => {
      let text = '';
      res.setEncoding('utf8');
      res.on('data', chunk => { text += chunk; if (text.length > 20_000_000) req.destroy(new Error('App response too large')); });
      res.on('end', () => { try { const result = JSON.parse(text); if (res.statusCode !== 200) reject(new Error(result.error)); else resolve(result.result); } catch (error) { reject(error); } });
    });
    req.setTimeout(15000, () => req.destroy(new Error('App bridge timed out; inspect uncertain side effects before retrying.')));
    req.on('error', reject); req.end(body);
  });
}
