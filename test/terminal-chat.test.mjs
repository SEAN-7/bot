import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { visibleWidth } from '@earendil-works/pi-tui';
import { createChatView, plainText } from '../apps/terminal-chat/terminal-chat.mjs';

class Terminal {
  columns = 80;
  rows = 24;
  kittyProtocolActive = false;
  output = '';
  start(input, resize) { this.input = input; this.resize = resize; }
  stop() {}
  write(data) { this.output += data; }
  hideCursor() {}
  showCursor() {}
  clearLine() {}
  clearFromCursor() {}
  clearScreen() {}
  moveBy() {}
  setTitle() {}
  setProgress() {}
  setProgramStatus() {}
}

function fixture(t, entries = []) {
  const session = Object.assign(new EventEmitter(), { ready: true, busy: false, status: 'Ready', entries });
  const submitted = [];
  session.submit = async text => { submitted.push(text); };
  let cancelled = 0, exited = 0;
  session.cancel = () => { cancelled++; };
  const terminal = new Terminal();
  const view = createChatView(session, terminal, () => { exited++; });
  view.tui.start();
  const frame = () => { view.tui.renderNow(true); return view.tui.getScreenLines().map(plainText); };
  t.after(() => { view.dispose(); view.tui.stop({ preserveScreen: true }); });
  return { session, terminal, view, frame, submitted, cancelled: () => cancelled, exited: () => exited };
}

test('full-screen transcript scrolls independently of the fixed editor and status after resize', t => {
  const f = fixture(t, Array.from({ length: 40 }, (_, index) => ({ role: 'user', text: `Message ${index}` })));
  f.view.editor.setText('Draft stays here');
  let frame = f.frame();
  assert.equal(frame.length, 24);
  assert.ok(frame.join('\n').includes('Message 39'));
  assert.ok(frame.slice(-5).join('\n').includes('Draft stays here'));
  assert.match(frame.at(-1), /Ready/);
  f.terminal.input('\x1b[5~'); // Page Up
  frame = f.frame();
  assert.ok(!frame.join('\n').includes('Message 39'));
  assert.ok(frame.slice(-5).join('\n').includes('Draft stays here'));
  f.terminal.columns = 32;
  f.terminal.rows = 10;
  f.terminal.resize();
  frame = f.frame();
  assert.equal(frame.length, 10);
  assert.ok(frame.every(line => visibleWidth(line) <= 32));
  assert.ok(frame.slice(-5).join('\n').includes('Draft stays here'));
  assert.match(frame.at(-1), /Ready/);
  assert.ok(f.terminal.output.includes('\x1b[?1049h'));
});

test('chat hides all tool activity until diagnostics are explicitly enabled', t => {
  const f = fixture(t, [
    { role: 'user', text: 'Check my workspace' },
    { role: 'tool', title: 'queued-tool', status: 'pending', text: '' },
    { role: 'tool', title: 'running-tool', status: 'in_progress', text: '' },
    { role: 'tool', title: 'shell', status: 'completed', input: { command: 'pwd' }, text: 'successful output' },
    { role: 'tool', title: 'fetch', status: 'failed', text: 'failed output' },
    { role: 'assistant', text: 'Workspace checked' },
  ]);
  let text = f.frame().join('\n');
  assert.match(text, /Check my workspace/);
  assert.match(text, /Workspace checked/);
  assert.match(text, /Working…/);
  assert.doesNotMatch(text, /queued-tool|running-tool|shell|fetch|successful output|failed output|pwd/);
  f.terminal.input('\x14'); // Ctrl+T
  text = f.frame().join('\n');
  assert.match(text, /successful output/);
  assert.match(text, /pwd/);
  assert.match(text, /failed output/);
  f.terminal.input('\x14'); // Hide diagnostics again, including new live events.
  f.session.entries.push({ role: 'tool', title: 'live-tool', status: 'failed', text: 'live failure' });
  f.session.emit('change');
  text = f.frame().join('\n');
  assert.doesNotMatch(text, /queued-tool|running-tool|shell|fetch|successful output|failed output|pwd|live-tool|live failure/);
  assert.match(text, /Workspace checked/);
});

test('multiline input submits once, busy input stays editable, and cancellation differs from exit', t => {
  const f = fixture(t);
  f.terminal.input('first');
  f.terminal.input('\n'); // Ctrl+J
  f.terminal.input('second');
  f.terminal.input('\r');
  assert.deepEqual(f.submitted, ['first\nsecond']);
  f.session.busy = true;
  f.session.status = 'Working…';
  f.session.emit('change');
  f.terminal.input('next draft');
  f.terminal.input('\r');
  assert.equal(f.submitted.length, 1);
  assert.equal(f.view.editor.getText(), 'next draft');
  f.terminal.input('\x03');
  assert.equal(f.cancelled(), 1);
  assert.equal(f.exited(), 0);
  f.terminal.input('\x04');
  assert.equal(f.exited(), 1);
  f.session.busy = false;
  f.terminal.input('\x03');
  assert.equal(f.exited(), 2);
});

test('untrusted text cannot inject terminal control sequences', t => {
  const f = fixture(t, [{ role: 'assistant', text: 'hello\x1b[2Jworld\x1b]52;c;stolen\x07' }]);
  assert.equal(plainText('hello\x1b[2Jworld\x1b]52;c;stolen\x07'), 'helloworld');
  assert.match(f.frame().join('\n'), /helloworld/);
  assert.ok(!f.terminal.output.includes('stolen'));
});

test('recovery notices use a System label instead of impersonating bot replies', t => {
  const f = fixture(t, [{ role: 'notice', text: 'Recovery report: Supervisor interrupted' }]);
  const text = f.frame().join('\n');
  assert.match(text, /System/);
  assert.match(text, /Recovery report: Supervisor interrupted/);
  assert.doesNotMatch(text, /\bBot\b/);
});
