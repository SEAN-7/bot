import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';

const linux = process.platform === 'linux';
test('self-extracting installer verifies payload and preserves argument boundaries', { skip: !linux }, () => {
  const dir = mkdtempSync(join(tmpdir(), 'bot-packaging-test-'));
  try {
    const payload = join(dir, 'payload');
    mkdirSync(join(payload, 'runtime/node/bin'), { recursive: true });
    mkdirSync(join(payload, 'install'));
    writeFileSync(join(payload, 'install/manage.mjs'), '// fixture\n');
    writeFileSync(join(payload, 'runtime/node/bin/node'), '#!/bin/sh\nprintf "<%s>\\n" "$@"\n', { mode: 0o755 });
    const archive = join(dir, 'payload.tar.gz');
    const tar = spawnSync('tar', ['-czf', archive, '-C', payload, '.']);
    assert.equal(tar.status, 0, tar.stderr.toString());
    const bytes = readFileSync(archive);
    const checksum = createHash('sha256').update(bytes).digest('hex');
    const stub = readFileSync(new URL('../install/bootstrap.sh', import.meta.url), 'utf8').replace('@PAYLOAD_SHA256@', checksum);
    const executable = join(dir, 'bot');
    writeFileSync(executable, Buffer.concat([Buffer.from(stub), bytes]), { mode: 0o755 });
    // Simulate root authorization without requiring tests to change host privileges.
    const commands = join(dir, 'commands');
    mkdirSync(commands);
    writeFileSync(join(commands, 'id'), '#!/bin/sh\necho 0\n', { mode: 0o755 });
    const env = { ...process.env, PATH: `${commands}:${process.env.PATH}`, BOT_INSTALL_TMPDIR: dir };
    const good = spawnSync(executable, ['install', '--name', 'contains spaces', 'literal;$value'], { env, encoding: 'utf8' });
    assert.equal(good.status, 0, good.stderr);
    assert.match(good.stdout, /<install>\n<--bundle>\n<[^>]+\/bundle>\n<--name>\n<contains spaces>\n<literal;\$value>/);
    const altered = Buffer.from(bytes);
    const next = spawnSync(executable, ['install-next', '--credential-file', '/some path/environment'], { env, encoding: 'utf8' });
    assert.equal(next.status, 0, next.stderr);
    assert.match(next.stdout, /\/app\/harness\/install.mjs>\n<install-next>/);
    assert.match(next.stdout, /<--credential-file>\n<\/some path\/environment>/);
    altered[altered.length - 1] ^= 1;
    writeFileSync(executable, Buffer.concat([Buffer.from(stub), altered]));
    const bad = spawnSync(executable, ['update'], { env, encoding: 'utf8' });
    assert.notEqual(bad.status, 0);
    assert.doesNotMatch(bad.stdout, /<update>/);
    const invalid = spawnSync(executable, ['destroy'], { env, encoding: 'utf8' });
    assert.equal(invalid.status, 2);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
