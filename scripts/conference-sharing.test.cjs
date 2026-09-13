'use strict';

// Local-only browser regression for the real conference controls. Screen
// capture and fullscreen are deterministic fakes; no desktop is captured.
const {test,before,after}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const http=require('node:http');
const path=require('node:path');
let playwright;
try{playwright=require('playwright');}catch(error){if(error.code!=='MODULE_NOT_FOUND')throw error;playwright=require('playwright-core');}
const {chromium}=playwright;

const root=path.join(__dirname,'../internal/viewerapp/ui');
const read=name=>fs.readFileSync(path.join(root,name),'utf8');
const dashboard=read('dashboard.js')+'\n'+read('conference.js');
const status={name:'会议测试设备',code:'123456789',pin:'654321',receiving:true,files:false,activeUntil:'永久授权',online:true,connected:false,active:true,ready:true,fileDirectory:'',portMaps:[]};
let browser,server,origin;

function pageHTML(){
  return read('dashboard.html')
    .replace(/{{if \.Windows}}([\s\S]*?){{else}}[\s\S]*?{{end}}/g,'$1')
    .replace(/{{if not \.Message}}hidden{{end}}/g,'hidden')
    .replace(/{{template "footer" \.}}/g,'')
    .replace(/{{\.[A-Za-z]+}}/g,'');
}

before(async()=>{
  server=http.createServer((req,res)=>{
    const url=new URL(req.url,'http://127.0.0.1');
    const send=(statusCode,body,type='application/json')=>{res.writeHead(statusCode,{'Content-Type':type,'Cache-Control':'no-store'});res.end(typeof body==='string'?body:JSON.stringify(body));};
    if(url.pathname==='/')return send(200,pageHTML(),'text/html; charset=utf-8');
    if(url.pathname==='/assets/dashboard.js')return send(200,dashboard,'text/javascript; charset=utf-8');
    if(url.pathname==='/assets/window-ui.js'||url.pathname==='/assets/compat.js')return send(200,'','text/javascript; charset=utf-8');
    if(url.pathname==='/assets/dashboard.css')return send(200,read('dashboard.css'),'text/css; charset=utf-8');
    if(url.pathname==='/assets/conference.css')return send(200,read('conference.css'),'text/css; charset=utf-8');
    if(url.pathname==='/assets/portmap.css')return send(200,read('portmap.css'),'text/css; charset=utf-8');
    if(url.pathname==='/assets/footer.css')return send(200,read('footer.css'),'text/css; charset=utf-8');
    if(url.pathname==='/assets/icon.svg')return send(200,read('icon.svg'),'image/svg+xml');
    if(url.pathname==='/favicon.ico')return send(204,'');
    if(url.pathname==='/api/local/status')return send(200,status);
    if(url.pathname==='/api/devices')return send(200,{local:status,devices:[]});
    if(url.pathname==='/api/local/service/status')return send(200,{supported:false});
    if(url.pathname.startsWith('/api/'))return send(200,{ok:true});
    send(404,{error:'not found'});
  });
  await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(0,'127.0.0.1',resolve);});
  origin='http://127.0.0.1:'+server.address().port+'/?access_token=fixture';
  browser=await chromium.launch({executablePath:process.env.YUDESK_TEST_BROWSER,headless:true});
});

after(async()=>{
  await browser?.close();
  if(server)await new Promise(resolve=>server.close(resolve));
});

async function fixture(){
  const context=await browser.newContext({viewport:{width:1100,height:760}}),page=await context.newPage();
  const errors=[],rejections=[],dialogs=[];
  page.on('pageerror',error=>errors.push(String(error)));
  page.on('dialog',dialog=>{dialogs.push(dialog.message());void dialog.dismiss();});
  await page.goto(origin,{waitUntil:'domcontentloaded'});
  await page.waitForFunction(()=>document.querySelector('#conferenceToast'));
  await page.evaluate(()=>{
    const stream=new MediaStream(),active=conference={
      code:'123456789',name:'主持人',requestedHost:true,stream,screen:null,shareBusy:false,speaker:true,
      audioElements:new Map(),audioMeters:new Map(),voiceLevels:new Map(),fullscreenMode:'',fullscreenBusy:false,
      ws:null,selfID:'self',hostID:'self',peers:new Map(),states:new Map(),tiles:new Map(),focusedID:'',
      followShare:false,memberQuery:'',intentional:false,heartbeat:0,recording:null,rtc:conferenceICEPolicy()
    };
    active.states.set('self',{id:'self',name:'主持人',joinedAt:1,microphone:false,camera:false,screen:false,recording:false});
    document.body.classList.add('conference-live');
    for(const pane of document.querySelectorAll('.pane'))pane.hidden=pane.id!=='pane-meeting';
    document.querySelector('#conferenceRoom').hidden=false;
    ensureConferenceTile('self','主持人',true,stream);
    updateConferenceControls();
    window.__testRejections=[];
    window.addEventListener('unhandledrejection',event=>{window.__testRejections.push(String(event.reason));});
  });
  return {context,page,errors,rejections,dialogs,close:()=>context.close()};
}

async function mockCapture(page,{cancel=false,fullscreen=false,delayed=true}={}){
  await page.evaluate(({cancel,fullscreen,delayed})=>{
    conference.fullscreenMode=fullscreen?'native':'';
    document.body.classList.toggle('conference-fullscreen',fullscreen);
    updateConferenceControls();
    window.__fullscreenDisableSettled=false;
    window.__captureCalled=false;
    window.__captureBeforeFullscreenSettled=false;
    const nativeFetch=window.fetch.bind(window);
    window.fetch=(input,options={})=>{
      const url=new URL(typeof input==='string'?input:input.url,location.href);
      let body={};try{body=JSON.parse(options.body||'{}');}catch(_){}
      if(url.pathname==='/api/ui/fullscreen'&&body.enabled===false&&delayed){
        return new Promise(resolve=>setTimeout(()=>{
          window.__fullscreenDisableSettled=true;
          resolve(new Response('{"ok":true}',{status:200,headers:{'Content-Type':'application/json'}}));
        },120));
      }
      return nativeFetch(input,options);
    };
    const canvas=document.createElement('canvas');canvas.width=64;canvas.height=48;
    const screen=canvas.captureStream(10);
    window.__capturedScreen=screen;
    let resolveCapture,rejectCapture;
    const pending=new Promise((resolve,reject)=>{resolveCapture=resolve;rejectCapture=reject;});
    Object.defineProperty(navigator.mediaDevices,'getDisplayMedia',{configurable:true,value:()=>{
      window.__captureCalled=true;
      window.__captureBeforeFullscreenSettled=!window.__fullscreenDisableSettled;
      if(cancel)return Promise.reject(new DOMException('Permission denied by user','NotAllowedError'));
      return delayed?pending:Promise.resolve(screen);
    }});
    window.__allowCapture=()=>resolveCapture(screen);
    window.__denyCapture=()=>rejectCapture(new DOMException('Permission denied by user','NotAllowedError'));
  },{cancel,fullscreen,delayed});
}

test('host button stays truthful through capture, fullscreen pause/restore and stop',async()=>{
  const f=await fixture();
  try{
    const share=f.page.locator('#conferenceShare');
    assert.equal(await share.isVisible(),true);
    assert.equal(await share.isEnabled(),true);
    assert.equal(await share.getAttribute('aria-pressed'),'false');
    assert.equal(await share.locator('small').textContent(),'共享屏幕');
    await mockCapture(f.page,{fullscreen:true,delayed:true});
    await share.click();
    await f.page.waitForFunction(()=>window.__captureCalled);
    assert.equal(await f.page.evaluate(()=>window.__captureBeforeFullscreenSettled),true,'capture must begin in the original click turn');
    assert.equal(await share.isEnabled(),false);
    assert.equal(await share.getAttribute('aria-busy'),'true');
    assert.equal(await share.locator('small').textContent(),'等待选择…');
    await f.page.evaluate(()=>window.__allowCapture());
    await f.page.waitForFunction(()=>conference.states.get('self').screen&&!conference.shareBusy&&conference.fullscreenMode==='native');
    assert.equal(await share.isEnabled(),true);
    assert.equal(await share.getAttribute('aria-pressed'),'true');
    assert.equal(await share.locator('small').textContent(),'停止共享');
    assert.match(await f.page.locator('#conferenceToast').textContent(),/屏幕正在共享/);
    assert.equal(await f.page.locator('#conferenceToast').isVisible(),true);
    assert.equal(await f.page.locator('#conferenceToast').getAttribute('role'),'status');
    await share.click();
    await f.page.waitForFunction(()=>!conference.states.get('self').screen&&!conference.shareBusy);
    assert.equal(await share.getAttribute('aria-pressed'),'false');
    assert.equal(await share.locator('small').textContent(),'共享屏幕');
    assert.match(await f.page.locator('#conferenceToast').textContent(),/共享已停止/);
    await mockCapture(f.page,{fullscreen:false,delayed:false});
    await share.click();
    await f.page.waitForFunction(()=>conference.states.get('self').screen&&!conference.shareBusy);
    await f.page.evaluate(()=>window.__capturedScreen.getVideoTracks()[0].dispatchEvent(new Event('ended')));
    await f.page.waitForFunction(()=>!conference.states.get('self').screen&&!conference.shareBusy);
    assert.match(await f.page.locator('#conferenceToast').textContent(),/系统已停止共享/);
    assert.deepEqual(f.errors,[]);
    assert.deepEqual(f.dialogs,[]);
    assert.deepEqual(await f.page.evaluate(()=>window.__testRejections),[]);
  }finally{await f.close();}
});

test('permission cancellation restores fullscreen and never escapes the application',async()=>{
  const f=await fixture();
  try{
    await mockCapture(f.page,{cancel:true,fullscreen:true,delayed:true});
    await f.page.locator('#conferenceShare').click();
    await f.page.waitForFunction(()=>window.__captureCalled&&!conference.shareBusy&&conference.fullscreenMode==='native');
    assert.equal(await f.page.locator('#conferenceShare').isEnabled(),true);
    assert.equal(await f.page.locator('#conferenceShare').getAttribute('aria-pressed'),'false');
    assert.match(await f.page.locator('#conferenceToast').textContent(),/已取消屏幕共享/);
    assert.equal(await f.page.locator('#conferenceToast').isVisible(),true);
    assert.equal(await f.page.locator('#conferenceToast').evaluate(element=>getComputedStyle(element).borderRadius),'10px');
    assert.equal(await f.page.evaluate(()=>conference!==null&&document.querySelector('#conferenceRoom').hidden===false),true);
    assert.deepEqual(f.errors,[]);
    assert.deepEqual(f.dialogs,[]);
    assert.deepEqual(await f.page.evaluate(()=>window.__testRejections),[]);
  }finally{await f.close();}
});

test('share control follows host ownership and focused view switches sharers',async()=>{
  const f=await fixture();
  try{
    await f.page.evaluate(()=>{
      const add=(id,name,screen)=>{
        const state={id,name,joinedAt:Date.now(),microphone:false,camera:false,screen,recording:false};
        conference.states.set(id,state);ensureConferenceTile(id,name,false,new MediaStream());updateConferenceTile(id);
      };
      add('first','甲',true);add('second','乙',false);
      conference.activeSharerID='first';focusConferenceShare('first',false);updateConferenceSharing();
    });
    assert.equal(await f.page.evaluate(()=>conference.focusedID),'first');
    await f.page.evaluate(()=>handleConferenceMessage({type:'state',id:'first',screen:false,microphone:false,camera:false,recording:false},conference));
    await f.page.waitForTimeout(80);
    await f.page.evaluate(()=>handleConferenceMessage({type:'state',id:'second',screen:true,microphone:false,camera:false,recording:false},conference));
    await f.page.waitForFunction(()=>conference.focusedID==='second');
    assert.equal(await f.page.locator('#conferenceSharing').textContent(),'乙 正在共享');
    assert.equal(await f.page.evaluate(()=>document.querySelector('#conferenceGrid').classList.contains('conference-grid-focused')),true);
    await f.page.evaluate(()=>{conference.hostID='second';updateConferenceControls();});
    assert.equal(await f.page.locator('#conferenceShare').isVisible(),false);
    await f.page.evaluate(()=>{conference.hostID='self';conference.shareBusy=true;updateConferenceControls();});
    assert.equal(await f.page.locator('#conferenceShare').isVisible(),true);
    assert.equal(await f.page.locator('#conferenceShare').isEnabled(),false);
    assert.equal(await f.page.locator('#conferenceShare').getAttribute('aria-busy'),'true');
    assert.deepEqual(f.errors,[]);
    assert.deepEqual(f.dialogs,[]);
    assert.deepEqual(await f.page.evaluate(()=>window.__testRejections),[]);
  }finally{await f.close();}
});
