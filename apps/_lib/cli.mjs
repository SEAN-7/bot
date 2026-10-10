// Optional library used by the bundled JavaScript apps. Other apps can use any CLI/language.
import Ajv from 'ajv';
import { appClient } from './client.mjs';
import { basename, dirname, join } from 'node:path';

export async function runCLI({commands = () => ({}), operations = {}, preflight}) {
  try {
    const [command, argument = '{}', ...extra] = process.argv.slice(2);
    const folder = dirname(process.argv[1]), id = basename(folder);
    const rpc = appClient(id);
    const config = await rpc('config');
    const api = { config, paths: { module: folder, workspace: process.env.BOT_WORKSPACE, drafts: process.env.BOT_DRAFTS, apps: process.env.BOT_APPS },
      state: { get: (key, fallback) => rpc('state.get', {key, fallback}), set: (key, value) => rpc('state.set', {key, value}), delete: key => rpc('state.delete', {key}) },
      history: () => rpc('history'), publish: content => rpc('publish', {content}), enqueue: event => rpc('enqueue', {event}), proposeUpdate: () => rpc('update.request'),
    };
    const entries = commands(api), ajv = new Ajv({allErrors:true,strict:false});
    for (const entry of Object.values(entries)) entry.validate = ajv.compile(entry.inputSchema || {type:'object'});
    if (command === '--preflight') {await preflight?.(api);return;}
    if (!command || command === '--help') {
      console.log(JSON.stringify({app:id,commands:Object.fromEntries(Object.entries(entries).map(([key, entry])=>[key,{description:entry.description,arguments:entry.inputSchema,...(entry.inputFormat?{inputFormat:entry.inputFormat}:{}),...(entry.outputFormat?{outputFormat:entry.outputFormat}:{})}])),operations:Object.keys(operations)},null,2));return;
    }
    if (extra.length) throw new Error('Pass one JSON object or --stdin');
    let input = argument;
    if (argument === '--stdin') { input = ''; process.stdin.setEncoding('utf8'); for await (const chunk of process.stdin) { input += chunk; if (input.length > 20_000_000) throw new Error('Input too large'); } }
    const entry = entries[command];
    if (entry?.inputFormat === 'text' && argument !== '--stdin') throw new Error('Pass Markdown through --stdin');
    const data = entry?.inputFormat === 'text' ? input : JSON.parse(input);
    if (entry && !entry.validate(data)) throw new Error('Invalid arguments: '+ajv.errorsText(entry.validate.errors));
    if (!entry && !operations[command]) throw new Error('Unknown command: '+command);
    const result = entry ? await entry.execute(data,{signal:new AbortController().signal,callId:process.env.BOT_CALL_ID}) : await operations[command](api,data);
    if (entry?.outputFormat === 'text') process.stdout.write(result);
    else console.log(JSON.stringify(result ?? null));
  } catch (error) { console.error(JSON.stringify({error:error.message,retryable:!!error.retryable,blocked:!!error.blocked})); process.exitCode = 1; }
}
