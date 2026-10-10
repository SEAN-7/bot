import assert from 'node:assert/strict';

// Reassemble captured summary evidence, independently checking contiguous offsets.
export function summaryEvidence(requests) {
  const records = new Map();
  for (const request of [requests].flat()) for (const message of request.messages) {
    if (!String(message.id).startsWith('source:')) continue;
    const match = /^Journal (.+) \([^)]+\), characters (\d+)-(\d+) of (\d+):\n/.exec(message.content);
    assert.ok(match, 'Summary evidence must include journal provenance and offsets');
    const [, id, start, end, total] = match;
    const record = records.get(id) ?? { text: '', total: Number(total) };
    assert.equal(record.text.length, Number(start), 'No omitted or duplicated evidence');
    const fragment = message.content.slice(match[0].length);
    assert.equal(fragment.length, Number(end) - Number(start));
    record.text += fragment;
    records.set(id, record);
  }
  return [...records.values()].map(({ text, total }) => {
    assert.equal(text.length, total, 'The whole original record was considered');
    return JSON.parse(text);
  });
}
