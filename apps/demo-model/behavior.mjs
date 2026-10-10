// Deterministic isolated fixture: no provider access. Chat uses the same shell apps.
export function createModel(api) {
  return { async complete({messages,signal}) {
    signal?.throwIfAborted();
    const input=messages.findLast(message=>message.metadata?.input);
    if(!input) return {text:'',toolCalls:[]};
    const current=messages.slice(messages.indexOf(input)+1);
    const calls=current.filter(message=>message.role==='assistant').flatMap(message=>message.toolCalls||[]);
    const results=current.filter(message=>message.role==='tool'&&message.toolName==='shell'&&!message.isError);
    // A launch receipt may precede its final output. Wait for the completion
    // notification, then poll once; do not treat a running read/send as done.
    const pending=results.findLast(result=>JSON.parse(result.content).outcome==='running');
    if(pending){
      const {sessionId}=JSON.parse(pending.content);
      const polled=results.some(result=>JSON.parse(result.content).outcome!=='running'&&calls.some(call=>call.id===result.toolCallId&&call.arguments.sessionId===sessionId));
      if(!polled){
        if(!current.some(message=>message.metadata?.source==='shell'&&message.metadata.sessionId===sessionId))return {text:'',toolCalls:[]};
        return {text:'',toolCalls:[{id:crypto.randomUUID(),name:'shell',arguments:{sessionId,yieldTimeMs:0}}]};
      }
    }
    const finalFor=prefix=>{
      const launch=results.findLast(result=>calls.some(call=>call.id===result.toolCallId&&call.arguments.command?.startsWith(prefix)));
      if(!launch)return;
      const receipt=JSON.parse(launch.content);
      return receipt.outcome!=='running'?launch:results.findLast(result=>JSON.parse(result.content).outcome!=='running'&&calls.some(call=>call.id===result.toolCallId&&call.arguments.sessionId===receipt.sessionId));
    };
    const sent=finalFor('bot-app chat send');
    if(sent)return {text:'',toolCalls:[]};
    const read=finalFor('bot-app chat read');
    if(!read)return {text:'',toolCalls:[{id:crypto.randomUUID(),name:'shell',arguments:{command:"bot-app chat read '{}'"}}]};
    const human=JSON.parse(JSON.parse(read.content).stdout).messages.findLast(message=>message.sender==='human');
    const args=JSON.stringify({text:`Demo mode · no AI connected\n\nI received: ${human?.text??'(no human message)'}`});
    const quoted="'"+args.replaceAll("'", "'\\''")+"'";
    return {text:'',toolCalls:[{id:crypto.randomUUID(),name:'shell',arguments:{command:'bot-app chat send '+quoted}}]};
  }};
}
