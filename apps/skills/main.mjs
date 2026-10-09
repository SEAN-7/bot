import { runCLI } from '../_lib/cli.mjs';
import { commands } from './commands.mjs';
import { contribute } from './behavior.mjs';
await runCLI({ commands, operations: { contribute } });
