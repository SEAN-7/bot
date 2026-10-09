import { readdir, readFile, realpath, stat } from 'node:fs/promises';
import { join, relative, isAbsolute } from 'node:path';
import { createHash } from 'node:crypto';

const NAME = /^[a-z][a-z0-9-]{0,63}$/;
const inside = (root, path) => { const part = relative(root, path); return !isAbsolute(part) && part !== '..' && !part.startsWith('../'); };
const MAX_BYTES = 64_000;

// Deliberately small frontmatter format: single-line name and description.
export function metadata(content) {
  const header = /^---\r?\n([\s\S]*?)\r?\n---(?:\r?\n|$)/.exec(content)?.[1];
  if (!header) throw new Error('SKILL.md needs name and description frontmatter');
  const field = key => {
    const value = new RegExp(`^${key}:\\s*(.+)$`, 'm').exec(header)?.[1]?.trim();
    return value?.replace(/^(["'])(.*)\1$/, '$2');
  };
  const name = field('name'), description = field('description');
  if (!NAME.test(name ?? '') || !description || description.length > 1000) throw new Error('Invalid skill name or description');
  return { name, description };
}

export async function discover(roots, enabledApps = null) {
    const found = new Map();
    for (const root of roots) {
      let directory, entries;
      try { directory = await realpath(root); entries = await readdir(directory, { withFileTypes: true }); }
      catch (error) { if (error.code === 'ENOENT') continue; throw error; }
      for (const entry of entries.sort((a, b) => a.name.localeCompare(b.name))) {
        if (!entry.isDirectory() || !NAME.test(entry.name)) continue;
        if (enabledApps && root.endsWith('/apps') && !enabledApps[entry.name]?.enabled) continue;
        const path = await realpath(join(directory, entry.name, 'SKILL.md')).catch(error => {
          if (error.code === 'ENOENT') return null;
          throw error;
        });
        if (!path) continue;
        if (!inside(directory, path)) throw new Error(`Skill escapes its directory: ${entry.name}`);
        if ((await stat(path)).size > MAX_BYTES) throw new Error(`Skill is too large: ${entry.name}`);
        const content = await readFile(path, 'utf8');
        const info = metadata(content);
        if (info.name !== entry.name) throw new Error(`Skill name must match directory: ${entry.name}`);
        if (found.has(info.name)) throw new Error(`Duplicate skill: ${info.name}`);
        found.set(info.name, { ...info, path, content });
      }
    }
    return [...found.values()];
  }
