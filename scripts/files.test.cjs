const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
function fixture(){
  class Element {
    constructor(){this.children=[];this.files=[];this.value='';this.textContent='';this.disabled=false;}
    append(...children){this.children.push(...children);}
    replaceChildren(...children){this.children=children;this.textContent='';}
    querySelectorAll(){return this.children.flatMap(row=>row.children.filter(c=>c.onclick));}
  }
  const elements={},pending=[],listeners={};
  const el=id=>elements[id]??=new Element();
  const scope={URL,AbortController,AbortSignal,DOMException,console,location:{href:'http://127.0.0.1/'},document:{getElementById:el,createElement:()=>new Element()},addEventListener:(name,fn)=>{listeners[name]=fn;},fetch:(address,options)=>new Promise((resolve,reject)=>pending.push({url:new URL(address),options,resolve,reject}))};
  vm.runInNewContext(fs.readFileSync(require.resolve('../internal/viewerapp/ui/files.js'),'utf8'),scope);
  scope.YuDeskFiles.mount({api:p=>p,canControl:true});
  const reply=(i,entries)=>pending[i].resolve({ok:true,json:async()=>({entries})});
  const flush=()=>new Promise(resolve=>setImmediate(resolve));
  return {el,pending,scope,reply,flush,close:()=>listeners.pagehide()};
}
test('a superseded directory response cannot overwrite the latest rows or paths',async()=>{
  const f=fixture();f.reply(0,[{name:'first',directory:true}]);await f.flush();
  f.el('fileList').children[0].children[1].onclick();
  assert.equal(f.pending[1].url.searchParams.get('path'),'first');
  assert.equal(f.el('upload').disabled,true);
  const back=f.el('fileUp').onclick();
  assert.equal(f.pending[1].options.signal.aborted,true);
  f.reply(2,[{name:'latest',directory:true}]);await back;
  f.reply(1,[{name:'stale',directory:true}]);await f.flush();
  assert.equal(f.el('fileDirectory').textContent,'接收目录 /');
  assert.equal(f.el('fileList').children[0].children[0].textContent,'latest /');
  f.el('fileList').children[0].children[1].onclick();
  assert.equal(f.pending[3].url.searchParams.get('path'),'latest');f.close();f.reply(3,[]);await f.flush();
});
test('superseded errors and page-close results never replace a current list',async()=>{
  const f=fixture();const fresh=f.el('refreshFiles').onclick();
  f.reply(1,[{name:'current',directory:false,size:1}]);await fresh;
  f.pending[0].reject(Error('stale request'));await f.flush();
  assert.equal(f.el('fileList').children[0].children[0].textContent,'current · 0.00 MiB');
  const closing=f.el('refreshFiles').onclick();f.close();assert.equal(f.pending[2].options.signal.aborted,true);
  f.reply(2,[{name:'after close'}]);await closing;
  assert.equal(f.el('fileList').textContent,'正在读取…');
});
test('file controls recover after an invalid directory response',async()=>{
  const f=fixture();f.reply(0,null);await f.flush();assert.match(f.el('fileList').textContent,/格式无效/);assert.equal(f.el('upload').disabled,false);f.close();
});
test('downloads reject missing sizes and oversize chunks without publishing a file',async()=>{
  for(const length of [null,'1073741825','0']){
    const f=fixture();f.reply(0,[{name:'file.bin',size:0}]);await f.flush();
    let saved=false,aborted=false,writes=0;
    f.scope.showSaveFilePicker=async()=>({createWritable:async()=>({write:async()=>writes++,close:async()=>{saved=true;},abort:async()=>{aborted=true;}})});
    f.el('fileList').children[0].children[1].onclick();await f.flush();
    f.pending[1].resolve({ok:true,headers:{get:()=>length},body:{getReader:()=>({read:async()=>({done:false,value:Uint8Array.of(1)}),cancel:async()=>{},releaseLock(){}})}});
    await f.flush();assert.equal(saved,false);assert.equal(aborted,true);assert.equal(writes,0);assert.equal(f.el('upload').disabled,false);f.close();
  }
});
