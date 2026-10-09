// Chat is an external channel. Model text and channel messages have separate lives.
const human = message => message.role === 'user' && (!message.metadata?.source || message.metadata.source === 'chat');
const sent = message => message.role === 'channel' && message.metadata?.module === 'chat';
const legacyReply = message => message.role === 'assistant' && !message.metadata?.modelOutput && !!message.content;
import { ChatStore, notification, isNotification } from './database.mjs';

export function project(messages) {
  return messages.flatMap(message => {
    if (sent(message)) return [];
    if (human(message)) return [{ ...message, role: 'context', content: isNotification(message.content)?message.content:`Incoming terminal chat message ${message.id}. Read it with bot-app chat read; use bot-app chat send through shell to communicate.`, metadata: { ...message.metadata, input: true } }];
    // Preserve old replies through chat_read without treating them as new model output.
    if (legacyReply(message)) return message.toolCalls?.length
      ? [{ ...message, content: '', metadata: { ...message.metadata, nativeAssistant: message.metadata?.nativeAssistant ? { ...message.metadata.nativeAssistant, content: message.metadata.nativeAssistant.content.filter(block => block.type !== 'text') } : undefined } }]
      : [];
    return [message];
  });
}

export async function watch(api,{signal}={}) {
  const store=new ChatStore(api.paths.workspace);
  try {
    store.import(await api.history());
    while(!signal?.aborted) {
      for(const message of store.pending()) {
        await api.enqueue({id:message.event_id.slice('chat:'.length),text:notification(message.event_id)});
        store.notified(message.event_id);
      }
      await new Promise(resolve=>{const timer=setTimeout(done,250);function done(){clearTimeout(timer);signal?.removeEventListener('abort',done);resolve();}signal?.addEventListener('abort',done,{once:true});if(signal?.aborted)done();});
    }
  }finally{store.close();}
}
