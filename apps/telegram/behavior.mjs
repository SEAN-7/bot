import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { TelegramStore } from './database.mjs';
import { telegramAPI } from './api.mjs';

export const dataFolder = workspace => join(workspace,'telegram');
export async function credentials(folder) {
  try{return JSON.parse(await readFile(join(folder,'credentials.json'),'utf8'));}
  catch(error){if(error.code==='ENOENT')return null;throw new Error('Telegram credential file is invalid');}
}
export function project(messages) {
  return messages.map(message=>message.role==='user'&&message.metadata?.source==='telegram'
    ? {...message,role:'context',metadata:{...message.metadata,input:true}}:message);
}
export async function receive(store, api, enqueue, {signal, timeout=30}={}) {
  const updates=await api('getUpdates',{offset:store.get('offset',0),timeout,limit:100,allowed_updates:['message','edited_message']},{signal,timeoutMs:(timeout+10)*1000});
  for(const update of updates) {
    if(!Number.isSafeInteger(update.update_id))throw new Error('Invalid Telegram update');
    const message=update.message||update.edited_message;
    let binding=store.get('binding');
    if(message?.chat?.type==='private'&&message.from&&!message.from.is_bot) {
      const pairing=store.get('pairing');
      const startCode=message.text?.trim().match(/^\/start(?:@[A-Za-z0-9_]+)?\s+([A-Za-z0-9_-]{1,64})$/)?.[1];
      const authorizedUsername=store.get('authorizedUsername');
      const byUsername=authorizedUsername&&message.from.username?.toLowerCase()===authorizedUsername;
      const byCode=pairing&&pairing.expires>Date.now()&&startCode===pairing.code;
      if(!binding&&(byUsername||byCode)) {
        binding={chatId:message.chat.id,userId:message.from.id,...(message.from.username?{username:message.from.username}:{})};
        store.set('binding',binding);store.set('pairing',null);store.set('authorizedUsername',null);
        // The real /start message establishes the channel; let the bot greet the human.
      }
      if(binding&&message.chat.id===binding.chatId&&message.from.id===binding.userId) {
        const copy={...message};
        // Never store the one-time pairing secret in message history.
        if(pairing&&startCode===pairing.code)copy.text='/start';
        store.message(copy,'human');
        const text=`Telegram ${update.edited_message?'message edited':'message received'} in chat ${binding.chatId}, message ${message.message_id}. Read it with bot-app telegram read and reply through bot-app telegram send. This is a separate Telegram conversation; do not send the reply to terminal chat.`;
        await enqueue({id:`${store.get('identity').id}:${update.update_id}`,text,metadata:{chatId:binding.chatId,messageId:message.message_id,updateId:update.update_id}});
      }
    }
    // Acknowledge only after the durable inbox AND notification accept the update.
    store.set('offset',update.update_id+1);
  }
  store.set('watch',{state:store.get('binding')?'connected':'awaiting-pairing',checkedAt:Date.now(),error:null});
  return updates.length;
}
export async function send(store, api, {text,replyTo}, callId) {
  const binding=store.get('binding');if(!binding)throw new Error('Telegram is not paired');
  if(!callId)throw new Error('Sending requires a supervised shell action');
  const prior=store.delivery(callId);
  if(prior) {
    if(prior.text!==text||prior.reply_to!==(replyTo??null))throw new Error('Delivery ID already used for different content');
    if(prior.status==='sent')return {messageId:prior.message_id,duplicate:true};
    if(prior.status!=='rejected')throw new Error('Delivery outcome UNKNOWN. Do not repeat automatically; inspect Telegram before sending again.');
    throw new Error('This action was rejected. Use a new shell action to retry.');
  }
  store.startDelivery(callId,text,replyTo);
  let result;
  try {result=await api('sendMessage',{chat_id:binding.chatId,text,...(replyTo?{reply_parameters:{message_id:replyTo}}:{})});}
  catch(error){store.finishDelivery(callId,error.rejected?'rejected':'unknown',null,error.message);throw error;}
  store.finishDelivery(callId,'sent',result);
  return {messageId:result.message_id};
}
export async function watch(api, {signal}={}) {
  const folder=dataFolder(api.paths.workspace), store=new TelegramStore(folder);
  const pause=ms=>new Promise(resolve=>{const done=()=>{clearTimeout(timer);signal?.removeEventListener('abort',done);resolve();};const timer=setTimeout(done,ms);signal?.addEventListener('abort',done,{once:true});if(signal?.aborted)done();});
  try {
    while(!signal?.aborted) {
      try {
        const secret=await credentials(folder);
        if(!secret){store.set('watch',{state:'unconfigured',checkedAt:Date.now(),error:null});await pause(10000);continue;}
        const remote=telegramAPI(secret.token);
        const identity=await remote('getMe',{}, {signal,timeoutMs:10000});
        const prior=store.get('identity');if(prior&&prior.id!==identity.id)throw new Error('Telegram identity changed; configure a separate app inbox first');
        store.set('identity',identity);
        const webhook=await remote('getWebhookInfo',{}, {signal,timeoutMs:10000});
        if(webhook.url)throw new Error('Telegram has a webhook configured; remove it before using this app');
        while(!signal?.aborted)await receive(store,remote,event=>api.enqueue(event),{signal});
      } catch(error) {
        if(signal?.aborted)break;
        store.set('watch',{state:'error',checkedAt:Date.now(),error:error.message});
        await pause(5000);
      }
    }
  } finally {store.close();}
}
