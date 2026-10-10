export const sectionLimits = config => Object.fromEntries(['USER', 'notes'].map(section => [section,
  Math.max(256, Math.min(100000, config.maxChars ?? (section === 'USER' ? config.userMaxChars ?? 1375 : config.notesMaxChars ?? 2200))),
]));
