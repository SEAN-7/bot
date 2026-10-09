// Deterministic isolated fixture: no provider access. Chat uses the same shell apps.
export function createModel(api) {
  return { async complete({messages,signal}) {
    signal?.throwIfAborted();
    const input=messages.findLast(message=>message.metadata?.input);
    if(!input) return {text:'Demo mode · no AI connected',toolCalls:[]};
    const current=messages.slice(messages.indexOf(input)+1);
    const calls=current.filter(message=>message.role==='assistant').flatMap(message=>message.toolCalls||[]);
    const results=current.filter(message=>message.role==='tool'&&message.toolName==='shell'&&!message.isError);
    const sent=results.find(result=>calls.some(call=>call.id===result.toolCallId&&call.arguments.command?.startsWith('bot-app chat send')));
    if(sent)return {text:'',toolCalls:[]};
    const read=results.findLast(result=>calls.some(call=>call.id===result.toolCallId&&call.arguments.command?.startsWith('bot-app chat read')));
    if(!read)return {text:'',toolCalls:[{id:crypto.randomUUID(),name:'shell',arguments:{command:"bot-app chat read '{}'"}}]};
    const human=JSON.parse(JSON.parse(read.content).stdout).messages.findLast(message=>message.sender==='human');
    const args=JSON.stringify({text:`Demo mode · no AI connected\n\nI received: ${human?.text??'(no human message)'}`});
    const quoted="'"+args.replaceAll("'", "'\\''")+"'";
    return {text:'',toolCalls:[{id:crypto.randomUUID(),name:'shell',arguments:{command:'bot-app chat send '+quoted}}]};
  }};
}
