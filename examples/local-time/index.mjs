export default function localTime(api) {
  api.registerTool({
    name: 'local_time',
    description: 'Read the current UTC time.',
    inputSchema: { type: 'object', properties: {}, additionalProperties: false },
    async execute() {
      return { utc: new Date().toISOString() };
    },
  });
}
