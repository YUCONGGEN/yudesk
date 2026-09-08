'use strict';

// Standalone, headless UI regression tests. Every HTTP endpoint is a loopback
// fixture; window actions never reach YuDesk or the user's real desktop.
// Run with NODE_PATH pointing to Playwright and optionally YUDESK_TEST_BROWSER.
const {test,before,after}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const http=require('node:http');
const path=require('node:path');
const {chromium}=require('playwright');

const uiRoot=path.join(__dirname,'../internal/viewerapp/ui');
const token='fixture token+&/中文';
const read=name=>fs.readFileSync(path.join(uiRoot,name),'utf8');
const source=read('window-ui.js');
const stylesheet=read('window-ui.css');
let browser;
before(async()=>{browser=await chromium.launch({executablePath:process.env.YUDESK_TEST_BROWSER,headless:true});});
after(async()=>{await browser?.close();});

function markup(surface){
  let html=surface==='transition'
    ?'<!doctype html><html><head><meta charset="utf-8"></head><body data-token="{{.Token}}"><p id="notice" hidden></p></body></html>'
    :read(surface+'.html').replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi,'');
  // Render only inert template values. No dashboard/session script is run;
  // controls and styles still come from their real source templates.
  html=html.replaceAll('{{.Token}}',token.replaceAll('&','&amp;')).replace(/{{[\s\S]*?}}/g,'');
  return html.replace('</body>','<script src="/assets/window-ui.js"></script></body>');
}

async function fixture(t,{surface='transition',pending=null,clock=false}={}){
  const state={pending,getStatus:200,postStatus:200,postGate:null,posts:[],requests:[]};
  const failures=[],nativeDialogs=[];
  const server=http.createServer((req,res)=>{
    (async()=>{
      const url=new URL(req.url,'http://127.0.0.1');
      const send=(status,body,type='application/json')=>{res.writeHead(status,{'Content-Type':type,'Cache-Control':'no-store'});res.end(typeof body==='string'?body:JSON.stringify(body));};
      if(req.method==='GET'&&url.pathname==='/')return send(200,markup(surface),'text/html; charset=utf-8');
      if(req.method==='GET'&&url.pathname.startsWith('/assets/')){
        const name=url.pathname.slice('/assets/'.length);
        if(name==='window-ui.js')return send(200,source,'text/javascript; charset=utf-8');
        if(name==='window-ui.css')return send(200,stylesheet,'text/css; charset=utf-8');
        if(['dashboard.css','session.css','footer.css','icon.svg','beian.svg'].includes(name))return send(200,read(name),name.endsWith('.svg')?'image/svg+xml':'text/css; charset=utf-8');
        return send(404,{});
      }
      if(url.pathname==='/favicon.ico')return send(204,'');
      const chunks=[];
      for await(const chunk of req){chunks.push(chunk);if(chunks.reduce((n,value)=>n+value.length,0)>8192)throw Error('unexpected fixture request size');}
      const text=Buffer.concat(chunks).toString('utf8');
      const body=text?(req.headers['content-type']?.includes('application/json')?JSON.parse(text):Object.fromEntries(new URLSearchParams(text))):null;
      const request={method:req.method,path:url.pathname,token:url.searchParams.get('access_token'),body,contentType:req.headers['content-type']};
      state.requests.push(request);
      if(url.pathname==='/api/local/approval'){
        if(req.method==='GET')return send(state.getStatus,state.getStatus===200?{pending:state.pending}:'approval unavailable');
        if(req.method==='POST'){
          state.posts.push(request);
          if(state.postGate)await state.postGate;
          if(state.postStatus!==200)return send(state.postStatus,'授权请求未完成，请重试');
          state.pending=null;
          return send(200,{ok:true});
        }
      }
      if(req.method==='POST'&&['/api/local/hide','/api/ui/minimize','/api/exit','/connect'].includes(url.pathname))return send(200,{ok:true});
      return send(404,{});
    })().catch(error=>{failures.push(error.message);if(!res.headersSent)res.writeHead(500);res.end();});
  });
  await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(0,'127.0.0.1',resolve);});
  const origin='http://127.0.0.1:'+server.address().port;
  const context=await browser.newContext({viewport:{width:1280,height:800}});
  t.after(async()=>{
    await context.close();
    server.closeAllConnections();
    await new Promise(resolve=>server.close(resolve));
    assert.deepEqual(nativeDialogs,[],'custom UI must never call browser alert/confirm/prompt');
    assert.deepEqual(failures,[],'fixture and page must have no uncaught errors');
  });
  // Disallow accidental external requests even if a source template changes.
  await context.route('**/*',route=>new URL(route.request().url()).origin===origin?route.continue():route.abort());
  const page=await context.newPage();
  page.setDefaultTimeout(6000);
  page.on('pageerror',error=>failures.push(error.message));
  page.on('dialog',dialog=>{nativeDialogs.push({type:dialog.type(),message:dialog.message()});void dialog.dismiss().catch(()=>{});});
  if(clock)await page.clock.install({time:new Date()});
  await page.goto(origin);
  await page.waitForFunction(()=>typeof window.yudeskConfirm==='function'&&!!document.getElementById('incomingApproval'));
  return {page,state,origin};
}

const pending=(mode='control',seconds=60,id='single-use-request')=>({id,mode,deadline:new Date(Date.now()+seconds*1000).toISOString()});
async function waitForPosts(state,count){
  const end=Date.now()+5000;
  while(state.posts.length<count&&Date.now()<end)await new Promise(resolve=>setTimeout(resolve,10));
  assert.equal(state.posts.length,count);
}
async function showApproval(page){
  await page.locator('#incomingApproval').waitFor({state:'visible'});
  await page.waitForFunction(()=>/\d+ 秒后自动拒绝/.test(document.querySelector('#incomingApproval .yu-countdown').textContent));
}

for(const [surface,hideSelector] of [['dashboard','#hideApp'],['session','[data-window-action="hide"]'],['transition','[data-window-action="hide"]']]){
  test(surface+': hide, minimize and close are ordered and minimize stays local',async t=>{
    const {page,state}=await fixture(t,{surface});
    const selectors=[hideSelector,'[data-window-action="minimize"]','[aria-label="关闭应用"]'];
    const domOrder=await page.evaluate(selectors=>{
      const nodes=selectors.map(selector=>document.querySelector(selector));
      return nodes.every((node,index)=>node&&(index===0||!!(nodes[index-1].compareDocumentPosition(node)&Node.DOCUMENT_POSITION_FOLLOWING)));
    },selectors);
    assert.equal(domOrder,true);
    assert.equal(await page.locator('[data-window-action="minimize"]').count(),1,'do not add a duplicate transition toolbar');
    const boxes=await Promise.all(selectors.map(selector=>page.locator(selector).boundingBox()));
    assert.ok(boxes.every(Boolean));
    assert.ok(boxes[0].x+boxes[0].width<=boxes[1].x+1&&boxes[1].x+boxes[1].width<=boxes[2].x+1,'minimize must be to the right of hide, not just later in the DOM');
    const response=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/ui/minimize');
    await page.locator(selectors[1]).click();await response;
    await page.waitForFunction(()=>!document.querySelector('[data-window-action="minimize"]').disabled);
    assert.deepEqual(state.requests.filter(r=>r.method==='POST').map(({path,body,token})=>({path,body,token})),[{path:'/api/ui/minimize',body:{},token}]);
    // Close is immediate and mocked; it must not ask for approval or disconnect
    // merely the remote session. No real process is terminated by this test.
    const closed=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/exit');
    await page.locator(selectors[2]).click();await closed;
    assert.equal(state.requests.filter(r=>r.path==='/api/exit'&&r.method==='POST').length,1);
    assert.equal(await page.locator('#appConfirm').isVisible(),false);
  });
}

test('dashboard accepts a device code without a PIN but rejects a partial PIN',async t=>{
  const {page}=await fixture(t,{surface:'dashboard'});
  await page.locator('#targetCode').fill('123456789');
  assert.equal(await page.locator('#targetPIN').getAttribute('required'),null);
  assert.equal(await page.locator('#connectForm').evaluate(form=>form.checkValidity()),true);
  await page.locator('#targetPIN').fill('12');
  assert.equal(await page.locator('#connectForm').evaluate(form=>form.checkValidity()),false);
  await page.locator('#targetPIN').fill('123456');
  assert.equal(await page.locator('#connectForm').evaluate(form=>form.checkValidity()),true);
});

test('custom service confirmation resolves cancel/accept/Escape without browser dialogs',async t=>{
  const {page,state}=await fixture(t);
  for(const [action,want] of [['cancel',false],['accept',true],['escape',false]]){
    await page.evaluate(()=>{window.confirmResult='waiting';void window.yudeskConfirm('安装服务需要管理员授权，继续？ <b>这不是 HTML</b>').then(value=>{window.confirmResult=value;});});
    await page.locator('#appConfirm').waitFor({state:'visible'});
    assert.equal(await page.locator('#appConfirm p').textContent(),'安装服务需要管理员授权，继续？ <b>这不是 HTML</b>');
    assert.equal(await page.locator('#appConfirm b').count(),0);
    assert.equal(await page.locator('#confirmCancel').evaluate(button=>button===document.activeElement),true,'safe option must receive initial focus');
    if(action==='escape')await page.keyboard.press('Escape');else await page.locator(action==='cancel'?'#confirmCancel':'#confirmAccept').click();
    await page.waitForFunction(value=>window.confirmResult===value,want);
    assert.equal(await page.locator('#appConfirm').isVisible(),false);
  }
  assert.deepEqual(state.requests.filter(r=>r.method==='POST'),[],'confirmation alone cannot invoke a service or window action');
});

test('a concurrent confirmation is refused without overwriting the first decision',async t=>{
  const {page}=await fixture(t);
  await page.evaluate(()=>{window.firstResult='waiting';void window.yudeskConfirm('第一项操作').then(value=>{window.firstResult=value;});});
  await page.locator('#appConfirm').waitFor({state:'visible'});
  assert.equal(await page.evaluate(()=>window.yudeskConfirm('第二项操作')),false);
  assert.equal(await page.locator('#appConfirm p').textContent(),'第一项操作');
  await page.locator('#confirmAccept').click();
  await page.waitForFunction(()=>window.firstResult===true);
});

for(const accept of [false,true]){
  test('incoming control request '+(accept?'allows':'denies')+' exactly the displayed request ID',async t=>{
    const request=pending('control',60,'approval-'+accept);
    const {page,state}=await fixture(t,{pending:request});
    await showApproval(page);
    assert.match(await page.locator('#incomingApproval').textContent(),/观看画面并操作本机鼠标、键盘/);
    assert.match(await page.locator('#incomingApproval').textContent(),/不会保存免密许可/);
    assert.equal(await page.locator('#denyConnection').evaluate(button=>button===document.activeElement),true);
    const seconds=Number((await page.locator('.yu-countdown').textContent()).match(/\d+/)[0]);
    assert.ok(seconds>0&&seconds<=60);
    // Exercise the actual stylesheet: compact, centered modal within viewport.
    const box=await page.locator('#incomingApproval').boundingBox();
    assert.ok(box.width<=340&&box.x>=0&&box.y>=0&&box.x+box.width<=1280&&box.y+box.height<=800);
    assert.equal(await page.locator('#incomingApproval').evaluate(dialog=>getComputedStyle(dialog).borderRadius),'14px');
    await page.locator(accept?'#allowConnection':'#denyConnection').click();
    await page.locator('#incomingApproval').waitFor({state:'hidden'});
    assert.equal(state.posts.length,1);
    assert.deepEqual(state.posts[0].body,{requestID:request.id,accept});
    assert.equal(state.posts[0].token,token);
    assert.equal(state.posts[0].contentType,'application/json');
  });
}

test('view-only consent explicitly forbids mouse and keyboard and Escape rejects it',async t=>{
  const request=pending('view');
  const {page,state}=await fixture(t,{pending:request});
  await showApproval(page);
  assert.match(await page.locator('#incomingApproval').textContent(),/观看本机画面，不允许操作鼠标和键盘/);
  await page.keyboard.press('Escape');
  await page.locator('#incomingApproval').waitFor({state:'hidden'});
  assert.deepEqual(state.posts.map(r=>r.body),[{requestID:request.id,accept:false}]);
});

test('double clicking and conflicting decisions cannot submit twice while resolving',async t=>{
  const request=pending();
  const {page,state}=await fixture(t,{pending:request});
  let release;
  state.postGate=new Promise(resolve=>{release=resolve;});
  t.after(()=>release());
  await showApproval(page);
  // Dispatch synchronously, including a conflicting denial, to exercise the
  // handler's own guard rather than relying only on disabled-button behavior.
  await page.evaluate(()=>{
    document.getElementById('allowConnection').click();
    document.getElementById('allowConnection').dispatchEvent(new MouseEvent('click',{bubbles:true}));
    document.getElementById('denyConnection').dispatchEvent(new MouseEvent('click',{bubbles:true}));
  });
  await waitForPosts(state,1);
  assert.equal(await page.locator('#allowConnection').isDisabled(),true);
  assert.equal(await page.locator('#denyConnection').isDisabled(),true);
  assert.deepEqual(state.posts.map(r=>r.body),[{requestID:request.id,accept:true}]);
  release();
  await page.locator('#incomingApproval').waitFor({state:'hidden'});
  await page.evaluate(()=>document.getElementById('allowConnection').dispatchEvent(new MouseEvent('click')));
  assert.equal(state.posts.length,1);
});

test('60-second expiry closes consent locally even if the approval server is unavailable',async t=>{
  const request=pending();
  const {page,state}=await fixture(t,{pending:request,clock:true});
  await showApproval(page);
  state.getStatus=503;
  await page.clock.fastForward(61000);
  await page.locator('#incomingApproval').waitFor({state:'hidden'});
  // A queued click after expiry must not resurrect or grant the request.
  await page.evaluate(()=>document.getElementById('allowConnection').dispatchEvent(new MouseEvent('click')));
  assert.equal(state.posts.length,0);
});

test('an already expired request is never displayed or sent as accepted',async t=>{
  const {page,state}=await fixture(t,{pending:pending('control',-1),clock:true});
  await page.clock.fastForward(1500);
  assert.equal(await page.locator('#incomingApproval').isVisible(),false);
  await page.evaluate(()=>document.getElementById('allowConnection').dispatchEvent(new MouseEvent('click')));
  assert.equal(state.posts.length,0);
});

test('a cancelled request closes; a replacement uses its new ID and mode',async t=>{
  const {page,state}=await fixture(t,{pending:pending('control',60,'old-request'),clock:true});
  await showApproval(page);
  state.pending=null;
  await page.clock.fastForward(1200);
  await page.locator('#incomingApproval').waitFor({state:'hidden'});
  assert.equal(state.posts.length,0);
  state.pending=pending('view',60,'replacement-request');
  await page.clock.fastForward(1200);
  await showApproval(page);
  assert.match(await page.locator('#incomingApproval').textContent(),/不允许操作鼠标和键盘/);
  await page.locator('#allowConnection').click();
  await page.locator('#incomingApproval').waitFor({state:'hidden'});
  assert.deepEqual(state.posts.map(r=>r.body),[{requestID:'replacement-request',accept:true}]);
});

test('a failed decision shows inline feedback and permits an explicit retry',async t=>{
  const request=pending();
  const {page,state}=await fixture(t,{pending:request});
  state.postStatus=503;
  await showApproval(page);
  await page.locator('#allowConnection').click();
  await page.waitForFunction(()=>document.getElementById('notice').textContent.includes('请重试'));
  assert.equal(await page.locator('#incomingApproval').isVisible(),true);
  await page.waitForFunction(()=>!document.getElementById('allowConnection').disabled&&!document.getElementById('denyConnection').disabled);
  state.postStatus=200;
  await page.locator('#denyConnection').click();
  await page.locator('#incomingApproval').waitFor({state:'hidden'});
  assert.deepEqual(state.posts.map(r=>r.body),[{requestID:request.id,accept:true},{requestID:request.id,accept:false}]);
});
