import { randomUUID } from 'node:crypto';

export default function scheduler(api) {
  const read = () => api.state.get('schedules', []);
  api.registerTool({
    name: 'schedule_create',
    description: 'Queue a future wake-up in the same conversation. Supply an ISO timestamp with a timezone; optionally repeat at a fixed interval. Missed recurring slots coalesce into one wake-up after downtime.',
    inputSchema: {
      type: 'object', additionalProperties: false, required: ['text', 'at'],
      properties: {
        text: { type: 'string', minLength: 1, maxLength: 20_000 },
        at: { type: 'string', minLength: 20, maxLength: 40 },
        intervalMs: { type: 'integer', minimum: 1_000, maximum: 31_536_000_000 },
      },
    },
    async execute({ text, at, intervalMs = null }) {
      if (!/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)$/.test(at) || !Number.isFinite(Date.parse(at))) {
        throw new Error('at must be an ISO timestamp with an explicit timezone');
      }
      const schedules = await read();
      if (schedules.filter(item => item.status === 'active').length >= (api.config.maxSchedules ?? 100)) {
        throw new Error('Active schedule limit reached');
      }
      const schedule = { id: randomUUID(), text, nextAt: new Date(at).toISOString(), intervalMs, status: 'active', lastDeliveredAt: null };
      await api.state.set('schedules', [...schedules, schedule]);
      return schedule;
    },
  });
  api.registerTool({
    name: 'schedule_list', description: 'List durable schedules, including completed and cancelled entries.',
    inputSchema: { type: 'object', properties: {}, additionalProperties: false }, execute: read,
  });
  api.registerTool({
    name: 'schedule_cancel', description: 'Cancel future delivery of a schedule. A wake-up already queued may still run.',
    inputSchema: { type: 'object', additionalProperties: false, required: ['id'], properties: { id: { type: 'string', minLength: 1 } } },
    async execute({ id }) {
      const schedules = await read();
      const entry = schedules.find(item => item.id === id);
      if (!entry) throw new Error(`Unknown schedule: ${id}`);
      entry.status = 'cancelled';
      await api.state.set('schedules', schedules);
      return entry;
    },
  });
  api.on('tick', async ({ signal }) => {
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
  });
}
