import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, symlink, mkdir, writeFile, readFile, lstat, rm } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { tmpdir } from 'node:os';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
const exec = promisify(execFile);
const source = dirname(dirname(fileURLToPath(import.meta.url)));
async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), 'bot-entrypoint-symlink-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const current = join(root, 'current');
  await symlink(source, current, 'dir');
  return { root, current };
}
for (const [name, path, arg, pattern] of [
  ['CLI', 'src/cli.mjs', '--help', /Usage:|setup\s+Create/],
  ['installer manager', 'install/manage.mjs', 'help', /Usage: bot COMMAND/]
]) {
  test(`${name} executes when invoked through the installed current directory symlink`, async t => {
    const { current } = await fixture(t);
    const { stdout } = await exec(process.execPath, [join(current, path), arg], { timeout: 10000 });
    assert.match(stdout, pattern);
  });
}

test('persistent state materializer executes through a managed current symlink', async t => {
  const { root, current } = await fixture(t);
  const core = join(root, 'state/cores/core-1');
  const dependencies = join(root, 'release/node_modules');
  await mkdir(core, { recursive: true });
  await mkdir(dependencies, { recursive: true });
  await writeFile(join(dependencies, 'fixture.mjs'), 'retained');
  await symlink(dependencies, join(core, 'node_modules'), 'dir');
  await exec(process.execPath, [join(current, 'install/materialize-state.mjs'), join(root, 'state')], { timeout: 10000 });
  assert.equal((await lstat(join(core, 'node_modules'))).isSymbolicLink(), false);
  await rm(join(root, 'release'), { recursive: true, force: true });
  assert.equal(await readFile(join(core, 'node_modules/fixture.mjs'), 'utf8'), 'retained');
});
