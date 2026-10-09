import { readFile, access, realpath } from 'node:fs/promises';
import { join, relative, isAbsolute } from 'node:path';

export const APP_ID = /^[a-z][a-z0-9-]{0,63}$/;
export function commandAt(folder, command, env = process.env) {
  if (!Array.isArray(command) || !command.length || command.length > 32 || command.some(part => typeof part !== 'string' || !part || part.includes('\0'))) throw new Error('App command must be a nonempty argv array');
  return { command: command[0] === 'node' ? (env.BOT_NODE || process.execPath) : command[0], args: command.slice(1), cwd: folder };
}
export async function appManifest(folder) {
  const manifest = JSON.parse(await readFile(join(folder, 'app.json'), 'utf8'));
  if (!APP_ID.test(manifest.id) || typeof manifest.version !== 'string' || !manifest.version || manifest.entry || manifest.apiVersion) throw new Error('Invalid app manifest; JavaScript module entries are unsupported');
  commandAt(folder, manifest.command);
  if (manifest.preflight !== undefined) {
    if (!Array.isArray(manifest.preflight)) throw new Error('App preflight must be an argv array');
    commandAt(folder, ['check', ...manifest.preflight]);
  }
  if (manifest.acp !== undefined) commandAt(folder, manifest.acp);
  const skill = await realpath(join(folder, 'SKILL.md'));
  if (isAbsolute(relative(await realpath(folder), skill)) || relative(await realpath(folder), skill).startsWith('..')) throw new Error('Skill escapes its app');
  await access(skill);
  return manifest;
}
