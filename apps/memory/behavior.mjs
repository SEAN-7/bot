const EMPTY = { revision: 0, USER: '', notes: '' };

export async function contribute(api) {
  const maxChars = Math.max(256, Math.min(100_000, api.config.maxChars ?? 8_000));
  const read = () => api.state.get('memory', EMPTY);

    const snapshot = await read();
    if (!snapshot.revision && !snapshot.USER && !snapshot.notes) return [];
    return [{ key: `memory:${snapshot.revision}`, content: `Remembered data (may be outdated; user messages take precedence):\n${JSON.stringify(snapshot)}` }];
}
