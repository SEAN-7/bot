import { appClient } from './app-client.mjs';
import { createInterface } from 'node:readline';
const [app,operation,argument='{}',...extra]=process.argv.slice(2);
try {
  if(!app||app==='--help')console.log('Usage: botctl APP OPERATION [JSON | --stdin]\n       botctl APP --session\nOperations: config, history, contribute, state.get, state.set, state.delete, publish, enqueue, update.request.\n--session accepts JSON lines {id,operation,args} and returns {id,result|error}.');
  else {
    if(extra.length)throw new Error('Pass one JSON argument or --stdin');
    const request=appClient(app);
    if(operation==='--session') {
      for await(const line of createInterface({input:process.stdin})) {
        const message=JSON.parse(line);
        if(line.length>150000)throw new Error('Request too large');
        try {console.log(JSON.stringify({id:message.id,result:await request(message.operation,message.args)??null}));}
        catch(error){console.log(JSON.stringify({id:message.id,error:error.message}));}
      }
    } else {
      let input=argument;
      if(argument==='--stdin'){input='';for await(const chunk of process.stdin){input+=chunk;if(input.length>150000)throw new Error('Request too large');}}
      console.log(JSON.stringify(await request(operation,JSON.parse(input))??null));
    }
  }
}catch(error){console.error(`botctl: ${error.message}`);process.exitCode=1;}
