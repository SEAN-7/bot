import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, cp, symlink, writeFile, readFile, readdir, rm } from 'node:fs/promises';
import { join, relative } from 'node:path';
import { tmpdir } from 'node:os';
import { CORE_ROOT, initialize, stageCore, stageDrafts, readCurrent, readJSON, metadataPath } from '../src/releases.mjs';

test('operator core staging validates the API and snapshots trusted installed source', async t => {
  const directory = await mkdtemp(join(tmpdir(), 'bot-core-update-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const home = join(directory, 'home');
  const defaultsDir = join(directory, 'defaults');
  await mkdir(defaultsDir);
  await initialize(home, { modules: {}, runtime: { sandbox: 'off' } }, { defaultsDir });
  const original = await readCurrent(home);
  const source = join(directory, 'installed-core');
  await mkdir(source);
  await cp(join(CORE_ROOT, 'src'), join(source, 'src'), { recursive: true });
  const pkg = await readJSON(join(CORE_ROOT, 'package.json'));
  await writeFile(join(source, 'package.json'), JSON.stringify({ ...pkg, botCoreApi: 999 }));
  await symlink(join(CORE_ROOT, 'node_modules'), join(source, 'node_modules'), 'dir');
  const beforeCores = await readdir(join(home, 'cores'));
  await assert.rejects(stageCore(home, source), /API is incompatible/);
  assert.deepEqual(await readdir(join(home, 'cores')), beforeCores);
  assert.equal((await readJSON(metadataPath(home))).pending, null);

  await writeFile(join(source, 'package.json'), JSON.stringify(pkg));
  const runtime = await readFile(join(source, 'src', 'runtime.mjs'), 'utf8');
  const revision = await stageCore(home, source);
  const candidate = await readJSON(join(home, 'revisions', revision, 'config.json'));
  assert.notEqual(candidate.coreRoot, original.coreRoot);
  assert.ok(!relative(join(home, 'cores'), candidate.coreRoot).startsWith('..'));
  await writeFile(join(source, 'src', 'runtime.mjs'), '// changed operator installation\n');
  assert.equal(await readFile(join(candidate.coreRoot, 'src', 'runtime.mjs'), 'utf8'), runtime);
  assert.equal((await readCurrent(home)).revision, original.revision);

  const metadata = await readJSON(metadataPath(home));
  await writeFile(metadataPath(home), JSON.stringify({ ...metadata, pending: null }));
  const writableSource = join(home, 'workspace', 'untrusted-core');
  await mkdir(writableSource);
  await cp(join(source, 'src'), join(writableSource, 'src'), { recursive: true });
  await writeFile(join(writableSource, 'package.json'), JSON.stringify(pkg));
  await symlink(join(CORE_ROOT, 'node_modules'), join(writableSource, 'node_modules'), 'dir');
  await assert.rejects(stageCore(home, writableSource), /agent-writable/);
  const draftPath = join(home, 'drafts', 'config.json');
  const draft = await readJSON(draftPath);
  await writeFile(draftPath, JSON.stringify({ ...draft, coreRoot: writableSource }));
  await assert.rejects(stageDrafts(home), /Only the operator/);
});
