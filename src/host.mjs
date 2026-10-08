import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import Ajv from 'ajv';
import { manifestAt } from './releases.mjs';

export class PluginHost {
  constructor({config,revisionDir,workspace,drafts,rpc}) {
    Object.assign(this,{config,revisionDir,workspace,drafts,rpc});
    this.tools=new Map(); this.hooks=new Map(); this.model=null; this.context=null;
    this.ajv=new Ajv({allErrors:true,strict:false});
  }
  async load() {
    for(const [id,options] of Object.entries(this.config.modules||{})) {
      if(!options.enabled) continue;
      const module=join(this.revisionDir,'plugins',id);
      const manifest=await manifestAt(module);
      const state={
        get:(key,fallback)=>this.rpc('state.get',{module:id,key,fallback}),
        set:(key,value)=>this.rpc('state.set',{module:id,key,value}),
        delete:key=>this.rpc('state.delete',{module:id,key})
      };
      const api={
        config:Object.freeze({...options}), paths:Object.freeze({module,workspace:this.workspace,drafts:this.drafts}), state,
        history:()=>this.rpc('history'), enqueue:event=>this.rpc('enqueue',{module:id,event}),
        log:message=>this.rpc('log',{module:id,message:String(message)}),
        proposeUpdate:()=>this.rpc('update.request',{module:id}),
        trackProcess:pid=>this.rpc('process.track',{pid}), releaseProcess:pid=>this.rpc('process.release',{pid}),
        registerTool:tool=>{
          if(!tool||!/^[a-zA-Z][a-zA-Z0-9_-]{0,63}$/.test(tool.name)||typeof tool.execute!=='function'||typeof tool.description!=='string') throw new Error(`Invalid tool in ${id}`);
          if(this.tools.has(tool.name)) throw new Error(`Duplicate tool: ${tool.name}`);
          const validate=this.ajv.compile(tool.inputSchema||{type:'object'});
          this.tools.set(tool.name,{...tool,owner:id,validate});
        },
        registerModel:model=>{
          if(this.model) throw new Error('Only one model implementation may be enabled');
          if(typeof model?.complete!=='function') throw new Error('Model needs complete()');
          this.model=model;
        },
        registerContext:context=>{
          if(this.context) throw new Error('Only one context manager may be enabled');
          if(typeof context!=='function') throw new Error('Context manager must be a function');
          this.context=context;
        },
        on:(event,handler)=>{
          if(!['beforeModel','afterTurn','tick','start','stop'].includes(event)||typeof handler!=='function') throw new Error(`Invalid hook: ${event}`);
          if(!this.hooks.has(event)) this.hooks.set(event,[]);
          this.hooks.get(event).push({module:id,handler});
        }
      };
      const imported=await import(pathToFileURL(join(module,manifest.entry)).href);
      if(typeof imported.default!=='function') throw new Error(`Module ${id} needs a default factory`);
      await imported.default(Object.freeze(api));
    }
    if(!this.model) throw new Error('Enable exactly one model module');
  }
  inventory() { return [...this.tools.values()].map(({name,description,inputSchema})=>({name,description,inputSchema:inputSchema||{type:'object'}})); }
  async emit(event,context={}) {
    for(const hook of this.hooks.get(event)||[]) {
      const result=await hook.handler(context);
      if(event==='beforeModel'&&result!==undefined) {
        if(!Array.isArray(result)) throw new Error(`${hook.module}: beforeModel must return an array`);
        for(const item of result) {
          if(typeof item?.content!=='string'||item.content.length>100_000) throw new Error('Invalid context contribution');
          await this.rpc('contribute',{module:hook.module,...item});
        }
      }
    }
  }
  async prepare(signal) {
    const settings=this.config.model||{};
    let messages=await this.rpc('history');
    await this.emit('beforeModel',{messages,settings,signal});
    messages=await this.rpc('history');
    if(this.context) {
      const result=await this.context({messages,tools:this.inventory(),settings,model:this.model,signal});
      if(!Array.isArray(result?.messages)||result.messages.some(m=>typeof m.content!=='string')) throw new Error('Invalid prepared context');
      // A context strategy may summarize history, but must retain the current input.
      const latestUser=[...messages].reverse().find(m=>m.role==='user');
      if(latestUser&&!result.messages.some(m=>m.id===latestUser.id)) throw new Error('Context manager removed the current user input');
      messages=result.messages;
    }
    return {messages,tools:this.inventory(),settings,signal};
  }
}
