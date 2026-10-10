import { spawn } from 'node:child_process';
export const invoke = (app, command, input = {}) => invokeCommand(['bot-app', app, command, '--stdin'], input);
export function invokeCommand(argv, input = {}, env = process.env) {
  return new Promise((resolve, reject) => {
    const child = spawn(argv[0], argv.slice(1), {env,stdio:['pipe','pipe','pipe']});
    let stdout = '', stderr = '', failed = false;
    const fail = error => { if (!failed) {failed=true;child.kill('SIGKILL');reject(error);} };
    child.stdin.on('error', error => {if(error.code!=='EPIPE')fail(error);});
    child.on('error', fail);
    for (const [stream, name] of [[child.stdout,'stdout'],[child.stderr,'stderr']]) stream.setEncoding('utf8').on('data', chunk => {
      if(name==='stdout')stdout+=chunk;else stderr+=chunk;
      if(stdout.length>20_000_000||stderr.length>100_000)fail(new Error(`App ${argv[0]} output exceeded its limit`));
    });
    child.once('exit', (code, signal) => {
      if(failed)return;
      try {
        if(code!==0) {
          let detail;try{detail=JSON.parse(stderr.trim().split('\n').at(-1));}catch{}
          throw Object.assign(new Error(detail?.error||`App ${argv[0]} exited (${code??signal}): ${stderr.slice(-8000)}`),{retryable:!!detail?.retryable,blocked:!!detail?.blocked});
        }
        resolve(JSON.parse(stdout));
      } catch(error) {reject(error);}
    });
    child.stdin.end(JSON.stringify(input));
  });
}
