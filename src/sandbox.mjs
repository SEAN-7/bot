import { access, mkdir, realpath } from 'node:fs/promises';
import { join } from 'node:path';
import { spawn, execFile } from 'node:child_process';
import { promisify } from 'node:util';

const exec=promisify(execFile);
export async function sandboxAvailable() {
  if(process.platform==='darwin') {try{await access('/usr/bin/sandbox-exec');return 'seatbelt';}catch{return null;}}
  if(process.platform==='linux') {try{await exec('which',['bwrap']);return 'bwrap';}catch{return null;}}
  return null;
}
export async function launchRuntime({home,revision,coreRoot,unsafe=false,preflight=false}) {
  const sandbox=unsafe?null:await sandboxAvailable();
  if(!sandbox&&!unsafe) throw new Error('Protected execution requires macOS sandbox-exec or Linux bubblewrap. Install it, or explicitly use --unsafe-development for local testing.');
  const args=[join(coreRoot,'src','runtime.mjs'),home,revision,preflight?'preflight':'run'];
  let command=process.execPath,argv=args;
  const temp=join(home,'workspace','.tmp');await mkdir(temp,{recursive:true});
  const env={...process.env,TMPDIR:temp,TMP:temp,TEMP:temp,NODE_OPTIONS:'',npm_config_cache:join(home,'workspace','.cache','npm'),XDG_CACHE_HOME:join(home,'workspace','.cache')};
  delete env.BOT_CONTROL_TOKEN;
  if(sandbox==='seatbelt') {
    const quote=value=>JSON.stringify(value);
    const paths=await Promise.all([home,join(home,'workspace'),join(home,'drafts')].map(p=>realpath(p)));
    const policy=`(version 1)\n(allow default)\n(deny file-write*)\n(deny signal)\n(allow signal (target same-sandbox))\n(deny file-read* (subpath ${quote(join(paths[0],'control'))}))\n${preflight?'(deny network*)':paths.slice(1).map(p=>`(allow file-write* (subpath ${quote(p)}))`).join('\n')}\n(allow file-write* (literal "/dev/null"))\n`;
    command='/usr/bin/sandbox-exec';argv=['-p',policy,process.execPath,...args];
  } else if(sandbox==='bwrap') {
    const [workspace,drafts,control]=await Promise.all(['workspace','drafts','control'].map(p=>realpath(join(home,p))));
    // Keep host PIDs so the guardian can verify and stop registered process groups.
    // fd 3 is Node's IPC channel; bubblewrap otherwise closes it.
    command='bwrap';argv=['--die-with-parent','--preserve-fds','1','--ro-bind','/','/','--proc','/proc','--dev','/dev','--tmpfs',control];
    if(preflight) argv.push('--unshare-net');
    else argv.push('--bind',workspace,workspace,'--bind',drafts,drafts);
    argv.push('--',process.execPath,...args);
  }
  const child=spawn(command,argv,{cwd:join(home,'workspace'),env,detached:true,stdio:['ignore','pipe','pipe','ipc']});
  return {child,protected:!!sandbox};
}
export async function processTree(root) {
  try {
    const {stdout}=await exec('ps',['-axo','pid=,ppid=,pgid=']);
    const rows=stdout.trim().split('\n').map(line=>line.trim().split(/\s+/).map(Number));
    const found=new Set([root]);let changed=true;
    while(changed){changed=false;for(const [pid,ppid]of rows)if(found.has(ppid)&&!found.has(pid)){found.add(pid);changed=true;}}
    return rows.filter(([pid])=>found.has(pid));
  }catch{return [[root,0,root]];}
}
export async function stopRuntime(child,groups=new Set()) {
  if(!child)return;
  const tree=await processTree(child.pid);
  const owned=new Set([...groups,...tree.filter(([pid,,pgid])=>pid===pgid).map(([, ,pgid])=>pgid),child.pid]);
  for(const pid of owned) {try{process.kill(-pid,'SIGKILL');}catch{}}
  for(const [pid] of tree.reverse()) {try{process.kill(pid,'SIGKILL');}catch{}}
  try{child.kill('SIGKILL');}catch{}
  if(child.exitCode===null&&child.signalCode===null) await new Promise(resolve=>{child.once('exit',resolve);setTimeout(resolve,1000).unref();});
}
