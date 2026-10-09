import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import Ajv from 'ajv';
import { commands, parseResults } from '../apps/web-search/commands.mjs';
import { createConfig } from '../src/cli.mjs';
import { setupChoices } from '../install/manage.mjs';
import { initialize, readCurrent } from '../src/releases.mjs';

// DuckDuckGo Lite's result-link/result-snippet structure, including its redirect URLs.
const hit = (url, title, description = '') => `<tr><td><a rel="nofollow" href="${url}" class='result-link'>${title}</a></td></tr><tr><td class='result-snippet'>${description}</td></tr>`;
const HTML = hit('//duckduckgo.com/l/?uddg=https%3A%2F%2Fnodejs.org%2Fapi%2F&amp;rut=unused', 'Node.js &amp; API', 'Read <b>documentation</b> &#x2014; &#39;official&#39;.')
  + hit('https://nodejs.org/api/', 'Duplicate', 'Skip it')
  + hit('javascript:alert(1)', 'Invalid')
  + hit('https://example.org/docs?q=a&amp;b=2', 'Other docs', 'Second result');

function fixture(request, config = {}) {
  const tool = commands({ config }, { fetch: request }).query;
  return { get tool() { return tool; }, call: (args, signal = new AbortController().signal) => tool.execute(args, { signal }) };
}

test('search normalizes snippets, decodes redirect links, deduplicates and caps results', async () => {
  let calls = 0;
  const h = fixture(async (url, options) => {
    calls++;
    assert.equal(url.origin, 'https://lite.duckduckgo.com');
    assert.equal(url.searchParams.get('q'), 'node.js & docs');
    assert.equal(options.redirect, 'error');
    assert.equal(options.signal.aborted, false);
    return new Response(HTML);
  });
  assert.equal(calls, 0, 'Registration must not perform network requests');
  const result = await h.call({ query: '  node.js & docs  ' });
  assert.deepEqual(result, { provider: 'duckduckgo', query: 'node.js & docs', untrusted: true, results: [
    { title: 'Node.js & API', url: 'https://nodejs.org/api/', description: "Read documentation — 'official'.", position: 1 },
    { title: 'Other docs', url: 'https://example.org/docs?q=a&b=2', description: 'Second result', position: 2 },
  ] });
  assert.equal(parseResults(HTML, 1).length, 1);
  assert.equal(new Ajv().compile(h.tool.inputSchema)({ query: 'x', limit: 11 }), false);
  assert.equal(new Ajv().compile(h.tool.inputSchema)({ query: 'x', arbitrary: true }), false);
  await assert.rejects(h.call({ query: '   ' }), /Invalid search/);
  assert.equal(calls, 1);
});

test('search distinguishes no matches from challenge pages, HTTP failures and markup changes', async () => {
  assert.deepEqual(parseResults('<p>No results found for this query</p>', 5), []);
  for (const status of [202, 403, 429, 500]) {
    await assert.rejects(fixture(async () => new Response('failure', { status })).call({ query: 'x' }), /blocked|rate-limited|HTTP 500/);
  }
  await assert.rejects(fixture(async () => new Response('<form id="challenge-form">No results</form>')).call({ query: 'x' }), /bot challenge/);
  await assert.rejects(fixture(async () => new Response('<html>New layout</html>')).call({ query: 'x' }), /no recognizable results/);
  await assert.rejects(fixture(async () => { throw new Error('network unavailable'); }).call({ query: 'x' }), /network unavailable/);
});

test('search bounds streamed response bytes and cleans up the reader', async () => {
  let cancelled = false;
  const h = fixture(async () => new Response(new ReadableStream({
    start(controller) { controller.enqueue(new Uint8Array(1100)); },
    cancel() { cancelled = true; },
  })), { maxResponseBytes: 1024 });
  await assert.rejects(h.call({ query: 'x' }), /byte limit/);
  assert.equal(cancelled, true);
  const bounded = parseResults(hit('https://example.org/', 'a'.repeat(1000), 'b'.repeat(3000)), 5)[0];
  assert.equal(bounded.title.length, 500);
  assert.equal(bounded.description.length, 1500);
});

test('search honors cancellation and bounds request and body time', async () => {
  const request = (_url, { signal }) => new Promise((resolve, reject) => signal.addEventListener('abort', () => reject(signal.reason), { once: true }));
  const controller = new AbortController();
  controller.abort();
  await assert.rejects(fixture(() => assert.fail('Already aborted')).call({ query: 'x' }, controller.signal), { name: 'AbortError' });
  const next = new AbortController();
  const pending = fixture(request).call({ query: 'x' }, next.signal);
  next.abort();
  await assert.rejects(pending, { name: 'AbortError' });
  // AbortSignal.timeout is unref'ed, so keep the test alive while awaiting it.
  const keepAlive = setInterval(() => {}, 1000);
  try {
    await assert.rejects(fixture(request, { timeoutMs: 100 }).call({ query: 'x' }), /timed out/);
    await assert.rejects(fixture(async (_url, { signal }) => new Response(new ReadableStream({
      start(stream) { signal.addEventListener('abort', () => stream.error(signal.reason), { once: true }); },
    })), { timeoutMs: 100 }).call({ query: 'x' }), /timed out/);
  } finally { clearInterval(keepAlive); }
});

test('new source and managed setups enable the bundled search plugin and honor opt-out', async t => {
  assert.equal(createConfig({}).apps['web-search'].enabled, true);
  assert.equal(createConfig({ disable: 'web-search' }).apps['web-search'].enabled, false);
  assert.ok(!(await setupChoices({ yes: true }, { interactive: false })).disabled.includes('web-search'));
  assert.ok((await setupChoices({ yes: true, disable: 'web-search' }, { interactive: false })).disabled.includes('web-search'));
  const home = await mkdtemp(join(tmpdir(), 'bot-web-search-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  await initialize(home, createConfig({}));
  const current = await readCurrent(home), revisionDir = join(home, 'revisions', current.revision);
  assert.equal(JSON.parse(await readFile(join(revisionDir, 'apps/web-search/app.json'))).id, 'web-search');
  assert.equal(current.config.modules,undefined);
  assert.equal(current.config.controller,'agent');
});
