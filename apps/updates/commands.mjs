export function commands(api) {
  return { apply: {
    description: 'Replace the running conversation controller and its startup configuration at a turn boundary, with recovery. Ordinary app edits are already live and do not need this command. Core remains operator-owned.',
    inputSchema: { type: 'object', properties: {}, additionalProperties: false },
    execute: () => api.proposeUpdate(),
  } };
}
