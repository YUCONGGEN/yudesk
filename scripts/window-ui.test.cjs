'use strict';

// Standalone, headless UI regression tests. Every HTTP endpoint is a loopback
// fixture; window actions never reach YuDesk or the user's real desktop.
// Run with NODE_PATH pointing to Playwright and optionally YUDESK_TEST_BROWSER.
const {test,before,after}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const http=require('node:http');
const path=require('node:path');
// The bundled runtime may supply playwright-core with its matching browser.
let playwright;
try{playwright=require('playwright');}catch(error){if(error.code!=='MODULE_NOT_FOUND')throw error;playwright=require('playwright-core');}
const {chromium}=playwright;

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
    ?'<!doctype html><html><head><meta charset="utf-8"><style>html,body{width:100%;height:100%;overflow:hidden}body{margin:0;display:grid;place-items:center}body>div{max-width:70vw;padding:28px 36px;border:1px solid #ddd;border-radius:12px;background:#fff;box-shadow:0 8px 35px #0001}</style></head><body data-token="{{.Token}}"><div class="transition-card">等待对方确认</div><p id="notice" hidden></p></body></html>'
    :read(surface+'.html').replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi,'');
  // Render only inert template values. No dashboard/session script is run;
  // controls and styles still come from their real source templates.
  html=html.replaceAll('{{.Token}}',token.replaceAll('&','&amp;')).replace(/{{[\s\S]*?}}/g,'');
  return html.replace('</body>','<script src="/assets/window-ui.js"></script></body>');
}

async function fixture(t,{surface='transition',pending=null,clock=false,platform='Linux x86_64',scale=1}={}){
  const state={pending,getStatus:200,postStatus:200,postGate:null,posts:[],requests:[],dragPosts:[],dragStatus:200,dragGate:null,regionPosts:[],regionGate:null};
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
      if(url.pathname==='/api/ui/drag-regions'&&req.method==='POST'){
        state.regionPosts.push(request);
        const gate=state.regionGate;state.regionGate=null;
        if(gate)await gate;
        return send(200,{ok:true});
      }
      if(url.pathname==='/api/ui/drag'&&req.method==='POST'){
        state.dragPosts.push(request);
        if(state.dragGate)await state.dragGate;
        return send(state.dragStatus,state.dragStatus===200?{ok:true}:'模拟窗口拖动失败，请重试');
      }
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
      if(req.method==='POST'&&['/api/local/hide','/api/ui/minimize','/api/ui/close-to-tray','/api/exit','/connect'].includes(url.pathname))return send(200,{ok:true});
      return send(404,{});
    })().catch(error=>{failures.push(error.message);if(!res.headersSent)res.writeHead(500);res.end();});
  });
  await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(0,'127.0.0.1',resolve);});
  const origin='http://127.0.0.1:'+server.address().port;
  const context=await browser.newContext({viewport:{width:1280,height:800},deviceScaleFactor:scale});
  if(platform!==null)await context.addInitScript(value=>Object.defineProperty(navigator,'platform',{get:()=>value,configurable:true}),platform);
  await context.addInitScript(()=>{
    window.__fixtureMouseEvents=[];
    window.__fixtureLayoutEpoch=0;
    window.__fixtureRegionEmissions=[];
    const originalFetch=window.fetch;
    window.fetch=function(input,options){
      const url=new URL(typeof input==='string'?input:input.url,location.href);
      const emission=url.pathname==='/api/ui/drag-regions'
        ?{epoch:window.__fixtureLayoutEpoch,body:JSON.parse(options.body),settled:false}:null;
      if(emission)window.__fixtureRegionEmissions.push(emission);
      const promise=originalFetch.apply(this,arguments);
      // Observe only; return the original promise and preserve every request.
      if(emission)promise.then(()=>{emission.settled=true;},()=>{emission.settled=true;});
      return promise;
    };
    document.addEventListener('mousedown',event=>window.__fixtureMouseEvents.push({trusted:event.isTrusted,button:event.button}),true);
    document.addEventListener('contextmenu',event=>event.preventDefault());
  });
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
  await page.waitForFunction(()=>[...document.styleSheets].some(sheet=>sheet.href?.includes('/assets/window-ui.css')));
  if(platform!==null)assert.equal(await page.evaluate(()=>navigator.platform),platform);
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

async function blankDragPoint(page){
  const point=await page.evaluate(()=>{
    const region=document.querySelector('[data-window-drag]');
    if(!region)return null;
    const r=region.getBoundingClientRect();
    for(const y of [r.y+r.height/2,r.y+4,r.bottom-4]){
      for(let offset=0;offset<r.width/2-4;offset+=4){
        for(const x of [r.x+r.width/2+offset,r.x+r.width/2-offset]){
          if(document.elementFromPoint(x,y)===region)return {x,y};
        }
      }
    }
    return null;
  });
  assert.ok(point,'the actual header must expose blank space for dragging');
  return point;
}

async function flushInput(page){
  // Cross a renderer task and paint boundary before asserting that no fetch
  // was scheduled. Positive requests also wait for their real HTTP response.
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
}

async function trustedMouse(page,point,{button='left',move=false}={}){
  const count=await page.evaluate(()=>window.__fixtureMouseEvents.length);
  await page.mouse.move(point.x,point.y);
  await page.mouse.down({button});
  try{if(move)await page.mouse.move(point.x+25,point.y+4,{steps:4});}
  finally{await page.mouse.up({button});}
  const events=await page.evaluate(start=>window.__fixtureMouseEvents.slice(start),count);
  assert.deepEqual(events,[{trusted:true,button:button==='right'?2:0}],'use real Playwright mouse input, not dispatchEvent');
}

function assertDragPosts(state,count){
  assert.equal(state.dragPosts.length,count,'each accepted mouse-down must produce exactly one drag POST');
  for(const request of state.dragPosts){
    assert.equal(request.method,'POST');assert.equal(request.path,'/api/ui/drag');
    assert.equal(request.token,token,'preserve the exact token including reserved and Unicode characters');
    assert.equal(request.contentType,'application/json');assert.deepEqual(request.body,{});
  }
}

async function regionUpdate(page,state,action,predicate=()=>true){
  const start=state.regionPosts.length;
  await action();
  const deadline=Date.now()+5000;
  while(Date.now()<deadline){
    const update=state.regionPosts.slice(start).find(request=>predicate(request.body));
    if(update)return update;
    await new Promise(resolve=>setTimeout(resolve,10));
  }
  assert.fail('expected native drag-region update; received '+JSON.stringify(state.regionPosts.slice(start)));
}

async function emittedRegion(page,state,epoch,started=performance.now()){
  const deadline=started+1800;
  while(performance.now()<deadline){
    // Take the FIRST request constructed after this DOM mutation. Do not wait
    // for geometry that passes: a newly computed stale rectangle must fail.
    const emission=await page.evaluate(epoch=>window.__fixtureRegionEmissions.find(value=>value.epoch===epoch),epoch);
    if(emission){
      const request=state.regionPosts.find(value=>JSON.stringify(value.body)===JSON.stringify(emission.body));
      if(request){assert.ok(performance.now()-started<2000,'fresh regions must recover within 2 seconds');return request;}
    }
    await new Promise(resolve=>setTimeout(resolve,10));
  }
  assert.fail('no regions request constructed for DOM layout epoch '+epoch+' within 1800ms');
}

async function settleRegionEmissions(page){
  const deadline=Date.now()+5000;
  while(Date.now()<deadline){
    if(await page.evaluate(()=>window.__fixtureRegionEmissions.length>0&&window.__fixtureRegionEmissions.every(value=>value.settled))){
      await flushInput(page);return;
    }
    await new Promise(resolve=>setTimeout(resolve,10));
  }
  assert.fail('previous regions request did not settle');
}

function assertRegions(request,{width=1280,height=800,scale=1,empty=false}={}){
  assert.equal(request.method,'POST');assert.equal(request.path,'/api/ui/drag-regions');
  assert.equal(request.token,token);assert.equal(request.contentType,'application/json');
  const body=request.body;
  assert.deepEqual(Object.keys(body).sort(),['height','rects','scale','width']);
  assert.equal(body.width,width);assert.equal(body.height,height);assert.equal(body.scale,scale);
  assert.ok(Array.isArray(body.rects)&&body.rects.length<=16);
  if(empty)assert.deepEqual(body.rects,[]);else assert.ok(body.rects.length>0,'blank headers need at least one native hit region');
  for(const rect of body.rects){
    assert.deepEqual(Object.keys(rect).sort(),['height','width','x','y']);
    assert.ok(Object.values(rect).every(Number.isFinite));
    assert.ok(rect.x>=0&&rect.y>=0&&rect.width>0&&rect.height>0&&rect.x+rect.width<=width+.01&&rect.y+rect.height<=100);
  }
}

async function assertRegionsAvoidControls(page,request){
  const layout=await page.evaluate(()=>{
    const box=e=>{const r=e.getBoundingClientRect();return {x:r.x,y:r.y,width:r.width,height:r.height};};
    const bars=[...document.querySelectorAll('[data-window-drag]')];
    const controls=bars.flatMap(bar=>[...bar.querySelectorAll('button,a,input,label,select,textarea,form,[contenteditable],[role="button"],[role="link"]')])
      .map(box).filter(r=>r.width>0&&r.height>0);
    return {bars:bars.map(box),controls};
  });
  for(const rect of request.body.rects){
    assert.ok(layout.bars.some(bar=>rect.x>=bar.x-.01&&rect.y>=bar.y-.01&&rect.x+rect.width<=bar.x+bar.width+.01&&rect.y+rect.height<=bar.y+bar.height+.01),'native regions must remain inside DOM drag surfaces');
    for(const control of layout.controls){
      const overlapX=Math.min(rect.x+rect.width,control.x+control.width)-Math.max(rect.x,control.x);
      const overlapY=Math.min(rect.y+rect.height,control.y+control.height)-Math.max(rect.y,control.y);
      assert.ok(overlapX<=.01||overlapY<=.01,'native hit rectangle overlaps a control: '+JSON.stringify({rect,control}));
    }
  }
  // The native rectangles intentionally leave a small safety margin around
  // controls. A blank pixel immediately beside a control need not be covered.
  const usable=await page.evaluate(rects=>rects.some(r=>{
    const hit=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);
    return hit?.closest('[data-window-drag]')&&!hit.closest('button,a,input,label,select,textarea,form,[contenteditable],[role="button"],[role="link"]');
  }),request.body.rects);
  assert.ok(usable,'native regions must contain a usable non-interactive header point');
}

async function completedDrag(page,state,point,count,move=false){
  const response=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/ui/drag'&&r.request().method()==='POST');
  await trustedMouse(page,point,{move});
  await response;await flushInput(page);
  assertDragPosts(state,count);
}

for(const surface of ['dashboard','session','transition']){
  test(surface+': Windows blank-header clicks and repeated drags each POST once with the token',async t=>{
    const {page,state}=await fixture(t,{surface,platform:'Win32'});
    const point=await blankDragPoint(page);
    if(surface==='transition'){
      const bar=await page.locator('.yu-transition-controls').boundingBox();
      assert.ok(bar&&bar.x===0&&bar.width===1280&&bar.y===0&&bar.height>=40,'transition bar must span the viewport');
    }
    for(let count=1;count<=4;count++)await completedDrag(page,state,point,count,count!==1);
  });

  test(surface+': Windows drag excludes controls, non-drag content, right button and synthetic events',async t=>{
    const {page,state}=await fixture(t,{surface,platform:'Win32'});
    // Mount representative controls inside the real header so exclusions cannot
    // pass merely because the controls are outside a drag region.
    const controls=[
      '<button type="button"><span>Nested button</span></button>',
      '<input value="fixture input">', '<a href="#fixture"><span>Nested link</span></a>',
      '<label>Fixture label</label>', '<textarea>Fixture textarea</textarea>',
      '<form><span>Form content</span></form>', '<div contenteditable="true">Editable</div>',
      '<div role="button"><span>Role button</span></div>', '<div role="link"><span>Role link</span></div>'
    ];
    for(const html of controls){
      const point=await page.evaluate(html=>{
        document.getElementById('dragExclusionFixture')?.remove();
        const region=document.querySelector('[data-window-drag]'),r=region.getBoundingClientRect();
        const box=document.createElement('div');box.id='dragExclusionFixture';
        box.style.cssText='position:fixed;z-index:100;left:'+(r.x+40)+'px;top:'+(r.y+4)+'px;background:white;min-width:120px';
        box.innerHTML=html;region.append(box);
        const target=box.querySelector('span')||box.firstElementChild;
        const rect=target.getBoundingClientRect(),x=rect.x+rect.width/2,y=rect.y+rect.height/2;
        if(!box.contains(document.elementFromPoint(x,y)))throw Error('excluded control is not hit-testable');
        return {x,y};
      },html);
      await trustedMouse(page,point);await flushInput(page);assertDragPosts(state,0);
    }
    await page.evaluate(()=>document.getElementById('dragExclusionFixture').remove());
    const outside=await page.evaluate(()=>{
      const box=document.createElement('div');box.textContent='Non-drag fixture';
      box.style.cssText='position:fixed;left:40px;top:300px;width:200px;height:40px;z-index:100;background:white';
      document.body.append(box);return {x:80,y:320};
    });
    await trustedMouse(page,outside);await flushInput(page);assertDragPosts(state,0);
    const point=await blankDragPoint(page);
    await trustedMouse(page,point,{button:'right'});await flushInput(page);assertDragPosts(state,0);
    await page.evaluate(()=>{
      const region=document.querySelector('[data-window-drag]');
      region.dispatchEvent(new MouseEvent('mousedown',{bubbles:true,button:0}));
      region.dispatchEvent(new PointerEvent('pointerdown',{bubbles:true,button:0,isPrimary:true}));
    });
    await flushInput(page);assertDragPosts(state,0);
    await completedDrag(page,state,point,1);
  });

  test(surface+': Linux trusted header drags never POST to the Windows fallback',async t=>{
    const {page,state}=await fixture(t,{surface,platform:'Linux x86_64'});
    const point=await blankDragPoint(page);
    for(let n=0;n<3;n++)await trustedMouse(page,point,{move:true});
    await flushInput(page);assertDragPosts(state,0);assert.equal(state.regionPosts.length,0);
  });

  test(surface+': a pending Windows drag rejects duplicates and permits the next completed gesture',async t=>{
    const {page,state}=await fixture(t,{surface,platform:'Win32'});
    let release;state.dragGate=new Promise(resolve=>{release=resolve;});t.after(()=>release());
    const point=await blankDragPoint(page);
    const request=page.waitForRequest(r=>new URL(r.url()).pathname==='/api/ui/drag');
    const response=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/ui/drag');
    await trustedMouse(page,point);await request;
    for(let n=0;n<3;n++)await trustedMouse(page,point,{move:true});
    await flushInput(page);assertDragPosts(state,1);
    state.dragGate=null;release();await response;await flushInput(page);
    await completedDrag(page,state,point,2);
  });

  test(surface+': failed Windows drag displays the page notice and releases pending for retry',async t=>{
    const {page,state}=await fixture(t,{surface,platform:'Win32'});
    state.dragStatus=503;
    const point=await blankDragPoint(page);
    await completedDrag(page,state,point,1);
    const notice=page.locator(surface==='session'?'#sessionNotice':'#notice');
    await notice.waitFor({state:'visible'});
    assert.equal(await notice.isVisible(),true);
    assert.equal(await notice.textContent(),'模拟窗口拖动失败，请重试');
    state.dragStatus=200;await completedDrag(page,state,point,2);
  });

  test(surface+': Windows native regions describe blank headers and never overlap controls',async t=>{
    const {page,state}=await fixture(t,{surface,platform:'Win32',scale:1.5});
    const request=await regionUpdate(page,state,()=>page.setViewportSize({width:1100,height:720}),body=>body.width===1100&&body.height===720&&body.rects.length>0);
    assertRegions(request,{width:1100,height:720,scale:1.5});
    await assertRegionsAvoidControls(page,request);
    // Mutate after initialization: native hit areas must drop a newly inserted
    // interactive island, including its complete bounding box.
    await settleRegionEmissions(page);
    const started=performance.now();
    const epoch=await page.evaluate(()=>{
      ++window.__fixtureLayoutEpoch;
      const bar=document.querySelector('[data-window-drag]'),r=bar.getBoundingClientRect();
      const input=document.createElement('input');input.id='dynamicRegionInput';
      input.style.cssText='position:fixed;left:'+(r.x+r.width/2-50)+'px;top:'+(r.y+4)+'px;width:100px;height:25px;z-index:90';bar.append(input);
      return window.__fixtureLayoutEpoch;
    });
    const updated=await emittedRegion(page,state,epoch,started);
    assertRegions(updated,{width:1100,height:720,scale:1.5});
    await assertRegionsAvoidControls(page,updated);
    // Hold a pre-mutation response so publish's inFlight branch is exercised.
    // The periodic lease may recover a skipped dirty update, but must do so
    // within 2s and its FIRST post-mutation payload must already avoid input.
    await settleRegionEmissions(page);
    let release;state.regionGate=new Promise(resolve=>{release=resolve;});t.after(()=>release());
    await regionUpdate(page,state,()=>page.evaluate(()=>dispatchEvent(new Event('resize'))));
    const heldStarted=performance.now();
    const heldEpoch=await page.evaluate(()=>{
      ++window.__fixtureLayoutEpoch;
      const bar=document.querySelector('[data-window-drag]'),r=bar.getBoundingClientRect();
      document.getElementById('dynamicRegionInput').style.left=(r.x+80)+'px';
      return window.__fixtureLayoutEpoch;
    });
    await flushInput(page);release();
    const recovered=await emittedRegion(page,state,heldEpoch,heldStarted);
    assertRegions(recovered,{width:1100,height:720,scale:1.5});
    await assertRegionsAvoidControls(page,recovered);
    t.diagnostic('in-flight region mutation recovered in '+Math.round(performance.now()-heldStarted)+'ms');
  });

  test(surface+': opening a dialog clears native regions and closing restores them',async t=>{
    const {page,state}=await fixture(t,{surface,platform:'Win32'});
    const cleared=await regionUpdate(page,state,()=>page.evaluate(()=>{void window.yudeskConfirm('拖动区域隔离测试');}),body=>body.rects.length===0);
    assertRegions(cleared,{empty:true});
    const restored=await regionUpdate(page,state,()=>page.locator('#confirmCancel').click(),body=>body.rects.length>0);
    assertRegions(restored);await assertRegionsAvoidControls(page,restored);
  });

  test(surface+': real DOM fullscreen clears native regions and exiting restores them',async t=>{
    const {page,state}=await fixture(t,{surface,platform:'Win32'});
    // Attach to a genuine mouse activation; do not spoof fullscreenElement.
    await page.evaluate(()=>{
      const button=document.createElement('button');button.id='fixtureFullscreen';button.textContent='Fullscreen';
      button.style.cssText='position:fixed;left:20px;top:250px;z-index:100';
      button.onclick=()=>{void document.documentElement.requestFullscreen();};document.body.append(button);
    });
    const cleared=await regionUpdate(page,state,()=>page.locator('#fixtureFullscreen').click(),body=>body.rects.length===0);
    assert.equal(await page.evaluate(()=>document.fullscreenElement===document.documentElement),true);
    assertRegions(cleared,{width:cleared.body.width,height:cleared.body.height,empty:true});
    const restored=await regionUpdate(page,state,()=>page.evaluate(()=>document.exitFullscreen()),body=>body.rects.length>0);
    const size=page.viewportSize();assertRegions(restored,size);await assertRegionsAvoidControls(page,restored);
  });
}

test('Windows native regions renew while idle and stop after pagehide clears them',async t=>{
  const {page,state}=await fixture(t,{platform:'Win32',clock:true});
  const renewed=await regionUpdate(page,state,()=>page.clock.runFor(1100),body=>body.rects.length>0);
  assertRegions(renewed);
  const cleared=await regionUpdate(page,state,()=>page.evaluate(()=>dispatchEvent(new PageTransitionEvent('pagehide'))),body=>body.rects.length===0);
  assertRegions(cleared,{empty:true});
  const count=state.regionPosts.length;
  await page.clock.runFor(2100);assert.equal(state.regionPosts.length,count,'pagehide must stop lease renewal');
});

for(const platform of ['Linux x86_64','MacIntel']){
  test(platform+': resize, DOM changes and lease ticks never publish Windows regions',async t=>{
    const {page,state}=await fixture(t,{platform,clock:true});
    await page.setViewportSize({width:1000,height:700});
    await page.evaluate(()=>document.querySelector('[data-window-drag]').style.height='50px');
    await page.clock.runFor(2100);
    assert.deepEqual(state.regionPosts,[]);assertDragPosts(state,0);
  });
}

test('Windows drag delegates to regions inserted after window-ui.js initialized',async t=>{
  const {page,state}=await fixture(t,{platform:'Win32'});
  await page.evaluate(()=>{
    document.querySelector('[data-window-drag]').remove();
    const region=document.createElement('header');region.dataset.windowDrag='';
    region.style.cssText='position:fixed;inset:0 0 auto;height:60px;background:white';
    document.body.append(region);
  });
  await completedDrag(page,state,await blankDragPoint(page),1);
});

for(const [surface,hideSelector] of [['dashboard','#hideApp'],['transition','[data-window-action="hide"]']]){
  test(surface+': hide, minimize and close are ordered and minimize stays local',async t=>{
    const {page,state}=await fixture(t,{surface});
    const minimizeSelector=surface==='dashboard'?'.topbar [data-window-action="minimize"]':'[data-window-action="minimize"]';
    const closeSelector=surface==='dashboard'?'[aria-label="关闭窗口"]':'[aria-label="关闭应用"]';
    const closePath=surface==='dashboard'?'/api/ui/close-to-tray':'/api/exit';
    const selectors=[hideSelector,minimizeSelector,closeSelector];
    const domOrder=await page.evaluate(selectors=>{
      const nodes=selectors.map(selector=>document.querySelector(selector));
      return nodes.every((node,index)=>node&&(index===0||!!(nodes[index-1].compareDocumentPosition(node)&Node.DOCUMENT_POSITION_FOLLOWING)));
    },selectors);
    assert.equal(domOrder,true);
    assert.equal(await page.locator(minimizeSelector).count(),1,'the active window toolbar must expose one minimize action');
    const boxes=await Promise.all(selectors.map(selector=>page.locator(selector).boundingBox()));
    assert.ok(boxes.every(Boolean));
    assert.ok(boxes[0].x+boxes[0].width<=boxes[1].x+1&&boxes[1].x+boxes[1].width<=boxes[2].x+1,'minimize must be to the right of hide, not just later in the DOM');
    const response=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/ui/minimize');
    await page.locator(selectors[1]).click();await response;
    await page.waitForFunction(()=>!document.querySelector('[data-window-action="minimize"]').disabled);
    assert.deepEqual(state.requests.filter(r=>r.method==='POST').map(({path,body,token})=>({path,body,token})),[{path:'/api/ui/minimize',body:{},token}]);
    // Close is immediate and mocked; it must not ask for approval or disconnect
    // merely the remote session. No real process is terminated by this test.
    const closed=page.waitForResponse(r=>new URL(r.url()).pathname===closePath);
    await page.locator(selectors[2]).click();await closed;
    assert.equal(state.requests.filter(r=>r.path===closePath&&r.method==='POST').length,1);
    if(surface==='dashboard')assert.equal(state.requests.some(r=>r.path==='/api/exit'),false,'desktop dashboard close must preserve the online process');
    assert.equal(await page.locator('#appConfirm').isVisible(),false);
  });
}

test('meeting room exposes a working local minimize control',async t=>{
  const {page,state}=await fixture(t,{surface:'dashboard'});
  await page.evaluate(()=>{
    document.querySelector('#pane-meeting').hidden=false;
    document.querySelector('#conferenceRoom').hidden=false;
  });
  const minimize=page.locator('#conferenceMinimize');
  assert.equal(await minimize.count(),1);
  assert.equal(await minimize.getAttribute('data-window-action'),'minimize');
  assert.equal(await minimize.getAttribute('aria-label'),'最小化会议');
  const response=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/ui/minimize');
  await minimize.click();await response;
  await page.waitForFunction(()=>!document.querySelector('#conferenceMinimize').disabled);
  assert.deepEqual(state.requests.filter(r=>r.method==='POST').map(({path,body,token})=>({path,body,token})),[{path:'/api/ui/minimize',body:{},token}]);
});

test('session starts with settings collapsed and exposes only its minimize window action',async t=>{
  const {page,state}=await fixture(t,{surface:'session'});
  assert.equal(await page.locator('#settings').getAttribute('aria-expanded'),'false');
  assert.equal(await page.locator('#sessionPanel').isVisible(),false);
  assert.equal(await page.locator('[data-window-action="hide"]').count(),0);
  assert.equal(await page.locator('[aria-label="关闭应用"]').count(),0);
  assert.equal(await page.locator('[data-window-action="minimize"]').count(),1);
  const response=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/ui/minimize');
  await page.locator('[data-window-action="minimize"]').click();await response;
  await page.waitForFunction(()=>!document.querySelector('[data-window-action="minimize"]').disabled);
  assert.deepEqual(state.requests.filter(r=>r.method==='POST').map(({path,body,token})=>({path,body,token})),[{path:'/api/ui/minimize',body:{},token}]);
});

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
    assert.equal(await page.locator('#incomingApproval').evaluate(dialog=>getComputedStyle(dialog).borderRadius),'18px');
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
