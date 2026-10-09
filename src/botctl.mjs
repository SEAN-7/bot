import { appClient } from './app-client.mjs';
const [app, operation, argument = '{}', ...extra] = process.argv.slice(2);
try {
  if (!app || app === '--help') console.log('Usage: botctl APP OPERATION [JSON | --stdin]\nOperations: config, history, state.get, state.set, state.delete, publish, enqueue, update.request.\nOnly durable app primitives are available; human input and operator control are separate.');
  else {
    if (extra.length) throw new Error('Pass one JSON argument or --stdin');
    let input = argument;
    if (argument === '--stdin') { input = ''; for await (const chunk of process.stdin) { input += chunk; if (input.length > 150000) throw new Error('Request too large'); } }
    console.log(JSON.stringify(await appClient(app)(operation, JSON.parse(input)) ?? null));
  }
} catch (error) { console.error(`botctl: ${error.message}`); process.exitCode = 1; }
