import { spawn } from 'node:child_process';

export default function execution(api) {
  const maxTimeoutMs = Math.max(100, Math.min(120_000, api.config.maxTimeoutMs ?? 60_000));
  const outputBytes = Math.max(1_024, Math.min(1_048_576, api.config.outputBytes ?? 65_536));
  api.on('beforeModel', async () => [{
    key: 'execution:environment:v1',
    content: `Environment reference for requested work:\nWorkspace: ${api.paths.workspace}\nEditable module drafts: ${api.paths.drafts}\nModule API reference: ${api.paths.workspace}/BOT-API.md\nSource-edit guide: ${api.paths.drafts}/execution/README.md\nEach module is a directory containing module.json (id, name, version, apiVersion: 1, entry) and an ES module exporting a factory (api) => { ... }. Register features with api.registerTool, api.registerModel, api.registerContext, or api.on; persist module state with api.state; queue future work with api.enqueue. Draft configuration is ${api.paths.drafts}/config.json; config.modules[id].enabled selects modules. Exactly one model and at most one context manager may be enabled. For a requested module change, inspect the existing source and API reference, edit or install a bundled module under drafts, check it, then call module_apply. Activation occurs after the current turn with trial and rollback. Core and runtime protection settings are operator-owned. This reference does not request changes or create an ongoing agenda.`,
  }]);
  api.registerTool({
    name: 'shell',
    description: `Run a bounded shell command from the workspace (${api.paths.workspace}). Editable modules are in ${api.paths.drafts}. Output is limited to ${outputBytes} bytes. Commands and their descendants are stopped on timeout, cancellation, or shell exit; do not launch background services.`,
    inputSchema: {
      type: 'object', additionalProperties: false, required: ['command'],
      properties: { command: { type: 'string', minLength: 1, maxLength: 100_000 }, timeoutMs: { type: 'integer', minimum: 1, maximum: maxTimeoutMs } },
    },
    async execute({ command, timeoutMs = Math.min(30_000, maxTimeoutMs) }, { signal }) {
      if (signal?.aborted) return { outcome: 'not_started', cancelled: true, stdout: '', stderr: '' };
      return new Promise((resolve, reject) => {
        // A separate process group is supervised here, never unref'ed or left running.
        // Do not run the command until the supervisor owns cleanup of its process group.
        // EOF also prevents starting work if the runtime dies before registration.
        const child = spawn('/bin/sh', ['-c', 'IFS= read -r _bot_start || exit 125; exec /bin/sh -c "$1"', 'bot-shell', command], {
          cwd: api.paths.workspace, detached: true, stdio: ['pipe', 'pipe', 'pipe'],
        });
        const chunks = { stdout: [], stderr: [] };
        let retained = 0;
        let truncated = false;
        let timedOut = false;
        let cancelled = false;
        let killTimer;
        let settled = false;
        let tracking = Promise.resolve();
        const killGroup = sig => {
          if (!child.pid) return;
          try { process.kill(-child.pid, sig); } catch (error) { if (error.code !== 'ESRCH') child.kill(sig); }
        };
        const stop = () => {
          killGroup('SIGTERM');
          killTimer ??= setTimeout(() => killGroup('SIGKILL'), 200);
        };
        const abort = () => { cancelled = true; stop(); };
        const timeout = setTimeout(() => { timedOut = true; stop(); }, timeoutMs);
        const cleanup = () => { clearTimeout(timeout); clearTimeout(killTimer); signal?.removeEventListener('abort', abort); };
        for (const stream of ['stdout', 'stderr']) child[stream].on('data', chunk => {
          const available = Math.max(0, outputBytes - retained);
          if (chunk.length > available) truncated = true;
          if (available) { const part = chunk.subarray(0, available); chunks[stream].push(part); retained += part.length; }
        });
        signal?.addEventListener('abort', abort, { once: true });
        if (signal?.aborted) abort();
        child.once('exit', () => killGroup('SIGKILL'));
        child.once('error', error => { if (!settled) { settled = true; cleanup(); reject(error); } });
        child.stdin.on('error', () => {}); // Cancellation can close the startup gate first.
        child.once('spawn', async () => {
          try {
            tracking = api.trackProcess(child.pid);
            await tracking;
            if (!cancelled && !timedOut && !settled) child.stdin.end('\n');
          } catch (error) {
            killGroup('SIGKILL');
            if (!settled) { settled = true; cleanup(); reject(error); }
          }
        });
        child.once('close', async (exitCode, exitSignal) => {
          const alreadySettled = settled;
          settled = true;
          cleanup();
          await tracking.catch(() => {});
          try { if (child.pid) await api.releaseProcess(child.pid); } catch (error) { if (!alreadySettled) reject(error); return; }
          if (alreadySettled) return;
          resolve({ exitCode, signal: exitSignal, timedOut, cancelled, truncated,
            outcome: timedOut || cancelled ? 'unknown' : 'completed',
            ...(timedOut || cancelled ? { warning: 'Interrupted command: side effects may have occurred. Do not automatically retry.' } : {}),
            stdout: Buffer.concat(chunks.stdout).toString('utf8'), stderr: Buffer.concat(chunks.stderr).toString('utf8') });
        });
      });
    },
  });
  api.registerTool({
    name: 'module_apply',
    description: 'Validate and stage all module drafts. The supervisor switches after this turn, trials the candidate, and rolls back failures. Core cannot be updated through this tool.',
    inputSchema: { type: 'object', properties: {}, additionalProperties: false },
    execute: () => api.proposeUpdate(),
  });
}
