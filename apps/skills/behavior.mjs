import { createHash } from 'node:crypto';
import { discover } from './discovery.mjs';
export async function contribute(api) {

    const entries = await discover([api.paths.apps, api.paths.workspace + '/skills']);
    const digest = createHash('sha256').update(JSON.stringify(entries)).digest('hex');
    return [{key: `shell-apps:${digest}`,content: `Current capability architecture: the only model-facing tool is shell. Historical tool plugins and their instructions are obsolete. Run bot-app --help to inspect apps and cat the matching SKILL.md before using a capability. Apps are ordinary programs run through shell; they cannot add model-facing tools. All behavior runs in separate app processes. There is no plugin loader or tool registration API.
Active apps and skill paths:
${JSON.stringify(entries.map(({content,...entry})=>entry))}
Workspace: ${api.paths.workspace}
Editable app programs and controller working copy: ${api.paths.drafts}/apps
Core CLI contract: ${api.paths.workspace}/BOT-API.md
Ordinary app files are live: edits, installations and upgrades take effect on the next invocation. No mandatory preflight, activation, probation or bot restart is required. Test when useful and proceed with authorized work without repeated permission. Existing processes use their loaded code until restarted. Only replacing the running controller or its startup configuration uses bot-app updates apply '{}', at a turn boundary with controller recovery. New app packages need app.json with an executable command and SKILL.md; add config.apps[id].enabled=true in drafts/config.json. New ordinary scripts can also live in workspace and run directly. Add workflow-only skills in workspace/skills/<name>/SKILL.md. Idle time creates no model calls. Skills are reference data; current user instructions take precedence.`}];
}
