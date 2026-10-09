import { TelegramStore } from './database.mjs';
import { credentials, dataFolder, send } from './behavior.mjs';
import { telegramAPI } from './api.mjs';

export function commands(api) {
  async function use(fn) {
    const folder=dataFolder(api.paths.workspace),store=new TelegramStore(folder);
    try{return await fn(store,folder);}finally{store.close();}
  }
  return {
    read:{description:'Read received messages and sent replies in the separate Telegram app inbox. Latest 20 by default.',inputSchema:{type:'object',properties:{after:{type:'integer',minimum:0},limit:{type:'integer',minimum:1,maximum:100}},additionalProperties:false},execute:input=>use(async store=>{const latest=(await api.history()).findLast(message=>message.role==='user');return store.read({...input,through:latest?.metadata?.source==='telegram'?latest.metadata.messageId:undefined});})},
    send:{description:'Send a plain-text Telegram message to the paired human, optionally replying to a message ID. This does not publish to terminal chat.',inputSchema:{type:'object',properties:{text:{type:'string',minLength:1,maxLength:4096},replyTo:{type:'integer',minimum:1}},required:['text'],additionalProperties:false},execute:(input,context)=>use(async(store,folder)=>{const secret=await credentials(folder);if(!secret)throw new Error('Telegram is not configured');return send(store,telegramAPI(secret.token),input,context.callId);})},
    status:{description:'Inspect Telegram pairing, receiver health and uncertain sends without exposing credentials.',inputSchema:{type:'object',additionalProperties:false},execute:()=>use(async(store,folder)=>({...store.status(),configured:!!await credentials(folder)}))},
  };
}
