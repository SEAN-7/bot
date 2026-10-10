import { setTimeout } from 'node:timers/promises';

export async function tick(api, { signal } = {}) {
  const read = () => api.state.get('schedules', []);

    const now = Date.now();
    const schedules = await read();
    for (const entry of schedules) {
      if (signal?.aborted) return;
      const due = Date.parse(entry.nextAt);
      if (entry.status !== 'active' || due > now) continue;
      // Persist after enqueue: a crash between these writes retries the same event ID.
      await api.enqueue({ id: `schedule:${entry.id}:${due}`, text: entry.text, metadata: { scheduleId: entry.id, scheduledAt: entry.nextAt } });
      entry.lastDeliveredAt = entry.nextAt;
      if (entry.intervalMs) entry.nextAt = new Date(due + (Math.floor((now - due) / entry.intervalMs) + 1) * entry.intervalMs).toISOString();
      else entry.status = 'completed';
      await api.state.set('schedules', schedules);
    }
}

export async function watch(api,{signal,run=()=>tick(api,{signal})}={}) {
  while(!signal?.aborted) {
    try { await run(); }
    catch(error) { if(signal?.aborted)break;console.error(`Scheduler: ${error.message}`); }
    await setTimeout(1000,undefined,{signal}).catch(error=>{if(error.name!=='AbortError')throw error;});
  }
}
