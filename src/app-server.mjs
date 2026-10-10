import { createServer } from 'node:http';
import { randomBytes } from 'node:crypto';

// A worker-local bridge exposes durable primitives, never operator/core control.
export async function startAppServer({ workspace, rpc }) {
  const token = randomBytes(32).toString('hex');
  const allowed = new Set(['config', 'state.get', 'state.set', 'state.delete', 'history', 'contribute', 'publish', 'enqueue', 'update.request']);
  const server = createServer((request, response) => {
    const respond = (code, data) => { response.writeHead(code, { 'content-type': 'application/json' }); response.end(JSON.stringify(data)); };
    (async () => {
      if (request.method !== 'POST' || request.headers.authorization !== `Bearer ${token}`) return respond(403, { error: 'App authentication required' });
      let body = ''; request.setEncoding('utf8');
      for await (const chunk of request) { body += chunk; if (body.length > 150000) throw new Error('App request too large'); }
      const data = JSON.parse(body);
      if (!/^[a-z][a-z0-9-]{0,63}$/.test(data.app) || !allowed.has(data.method)) throw new Error('Unsupported app operation');
      respond(200, { result: await rpc('app.request', data) });
    })().catch(error => respond(400, { error: error.message }));
  });
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
  return { env: { BOT_APP_URL: `http://127.0.0.1:${server.address().port}`, BOT_APP_TOKEN: token }, close: async () => { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); } };
}
