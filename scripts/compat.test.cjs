const test=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const source=fs.readFileSync(require('node:path').join(__dirname,'../internal/viewerapp/ui/compat.js'),'utf8');
function setup(existing={}){const context={AbortSignal:existing,AbortController,DOMException,setTimeout};vm.runInNewContext(source,context);return context.AbortSignal;}
test('WebKit fallback preserves timeout errors and bounded input',async()=>{
  const signal=setup().timeout(5);
  await new Promise(resolve=>signal.addEventListener('abort',resolve,{once:true}));
  assert.equal(signal.aborted,true);assert.equal(signal.reason.name,'TimeoutError');
  for(const n of [-1,Infinity,NaN,2147483648])assert.throws(()=>setup().timeout(n));
});
test('WebKit any fallback forwards cancellation and removes listeners',()=>{
  const sources=[new AbortController(),new AbortController()];let added=0,removed=0;
  for(const {signal} of sources){const add=signal.addEventListener.bind(signal),remove=signal.removeEventListener.bind(signal);signal.addEventListener=(...args)=>{added++;return add(...args);};signal.removeEventListener=(...args)=>{removed++;return remove(...args);};}
  const signal=setup().any(sources.map(c=>c.signal));
  sources[1].abort('cancelled');assert.equal(signal.aborted,true);assert.equal(signal.reason,'cancelled');assert.equal(added,2);assert.equal(removed,2);
});
test('WebKit any handles pre-aborted signals and preserves native methods',()=>{
  const source=new AbortController();source.abort('prior');
  assert.equal(setup().any([source.signal]).reason,'prior');assert.throws(()=>setup().any([null]));
  const any=()=>{},timeout=()=>{},native={any,timeout};setup(native);assert.equal(native.any,any);assert.equal(native.timeout,timeout);
});
