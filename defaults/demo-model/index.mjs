// Deterministic fixture: never calls a provider or executes a tool.
export default function demoModel(api) {
  api.registerModel({
    async complete({ messages, signal }) {
      signal?.throwIfAborted();
      const user = messages.filter(message => message.role === 'user').at(-1);
      const text = String(user?.content ?? '').trim();
      return {
        text: text
          ? `Demo mode · no AI connected\n\nI received: ${text}\n\nThis verifies the durable conversation and messaging interface. Connect a model provider during setup to have the bot reason or use its tools.`
          : 'Demo mode · no AI connected. Send a message to check the conversation.',
        toolCalls: [],
        metadata: { demo: true },
      };
    },
  });
}
