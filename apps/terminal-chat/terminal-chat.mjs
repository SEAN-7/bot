import { spawn } from 'node:child_process';
import {
  Container, Editor, Markdown, ProcessTerminal, ScrollView, Text,
  TuiAltScreen, VStack, matchesKey, stripTerminalSequences, truncateToWidth,
} from '@earendil-works/pi-tui';
import { ChatSession } from './chat-session.mjs';

const cyan = text => `\x1b[36m${text}\x1b[39m`;
const dim = text => `\x1b[90m${text}\x1b[39m`;
const bold = text => `\x1b[1m${text}\x1b[22m`;
// Transcript content is data, never terminal control sequences.
export const plainText = text => stripTerminalSequences(String(text ?? '')).replace(/[\x00-\x08\x0b-\x1f\x7f-\x9f]/g, '');
const theme = {
  heading: bold, link: cyan, linkUrl: dim, code: cyan, codeBlock: text => text,
  codeBlockBorder: dim, quote: dim, quoteBorder: dim, hr: dim,
  listBullet: cyan, bold, italic: text => `\x1b[3m${text}\x1b[23m`,
  strikethrough: text => `\x1b[9m${text}\x1b[29m`, underline: text => text,
};

export function createChatView(session, terminal = new ProcessTerminal(), onExit = () => {}) {
  const tui = new TuiAltScreen(terminal, true, undefined, {
    scrollToEndIndicator: () => dim('↓ Latest'),
  });
  const transcript = new Container();
  const scroll = new ScrollView(transcript, { follow: 'end', primary: true, scrollbar: 'auto' });
  const editor = new Editor(tui, {
    borderColor: dim,
    selectList: { selectedPrefix: cyan, selectedText: cyan, description: dim, scrollInfo: dim, noMatch: dim },
  });
  let statusText = '';
  const status = { render: width => [truncateToWidth(' ' + statusText, width)] };
  tui.setLayoutRoot(new VStack([
    { component: scroll, basis: 0, grow: 1, minSize: 1 },
    { component: editor, basis: 'auto', maxSize: Math.max(3, Math.floor(terminal.rows * 0.35)), minSize: 3 },
    { component: status, basis: 1, shrink: 0 },
  ]));
  tui.setFocus(editor);
  let details = false;
  const views = new Map();
  const render = () => {
    transcript.clear();
    const visibleEntries = session.entries.filter(entry => details || entry.role !== 'tool');
    if (!visibleEntries.length) transcript.addChild(new Text(dim('Send a message to begin.'), 1, 1));
    for (const entry of visibleEntries) {
      let view = views.get(entry);
      if (!view) {
        view = { label: new Text('', 1, 0), body: new Markdown('', 1, 0, theme) };
        views.set(entry, view);
      }
      const label = entry.role === 'user' ? cyan('You') : entry.role === 'assistant' ? bold('Bot') : entry.role === 'tool' ? dim(`${plainText(entry.title)} · ${plainText(entry.status)}`) : dim('System');
      if (view.labelText !== label) { view.label.setText('\n' + label); view.labelText = label; }
      transcript.addChild(view.label);
      const text = entry.role === 'tool' ? [entry.input === undefined ? '' : JSON.stringify(entry.input, null, 2), entry.text].filter(Boolean).join('\n\n') : entry.text;
      const body = plainText(text);
      if (view.bodyText !== body) { view.body.setText(body); view.bodyText = body; }
      transcript.addChild(view.body);
    }
    editor.disableSubmit = !session.ready || session.busy;
    const working = session.busy || session.entries.some(entry => entry.role === 'tool' && entry.status === 'in_progress');
    statusText = dim(`${plainText(working && session.status === 'Ready' ? 'Working…' : session.status)} · Enter send · ^J newline · ^T tools · ^C ${session.busy ? 'cancel' : 'exit'} · ^D exit`);
    tui.requestRender();
  };
  session.on('change', render);
  editor.onSubmit = text => {
    if (!text.trim()) return;
    editor.addToHistory(text);
    scroll.scrollToEnd();
    void session.submit(text).catch(error => { session.status = error.message; render(); });
  };
  tui.addInputListener(data => {
    if (matchesKey(data, 'ctrl+d')) { onExit(); return { consume: true }; }
    if (matchesKey(data, 'ctrl+c')) {
      if (session.busy) session.cancel(); else onExit();
      return { consume: true };
    }
    if (matchesKey(data, 'ctrl+t')) { details = !details; render(); return { consume: true }; }
    if (matchesKey(data, 'alt+enter')) { editor.handleInput('\n'); return { consume: true }; }
    return undefined;
  });
  render();
  return { tui, editor, scroll, dispose: () => session.removeListener('change', render) };
}

export async function runTerminalChat({ command, args, cwd, env }) {
  const child = spawn(command, args, { cwd, env, stdio: ['pipe', 'pipe', 'pipe'] });
  const session = new ChatSession(child);
  let exit;
  const done = new Promise(resolve => { exit = resolve; });
  const view = createChatView(session, undefined, exit);
  const signalExit = () => exit();
  process.once('SIGTERM', signalExit);
  process.once('SIGINT', signalExit);
  try {
    view.tui.start();
    void session.start(cwd).catch(error => { session.status = error.message; session.changed(); });
    await done;
    return 0;
  } finally {
    view.dispose();
    session.close();
    // Restore the previous screen without dumping the whole transcript into the shell.
    view.tui.stop({ preserveScreen: true });
    await view.tui.terminal.drainInput(200, 30);
    process.removeListener('SIGTERM', signalExit);
    process.removeListener('SIGINT', signalExit);
  }
}
