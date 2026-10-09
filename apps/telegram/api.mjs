export function telegramAPI(token, {fetchImpl=fetch, baseURL='https://api.telegram.org'}={}) {
  if(!/^\d+:[A-Za-z0-9_-]+$/.test(token))throw new Error('Invalid Telegram bot token');
  return async (method, parameters={}, {signal,timeoutMs=40000}={}) => {
    let result;
    try {
      const response=await fetchImpl(`${baseURL}/bot${token}/${method}`, {method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(parameters),signal:signal?AbortSignal.any([signal,AbortSignal.timeout(timeoutMs)]):AbortSignal.timeout(timeoutMs)});
      result=await response.json();
    } catch {throw Object.assign(new Error(`Telegram ${method}: response unavailable; outcome may be unknown`),{unknown:true});}
    if(!result.ok)throw Object.assign(new Error(`Telegram ${method} rejected request (${result.error_code||'invalid response'})`),{rejected:true,retryAfter:result.parameters?.retry_after});
    return result.result;
  };
}
