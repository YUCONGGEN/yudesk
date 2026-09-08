'use strict';

// Reproducible marketing screenshots of the real client, with synthetic data.
// No real YuDesk process, screen capture, identity, PIN or server is accessed.
const fs=require('node:fs');
const path=require('node:path');
const http=require('node:http');
const assert=require('node:assert/strict');
const {chromium}=require('playwright');
const ui=path.join(__dirname,'../internal/viewerapp/ui');
const output=path.join(__dirname,'../cmd/yudesk-relay/site');
const read=name=>fs.readFileSync(path.join(ui,name),'utf8');
const local={id:'demo-only',name:'我的电脑',code:'123456789',pin:'000000',pinSynced:true,pinRevision:1,receiving:true,online:true,active:true,connected:false,files:false,fileDirectory:'演示接收目录',activeUntil:'演示授权'};
const devices=[{deviceID:'234567890',name:'办公室电脑'},{deviceID:'345678901',name:'家里的电脑'},{deviceID:'456789012',name:'工作笔记本'}];
const statuses=devices.map((d,i)=>({id:d.deviceID,deviceCode:d.deviceID,online:i!==2,connected:i===1,active:true,ready:i===0}));
let pending=null;
let html=read('dashboard.html').replace('{{template "footer" .}}',read('footer.html').replace('{{define "footer"}}','').replace('{{end}}',''))
  .replaceAll('{{.Version}}','2.0.0').replaceAll('{{.Token}}','public-demo-not-a-token').replaceAll('{{if not .Message}}hidden{{end}}','hidden').replace(/{{[\s\S]*?}}/g,'');
const server=http.createServer((req,res)=>{
  const url=new URL(req.url,'http://localhost');
  const send=(body,type='application/json')=>{res.writeHead(200,{'Content-Type':type});res.end(typeof body==='string'?body:JSON.stringify(body));};
  if(url.pathname==='/')return send(html,'text/html; charset=utf-8');
  const asset=url.pathname.match(/^\/assets\/([a-z-]+\.(?:css|js|svg))$/);
  if(asset){try{return send(read(asset[1]),asset[1].endsWith('.css')?'text/css':asset[1].endsWith('.js')?'text/javascript':'image/svg+xml');}catch{}}
  if(url.pathname==='/api/local/status')return send(local);
  if(url.pathname==='/api/devices')return send({local,devices});
  if(url.pathname==='/api/device/status')return send({devices:statuses});
  if(url.pathname==='/api/local/service/status')return send({supported:false});
  if(url.pathname==='/api/local/approval')return send({pending});
  if(url.pathname==='/api/ui/watch')return send({});
  res.writeHead(404);res.end();
});
(async()=>{
  let browser;
  try{
    await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
    const origin='http://127.0.0.1:'+server.address().port;
    browser=await chromium.launch({headless:true,executablePath:process.env.YUDESK_TEST_BROWSER});
    const context=await browser.newContext({viewport:{width:860,height:562},deviceScaleFactor:2});
    await context.route('**/*',r=>new URL(r.request().url()).origin===origin?r.continue():r.abort());
    const page=await context.newPage(),errors=[];
    page.on('pageerror',e=>errors.push(e.message));
    await page.goto(origin);
    await page.waitForFunction(()=>document.getElementById('localCode').textContent==='123 456 789'&&document.querySelectorAll('.device-row').length===4);
    await page.locator('#togglePIN').click();
    await page.evaluate(()=>document.fonts.ready);
    fs.mkdirSync(output,{recursive:true});
    await page.screenshot({path:path.join(output,'desktop-home.png')});
    await page.locator('nav [data-tab="devices"]').click();
    await page.waitForFunction(()=>document.getElementById('pane-devices').hidden===false&&document.querySelector('[data-presence-id="345678901"]').textContent==='连接中');
    await page.screenshot({path:path.join(output,'desktop-devices.png')});
    await page.locator('nav [data-tab="remote"]').click();
    pending={id:'public-demo-approval',mode:'control',deadline:new Date(Date.now()+60000).toISOString()};
    await page.locator('#incomingApproval').waitFor({state:'visible'});
    await page.screenshot({path:path.join(output,'desktop-approval.png')});
    assert.deepEqual(errors,[]);
    console.log('Generated three real-UI screenshots with synthetic demonstration data.');
  }finally{
    if(browser)await browser.close();
    server.closeAllConnections();
    await new Promise(resolve=>server.close(resolve));
  }
})().catch(e=>{console.error(e);process.exitCode=1;});
