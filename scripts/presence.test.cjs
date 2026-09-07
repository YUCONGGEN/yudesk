const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const html=fs.readFileSync(require('node:path').join(__dirname,'../internal/viewerapp/ui/launcher.html'),'utf8');
const source=html.match(/<script>([\s\S]*?)<\/script>/)[1];
const tick=()=>new Promise(resolve=>setImmediate(resolve));
function fixture(){
  const requests=[],intervals=[],timeouts=[];
  const input={value:'A'.repeat(24),events:{},addEventListener(type,handler){this.events[type]=handler;}};
  const status={},button={},form={dataset:{},submitted:false,addEventListener(_,handler){this.submitHandler=handler;},submit(){this.submitted=true;}};
  const nodes={'#device-id':input,'#device-status':status,'#connect-form':form,'#connect-button':button};
  const context=vm.createContext({document:{body:{dataset:{token:'test'}},querySelector:q=>nodes[q],querySelectorAll:()=>[]},AbortSignal,encodeURIComponent,
    setInterval:fn=>intervals.push(fn),setTimeout:fn=>timeouts.push(fn),clearTimeout:()=>{},
    fetch:url=>url.includes('/watch')?Promise.resolve({}):new Promise(resolve=>requests.push({url,resolve:devices=>resolve({ok:true,json:async()=>({devices})})}))});
  vm.runInContext(source,context);
  return {requests,intervals,timeouts,input,status,button,form,context};
}
test('editing device immediately invalidates old online response before debounce',async()=>{
  const f=fixture();assert.equal(f.requests.length,1);
  f.input.value='B'.repeat(24);f.input.events.input();
  f.requests[0].resolve([{id:'A'.repeat(24),online:true,active:true,ready:true}]);await tick();
  assert.equal(f.button.disabled,true);assert.ok(!f.status.textContent.includes('可以连接'));
});
test('background refresh cannot invalidate submit check or allow an offline device',async()=>{
  const f=fixture();f.requests[0].resolve([{id:'A'.repeat(24),online:true,active:true,ready:true}]);await tick();
  const submitting=f.form.submitHandler({preventDefault(){}});assert.equal(f.requests.length,2);
  f.intervals[0]();assert.equal(f.requests.length,2);
  f.requests[1].resolve([{id:'A'.repeat(24),online:false,active:true}]);await submitting;
  assert.equal(f.form.submitted,false);assert.equal(f.button.disabled,true);
});
test('management-only online state is not advertised as ready',async()=>{
  const f=fixture();f.requests[0].resolve([{id:'A'.repeat(24),online:true,active:true,ready:false}]);await tick();
  assert.equal(f.button.disabled,true);assert.ok(f.status.textContent.includes('准备'));
});
