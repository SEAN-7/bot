import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, rm, symlink, cp } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import Ajv from 'ajv';
import { contribute } from '../apps/skills/behavior.mjs';
const skills=api=>api.on('beforeModel',()=>contribute(api));
import { commands as skillCommands } from '../apps/skills/commands.mjs';
import { readUsage, summarizeUsage } from '../apps/skills/usage.mjs';
import { createConfig } from '../src/cli.mjs';
import { setupChoices } from '../install/manage.mjs';
import { initialize, readCurrent } from '../src/releases.mjs';

async function fixture(t) {
  const workspace = await mkdtemp(join(tmpdir(), 'bot-skills-'));
  t.after(() => rm(workspace, { recursive: true, force: true }));
  const tools = new Map(), hooks = new Map();
  const appRoot=join(workspace,'apps');
  await mkdir(join(appRoot,'bot-docs'),{recursive:true});
  await writeFile(join(appRoot,'bot-docs','SKILL.md'),'---\nname: bot-docs\ndescription: Inspect bot capabilities.\n---\nUse bot-app skills usage for recorded usage; excludes context-summary model calls.');
  const api={ paths:{apps:appRoot,workspace},on:(name,fn)=>hooks.set(name,fn) };
  skills(api);
  const entries=skillCommands(api);
  for(const [name,key] of [['skills_list','list'],['skill_read','read']]) {const command=entries[key];tools.set(name,{...command,validate:new Ajv().compile(command.inputSchema)});}
  async function add(name, content) {
    await mkdir(join(workspace, 'skills', name), { recursive: true });
    await writeFile(join(workspace, 'skills', name, 'SKILL.md'), content);
  }
  const call = (name, args = {}) => tools.get(name).execute(args);
  return { workspace, tools, hooks, add, call };
}

test('bundled orientation is indexed on demand and documents shell usage inspection', async t => {
  const h = await fixture(t);
  const list = await h.call('skills_list');
  assert.deepEqual(list.map(skill => skill.name), ['bot-docs']);
  assert.equal(list[0].content, undefined);
  const doc = await h.call('skill_read', { name: 'bot-docs' });
  assert.match(doc.content, /bot-app skills usage/);
  assert.match(doc.content, /context-summary model calls/);
  assert.match(doc.path, /apps\/bot-docs\/SKILL\.md$/);
  assert.equal(h.tools.get('skill_read').validate({ name: '../control' }), false);
  await assert.rejects(h.call('skill_read', { name: 'absent' }), /Unknown skill/);
});

test('workspace skills appear immediately and index refreshes when guide contents change', async t => {
  const h = await fixture(t);
  const before = await h.hooks.get('beforeModel')();
  assert.deepEqual(await h.hooks.get('beforeModel')(), before);
  await h.add('reports', '---\nname: reports\ndescription: "Prepare reports."\n---\nGuide v1');
  const added = await h.hooks.get('beforeModel')();
  assert.notEqual(added[0].key, before[0].key);
  assert.deepEqual((await h.call('skills_list')).map(skill => skill.name), ['bot-docs', 'reports']);
  await h.add('reports', '---\nname: reports\ndescription: Prepare reports.\n---\nGuide v2');
  const changed = await h.hooks.get('beforeModel')();
  assert.notEqual(changed[0].key, added[0].key);
  assert.match((await h.call('skill_read', { name: 'reports' })).content, /Guide v2/);
});

test('skills reject duplicate names and mismatched frontmatter', async t => {
  const h = await fixture(t);
  await h.add('reports', '---\nname: something-else\ndescription: Wrong name.\n---\n');
  await assert.rejects(h.call('skills_list'), /match directory/);
  await rm(join(h.workspace, 'skills', 'reports'), { recursive: true });
  await h.add('bot-docs', '---\nname: bot-docs\ndescription: Duplicate.\n---\n');
  await assert.rejects(h.call('skills_list'), /Duplicate skill/);
});

test('skills reject escaped symlinks and oversized documents', async t => {
  const h = await fixture(t);
  await h.add('bad', 'x'.repeat(64_001));
  await assert.rejects(h.call('skills_list'), /too large/);
  await rm(join(h.workspace, 'skills', 'bad', 'SKILL.md'));
  await writeFile(join(h.workspace, 'outside.md'), 'private reference');
  await symlink(join(h.workspace, 'outside.md'), join(h.workspace, 'skills', 'bad', 'SKILL.md'));
  await assert.rejects(h.call('skills_list'), /escapes/);
});

test('usage counts only recorded assistant responses and distinguishes missing metrics', () => {
  const report = summarizeUsage([
    { role: 'user', content: 'secret', usage: { input: 1000 } },
    { id: 2, role: 'assistant', content: 'secret', usage: { input: 10, output: 5, totalTokens: 15, reasoning: 3, cost: { total: 0.02 } } },
    { id: 3, role: 'assistant', usage: { input: 20, output: 7, cacheRead: 50, totalTokens: 77, cost: { total: 0.03 } } },
    { role: 'assistant' },
  ]);
  assert.equal(report.responses, 3);
  assert.equal(report.withoutUsage, 1);
  assert.deepEqual(report.totals, { input: 30, output: 12, totalTokens: 92, reasoning: 3, cacheRead: 50 });
  assert.equal(report.reported.reasoning, 1);
  assert.equal(report.estimatedCost.total, 0.05);
  assert.equal(report.latest.id, 3);
  assert.ok(!JSON.stringify(report).includes('secret'));
  assert.deepEqual(summarizeUsage([]).totals, {});
  const observed=summarizeUsage([
    {role:'context',metadata:{modelObservation:true},usage:{input:5,output:2}},
    {role:'context',usage:{input:1000}},
  ]);
  assert.equal(observed.responses,1);
  assert.deepEqual(observed.totals,{input:5,output:2});
});

test('usage reader leaves journal unchanged and cannot create a missing database', async t => {
  const home = await mkdtemp(join(tmpdir(), 'bot-usage-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  await mkdir(join(home, 'control'));
  assert.throws(() => readUsage(home));
  const db = new DatabaseSync(join(home, 'control/journal.sqlite'));
  db.exec('CREATE TABLE messages(id INTEGER PRIMARY KEY, data TEXT NOT NULL)');
  db.prepare('INSERT INTO messages VALUES(?,?)').run(4, JSON.stringify({ role: 'assistant', usage: { input: 8, output: 2, totalTokens: 10 } }));
  const before = db.prepare('SELECT * FROM messages').all();
  assert.equal(readUsage(home).totals.totalTokens, 10);
  assert.deepEqual(db.prepare('SELECT * FROM messages').all(), before);
  db.close();
});

test('skills are enabled by default for source and managed installations', async () => {
  assert.equal(createConfig({}).apps.skills.enabled, true);
  assert.equal(createConfig({ enable: 'skills' }).apps.skills.enabled, true);
  assert.equal(createConfig({ disable: 'skills' }).apps.skills.enabled, false);
  assert.ok(!(await setupChoices({ yes: true }, { interactive: false })).disabled.includes('skills'));
  assert.ok(!(await setupChoices({ yes: true, enable: 'skills' }, { interactive: false })).disabled.includes('skills'));
  const questions = [];
  const selections = await setupChoices({ provider: 'demo' }, { interactive: true, question: async text => { questions.push(text); return text.includes('skills') ? 'y' : ''; } });
  assert.ok(questions.some(text => text === 'Enable skills? [Y/n]: '));
  assert.ok(!selections.disabled.includes('skills'));
});

test('disabled skills have no controller contribution or active package',async t=>{
  const home=await mkdtemp(join(tmpdir(),'bot-no-skills-'));t.after(()=>rm(home,{recursive:true,force:true}));
  await initialize(home,createConfig({disable:'chat,memory,context,scheduler,skills,web-search'}));
  const current=await readCurrent(home);assert.deepEqual(current.config.apps.agent.contributors,[]);
  await assert.rejects(import('node:fs/promises').then(fs=>fs.access(join(home,'revisions',current.revision,'apps','skills'))),{code:'ENOENT'});
});
