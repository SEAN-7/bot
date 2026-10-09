import { randomUUID } from 'node:crypto';

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
