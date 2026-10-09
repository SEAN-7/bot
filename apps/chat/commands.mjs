import { ChatStore } from './database.mjs';

export function commands(api) {
  async function use(fn) {
    const store=new ChatStore(api.paths.workspace);
    try {
      const history=await api.history();store.import(history);
      const latest=history.findLast(message=>message.role==='user'||message.metadata?.resumedInput);
      return await fn(store,latest?.metadata?.eventId);
    } finally {store.close();}
  }
  return {
    read:{description:'Read messages from the terminal chat app, which owns its history separately from the bot working journal. Latest 20 by default; after pages forward.',inputSchema:{type:'object',properties:{after:{type:'integer',minimum:0},limit:{type:'integer',minimum:1,maximum:100}},additionalProperties:false},execute:input=>use((store,eventId)=>store.read({...input,eventId}))},
    send:{description:'Send a durable message in the terminal chat app. Ordinary model output is private; use this for replies, questions and progress.',inputSchema:{type:'object',properties:{text:{type:'string',minLength:1,maxLength:100000}},required:['text'],additionalProperties:false},execute:({text},context)=>use((store,eventId)=>store.send(context.callId,text,eventId))},
  };
}
