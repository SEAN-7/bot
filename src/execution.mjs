import { spawn } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { StringDecoder } from 'node:string_decoder';

export default function execution(api) {
  const maxTimeoutMs = Math.max(1, Math.min(2_147_483_647, api.config.maxTimeoutMs ?? 2_147_483_647));
  const maxSessions = Math.max(1, Math.min(128, api.config.maxSessions ?? 32));
  const sessions = new Map();
  let stopping = false;
  const waitSchema = { type: 'integer', minimum: 0, maximum: 30_000 };
  const outputBytes = Math.max(1_024, Math.min(1_048_576, api.config.outputBytes ?? 65_536));
  function start(command, timeoutMs, signal, callId) {
    // Keep the group registered across tool calls. The supervisor also kills it
    // if cancellation, a crash, or an update tears down the worker.
    const child = spawn('/bin/sh', ['-c', 'IFS= read -r _bot_start || exit 125; exec /bin/sh -c "$1"', 'bot-shell', command], {
      cwd: api.paths.workspace, env: { ...process.env, ...api.env, BOT_CALL_ID: callId || '' }, detached: true, stdio: ['pipe', 'pipe', 'pipe'],
    });
    const session = {
      id: randomUUID(), child, chunks: { stdout: [], stderr: [] }, retained: 0,
      truncated: false, timedOut: false, cancelled: false, closed: false, readers: 0, yielded: false,
      waiters: new Set(), decoders: { stdout: new StringDecoder('utf8'), stderr: new StringDecoder('utf8') },
    };
    sessions.set(session.id, session);
    let killTimer, timeout, tracking = Promise.resolve();
    let finish, ready;
    session.ready = new Promise(resolve => { ready = resolve; });
    session.done = new Promise(resolve => { finish = resolve; });
    const killGroup = sig => {
      if (!child.pid) return;
      try { process.kill(-child.pid, sig); } catch (error) { if (error.code !== 'ESRCH') child.kill(sig); }
    };
    session.stop = () => {
      if (session.closed) return;
      killGroup('SIGTERM');
      killTimer ??= setTimeout(() => killGroup('SIGKILL'), 200);
    };
    const abort = () => { session.cancelled = true; session.stop(); };
    session.abort = abort;
    if (timeoutMs !== undefined) timeout = setTimeout(() => { session.timedOut = true; session.stop(); }, timeoutMs);
    for (const stream of ['stdout', 'stderr']) child[stream].on('data', chunk => {
      const available = Math.max(0, outputBytes - session.retained);
      if (chunk.length > available) session.truncated = true;
      if (available) { const part = chunk.subarray(0, available); session.chunks[stream].push(part); session.retained += part.length; }
    });
    signal?.addEventListener('abort', abort, { once: true });
    if (signal?.aborted) abort();
    child.once('exit', () => killGroup('SIGKILL'));
    child.once('error', error => { session.error = error; });
    child.stdin.on('error', () => {}); // A process may exit before input arrives.
    child.once('spawn', async () => {
      try {
        tracking = api.trackProcess(child.pid);
        await tracking;
        if (!session.cancelled && !session.timedOut) child.stdin.write('\n');
      } catch (error) {
        session.error = error;
        killGroup('SIGKILL');
      } finally { ready(); }
    });
    child.once('close', async (exitCode, exitSignal) => {
      clearTimeout(timeout); clearTimeout(killTimer);
      signal?.removeEventListener('abort', abort);
      await tracking.catch(() => {});
      try { if (child.pid) await api.releaseProcess(child.pid); } catch (error) { session.error = error; }
      Object.assign(session, { closed: true, exitCode, exitSignal });
      ready();
      for (const wake of session.waiters) wake();
      // A waiting caller receives the final result itself. Otherwise wake the
      // conversation once, retaining the result for an optional later poll.
      try {
        if (session.yielded && !session.readers && !stopping && api.completed) {
          session.finalResult = collect(session);
          await api.completed(session.id, session.finalResult);
        }
      } catch (error) { session.error = error; api.onError?.(error); }
      finally { finish(); }
    });
    return session;
  }

  function collect(session) {
    if (session.finalResult) return session.finalResult;
    const result = {
      ...(session.closed ? { exitCode: session.exitCode, signal: session.exitSignal } : { sessionId: session.id }),
      timedOut: session.timedOut, cancelled: session.cancelled, truncated: session.truncated,
      outcome: session.closed ? (session.timedOut || session.cancelled ? 'unknown' : 'completed') : 'running',
      ...(session.timedOut || session.cancelled ? { warning: 'Interrupted command: side effects may have occurred. Do not automatically retry.' } : {}),
      ...Object.fromEntries(['stdout', 'stderr'].map(stream => {
        const decoder = session.decoders[stream];
        let text = decoder.write(Buffer.concat(session.chunks[stream]));
        if (session.closed || session.truncated) text += decoder.end();
        return [stream, text];
      })),
    };
    session.chunks = { stdout: [], stderr: [] }; session.retained = 0; session.truncated = false;
    return result;
  }

  async function read(session, yieldTimeMs, signal) {
    session.readers++;
    const abort = () => session.abort();
    signal?.addEventListener('abort', abort, { once: true });
    if (signal?.aborted) abort();
    try {
      if (!session.closed && yieldTimeMs > 0 && !api.shouldYield?.()) await new Promise(resolve => {
        const wake = () => { clearTimeout(timer); session.waiters.delete(wake); resolve(); };
        const timer = setTimeout(wake, yieldTimeMs);
        session.waiters.add(wake);
      });
      const result = collect(session);
      if (session.closed) sessions.delete(session.id);
      else session.yielded = true;
      if (session.error) throw session.error;
      return result;
    } finally { session.readers--; signal?.removeEventListener('abort', abort); }
  }

  api.on('stop', async () => {
    stopping = true;
    for (const session of sessions.values()) session.abort();
    await Promise.all([...sessions.values()].map(session => session.done));
    sessions.clear();
  });
  const launch = {
    name: 'shell',
    description: `Run a shell command from the workspace (${api.paths.workspace}). Wait up to yieldTimeMs (default 1000, maximum 30000), then return a sessionId if still running. Incoming notifications end the wait without stopping the command. Completion of a yielded session wakes the conversation with its final status and unread output; you can continue independent work or remain idle until notified instead of polling repeatedly. Poll or send input with shell({sessionId, ...}). There is no default execution timeout; timeoutMs explicitly stops the command. Output per call is limited to ${outputBytes} bytes. Sessions are supervised until exit, cancellation, or runtime restart. Do not detach background services.`,
    inputSchema: {
      type: 'object', additionalProperties: false, required: ['command'],
      properties: {
        command: { type: 'string', minLength: 1, maxLength: 100_000 },
        yieldTimeMs: waitSchema,
        timeoutMs: { type: 'integer', minimum: 1, maximum: maxTimeoutMs },
      },
    },
    async execute({ command, yieldTimeMs = 1000, timeoutMs }, { signal, callId }) {
      if (signal?.aborted) return { outcome: 'not_started', cancelled: true, stdout: '', stderr: '' };
      // Evict only finished, unread sessions. Never abandon a running group.
      if (sessions.size >= maxSessions) for (const [id, session] of sessions) {
        if (session.closed) sessions.delete(id);
        if (sessions.size < maxSessions) break;
      }
      if (sessions.size >= maxSessions) throw new Error('Shell session limit reached; finish or terminate an existing session with shell({sessionId, ...}).');
      return read(start(command, timeoutMs, signal, callId), yieldTimeMs, signal);
    },
  };
  const resume = {
    name: 'write_stdin',
    description: 'Poll a running shell session or send text to its stdin. Returns only new output, plus sessionId while running or exitCode when finished. yieldTimeMs defaults to 1000, maximum 30000; 0 checks immediately. Use endInput to close stdin (EOF), or terminate to stop the command and descendants. Sessions do not survive runtime restart; never automatically replay a missing session.',
    inputSchema: {
      type: 'object', additionalProperties: false, required: ['sessionId'],
      properties: {
        sessionId: { type: 'string', minLength: 1, maxLength: 64 },
        chars: { type: 'string', maxLength: 100_000 }, yieldTimeMs: waitSchema,
        endInput: { type: 'boolean' }, terminate: { type: 'boolean' },
      },
    },
    async execute({ sessionId, chars = '', yieldTimeMs = 1000, endInput = false, terminate = false }, { signal }) {
      const session = sessions.get(sessionId);
      if (!session) throw new Error('Shell session not found (finished, evicted, or runtime restarted). Its effects may have occurred; do not automatically replay the command.');
      if (terminate || signal?.aborted) session.abort();
      else if (chars || endInput) {
        // Do not let caller input open the startup gate before tracking succeeds.
        await session.ready;
        if (session.error) throw session.error;
        if (session.closed || session.child.stdin.destroyed || session.child.stdin.writableEnded) throw new Error('Shell session stdin is closed; poll without chars for final output.');
        if (chars) session.child.stdin.write(chars);
        if (endInput) session.child.stdin.end();
      }
      return read(session, yieldTimeMs, signal);
    },
  };
  return {
    name: 'shell', description: launch.description + ' Poll/send input/EOF/cancel through this same shell tool using sessionId and optional chars, endInput, terminate, yieldTimeMs. Session IDs do not survive restart. Apps and skills: run bot-app --help, then read the matching SKILL.md.',
    inputSchema: { type: 'object', properties: { ...launch.inputSchema.properties, ...resume.inputSchema.properties }, additionalProperties: false, oneOf: [launch.inputSchema, resume.inputSchema] },
    execute: (args, context) => (Object.hasOwn(args, 'sessionId') ? resume : launch).execute(args, context),
    interruptWaits: () => { for (const session of sessions.values()) for (const wake of session.waiters) wake(); },
  };
}
