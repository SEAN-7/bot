import { createHash, randomUUID } from 'node:crypto';
import { mkdir, readFile, writeFile, rename, link, rm } from 'node:fs/promises';
import { join } from 'node:path';

const digest = text => createHash('sha256').update(text).digest('hex');
const filename = api => join(api.paths.workspace, 'memory', 'INDEX.md');

export function measure(api, markdown) {
  const maxTokens = api.config.maxIndexTokens ?? 1000;
  if (!Number.isInteger(maxTokens) || maxTokens < 128 || maxTokens > 10000) throw new Error('maxIndexTokens must be an integer from 128 to 10000');
  if (typeof markdown !== 'string' || markdown.includes('\0')) throw new Error('Index must be Markdown text without NUL characters');
  const bytes = Buffer.byteLength(markdown, 'utf8'), estimatedTokens = Math.ceil(bytes / 4);
  return { estimatedTokens, maxTokens, bytes, estimator: 'ceil(UTF-8 bytes / 4)', fits: estimatedTokens <= maxTokens };
}

function validate(api, markdown) {
  const usage = measure(api, markdown);
  if (!usage.fits) throw new Error(`Index exceeds its ${usage.maxTokens} estimated-token budget by ${usage.estimatedTokens - usage.maxTokens}. Move details into linked notes and retry; the accepted index was not replaced.`);
  return usage;
}

// A complete file becomes visible in one filesystem operation. Initialization
// does not replace an index another invocation created while we were reading.
async function save(path, markdown, initial = false) {
  const temporary = `${path}.${randomUUID()}.tmp`;
  try {
    await writeFile(temporary, markdown, { mode: 0o600 });
    if (initial) { try { await link(temporary, path); } catch (error) { if (error.code !== 'EEXIST') throw error; } }
    else await rename(temporary, path);
  } finally { await rm(temporary, { force: true }); }
}

export async function readIndex(api) {
  const path = filename(api);
  let markdown;
  try { markdown = await readFile(path, 'utf8'); }
  catch (error) {
    if (error.code !== 'ENOENT') throw error;
    await mkdir(join(api.paths.workspace, 'memory'), { recursive: true, mode: 0o700 });
    const legacy = await api.state.get('memory', null);
    markdown = '# Memory index\n';
    if (legacy?.USER || legacy?.notes) {
      const detail = `# Previously saved memory\n\n## Preferences\n${legacy.USER || ''}\n\n## Notes\n${legacy.notes || ''}\n`;
      const archive = `legacy-${digest(JSON.stringify(legacy)).slice(0, 16)}.md`;
      await save(join(api.paths.workspace, 'memory', archive), detail, true);
      const pointer = `\n- Previous preferences and working notes: [saved memory](memory/${archive}).\n`;
      // Keep small existing facts visible; oversized legacy sections remain
      // intact in the linked file and original state, never silently truncated.
      markdown += pointer;
      if (measure(api, markdown + '\n' + detail).fits) markdown += '\n' + detail;
    }
    validate(api, markdown);
    await save(path, markdown, true);
    markdown = await readFile(path, 'utf8');
  }
  return { path, markdown, hash: digest(markdown), ...validate(api, markdown) };
}

export async function writeIndex(api, markdown) {
  validate(api, markdown);
  // Preserve legacy data even when the first operation replaces the index.
  try { await readFile(filename(api)); }
  catch (error) { if (error.code !== 'ENOENT') throw error; await readIndex(api); }
  await save(filename(api), markdown);
  return { path: filename(api), hash: digest(markdown), ...measure(api, markdown) };
}
