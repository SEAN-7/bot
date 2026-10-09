import { readFile, mkdir, writeFile, rename } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { randomBytes } from 'node:crypto';
import { TelegramStore } from './database.mjs';
import { credentials, dataFolder } from './behavior.mjs';
import { telegramAPI } from './api.mjs';

async function tokenInput() {
  if(!process.stdin.isTTY) {
    let input='';for await(const chunk of process.stdin){input+=chunk;if(input.length>1000)throw new Error('Token input too large');}return input.trim();
  }
  process.stdout.write('Telegram bot token (hidden): ');
  const raw=process.stdin.isRaw;process.stdin.setRawMode(true);process.stdin.resume();
  return new Promise((resolveToken,reject)=>{
    let input='';
    const finish=error=>{process.stdin.removeListener('data',onData);process.stdin.setRawMode(raw);process.stdin.pause();process.stdout.write('\n');error?reject(error):resolveToken(input.trim());};
    const onData=chunk=>{for(const char of chunk.toString()){if(char==='\u0003')return finish(new Error('Setup cancelled'));if(char==='\r'||char==='\n')return finish();if(char==='\u007f'||char==='\b')input=input.slice(0,-1);else if(char>=' ')input+=char;}};
    process.stdin.on('data',onData);
  });
}
export async function operator([mode,flag,home,...extra]) {
  if(flag!=='--home'||!home)throw new Error('Usage: telegram configure|inspect|authorize --home BOT_HOME [--username USERNAME]. Configure reads the token from stdin.');
  let username;
  if(mode==='authorize') {
    if(extra[0]!=='--username'||extra.length!==2||!/^@?[A-Za-z0-9_]{1,32}$/.test(extra[1]))throw new Error('Authorize requires an exact Telegram --username');
    username=extra[1].replace(/^@/,'').toLowerCase();
  }else if(extra.length)throw new Error('Unexpected arguments');
  const folder=dataFolder(join(resolve(home),'workspace'));
  const existing=await credentials(folder);
  const token=mode==='configure'?(existing?.token||await tokenInput()):existing?.token;
  if(!token){console.log(JSON.stringify({configured:false}));return;}
  const api=telegramAPI(token), identity=await api('getMe'), webhook=await api('getWebhookInfo');
  if(webhook.url)throw new Error('Telegram already has a webhook; configuration left unchanged. Remove the previous integration before connecting this app.');
  const store=new TelegramStore(folder);
  try {
    const prior=store.get('identity');if(prior&&prior.id!==identity.id)throw new Error('This inbox belongs to a different Telegram bot');
    if(mode==='authorize') {
      if(store.get('binding'))throw new Error('Telegram is already paired; authorization was left unchanged');
      store.set('authorizedUsername',username);
    }
    if(mode==='configure') {
      await mkdir(folder,{recursive:true,mode:0o700});
      const temp=join(folder,`credentials-${randomBytes(8).toString('hex')}.tmp`);
      await writeFile(temp,JSON.stringify({token})+'\n',{mode:0o600});await rename(temp,join(folder,'credentials.json'));
      store.set('identity',identity);
      if(!store.get('binding'))store.set('pairing',{code:randomBytes(24).toString('base64url'),expires:Date.now()+24*60*60*1000});
    }
    const pairing=store.get('pairing');
    console.log(JSON.stringify({...store.status(),username:identity.username,authorizedUsername:store.get('authorizedUsername'),pairingURL:pairing&&pairing.expires>Date.now()?`https://t.me/${identity.username}?start=${pairing.code}`:null}));
  } finally {store.close();}
}
