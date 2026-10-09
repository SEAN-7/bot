import { runCLI } from '../_lib/cli.mjs';
import { commands } from './commands.mjs';
import { tick } from './behavior.mjs';
await runCLI({ commands, operations: { tick } });
