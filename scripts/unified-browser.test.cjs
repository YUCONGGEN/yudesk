// Native app fixture; localhost only; OS input is intercepted, never injected.
const assert=require('node:assert/strict');
const path=require('node:path');
const {chromium}=require('playwright');
(async()=>{
  const [leftURL,rightURL,remoteCode,remotePIN]=process.argv.slice(2);
  for(const value of [leftURL,rightURL])assert.equal(new URL(value).hostname,'127.0.0.1');
  const browser=await chromium.launch({executablePath:process.env.YUDESK_TEST_BROWSER,headless:true});
  const errors=[];
  try{
    const context=await browser.newContext({viewport:{width:860,height:562}});
    const left=await context.newPage(),right=await context.newPage();
    const noPageScroll=async page=>{assert.deepEqual(await page.evaluate(()=>{window.scrollTo(100,100);return [window.scrollX,window.scrollY,document.documentElement.scrollWidth<=innerWidth,document.documentElement.scrollHeight<=innerHeight];}),[0,0,true,true]);};
    const checkFooter=async page=>{
      assert.match(await page.locator('.designer-credit').textContent(),/郁从根.*17739798184/);
      assert.equal(await page.locator('.product-motto').textContent(),'低延迟高响应');
      assert.equal(await page.locator('.icp-link').getAttribute('href'),'https://beian.miit.gov.cn/');
      assert.equal(await page.locator('.icp-link').getAttribute('rel'),'noopener noreferrer');
      await page.waitForFunction(()=>document.querySelector('.icp-link img').naturalWidth===14);
      assert.ok(await page.evaluate(()=>{const f=document.querySelector('.app-footer').getBoundingClientRect(),a=document.querySelector('.icp-link').getBoundingClientRect();return f.bottom<=innerHeight&&Math.abs((a.left+a.right)/2-innerWidth/2)<2;}));
    };
    for(const page of [left,right]){page.on('pageerror',e=>errors.push(e.message));await page.route(u=>u.pathname.startsWith('/api/input'),route=>route.fulfill({json:{ok:true}}));}
    await left.goto(leftURL);await right.goto(rightURL);
    for(const page of [left,right])await page.waitForFunction(()=>/^\d{3} \d{3} \d{3}$/.test(document.getElementById('localCode').textContent));
    assert.equal(await left.locator('#activation').getAttribute('open'),null);
    assert.equal(await left.locator('#activationKey').isVisible(),false);
    assert.match(await left.locator('#localPIN').textContent(),/^\d{6}$/);
    assert.ok((await left.locator('.device-field').boundingBox()).width<=200);
    assert.ok((await left.locator('.pin-field').boundingBox()).width<=112);
    await noPageScroll(left);
    await checkFooter(left);
    const rotate=async page=>{
      const old=await page.locator('#localPIN').textContent();
      assert.ok((await page.locator('#rotatePIN').boundingBox()).x>=(await page.locator('#togglePIN').boundingBox()).x);
      await page.locator('#rotatePIN').click();
      await page.waitForFunction(previous=>document.getElementById('localPIN').textContent!==previous,old);
      assert.match(await page.locator('#localPIN').textContent(),/^\d{6}$/);
      await page.waitForFunction(()=>document.getElementById('notice').textContent.includes('同步后台'),{}, {timeout:12000});
      await page.waitForFunction(()=>document.getElementById('notice').textContent.includes('已更换并同步后台'),{}, {timeout:12000});
    };
    await rotate(left);
    assert.equal(await left.locator('#pane-remote #activation').count(),0);
    assert.equal(await left.locator('#pane-settings #activation').count(),1);
    await left.locator('nav [data-tab="settings"]').click();
    await left.locator('#activation summary').click();assert.equal(await left.locator('#activationKey').isVisible(),true);await noPageScroll(left);
    assert.ok(await left.evaluate(()=>document.getElementById('installService').getBoundingClientRect().bottom<document.querySelector('.app-footer').getBoundingClientRect().top));
    await left.screenshot({path:path.resolve('.smoke/compact-settings-expanded.png')});await left.locator('#activation summary').click();
    // Test optional admin-consent controls only with mocked endpoints. This
    // fixture must never install/uninstall services or restart the user's app.
    const serviceActions=[];
    let installed=false;
    const serviceMatch=u=>u.pathname.startsWith('/api/local/service/');
    await left.route(serviceMatch,route=>{
      const pathname=new URL(route.request().url()).pathname;
      if(pathname.endsWith('/status'))return route.fulfill({json:{supported:true,installed,running:installed,trustedClient:false,message:installed?'服务已安装，请切换到安装版':'便携模式：锁屏控制需安装服务'}});
      assert.equal(route.request().postDataJSON().confirmed,true);
      serviceActions.push(pathname);
      installed=pathname.endsWith('/install');
      return route.fulfill({json:{ok:true}});
    });
    const accept=dialog=>dialog.accept();left.on('dialog',accept);
    await left.locator('nav [data-tab="settings"]').click();
    await noPageScroll(left);
    await left.screenshot({path:path.resolve('.smoke/compact-settings.png')});
    await left.locator('#installService').click();
    await left.waitForFunction(()=>document.getElementById('restartInstalled').hidden===false);
    await left.locator('#removeService').click();
    await left.waitForFunction(()=>document.getElementById('removeService').hidden===true);
    assert.deepEqual(serviceActions,['/api/local/service/install','/api/local/service/remove']);
    left.off('dialog',accept);
    await left.unroute(serviceMatch);
    await left.locator('nav [data-tab="devices"]').click();await left.locator('#addDevice').click();
    await left.locator('#addName').fill('办公室电脑');await left.locator('#addCode').fill(remoteCode);await left.locator('#addForm button[type="submit"]').click();
    await left.waitForFunction(()=>document.querySelectorAll('.device-row').length===2);
    assert.match(await left.locator('#deviceRows').textContent(),/办公室电脑/);
    await left.locator('#deviceSearch').fill('办公室');assert.equal(await left.locator('.device-row:visible').count(),1);await left.locator('#deviceSearch').fill('');
    await left.screenshot({path:path.resolve('.smoke/unified-devices.png')});
    await left.locator('nav [data-tab="remote"]').click();
    assert.ok(await left.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
    assert.ok((await left.locator('#connectButton').boundingBox()).height<=46);
    await left.screenshot({path:path.resolve('.smoke/unified-dashboard.png')});
    // Pause must withdraw the waiting data connection, not just change a switch.
    await right.locator('#receiving').uncheck();
    const leftEndpoint=p=>{const u=new URL(leftURL);u.pathname=p;return u;};
    const status=leftEndpoint('/api/device/status');status.searchParams.set('ids',remoteCode);
    async function waitPresence(ready){const until=Date.now()+25000;while(Date.now()<until){const r=await left.request.get(status.href);const value=await r.json();if(value.devices[0].ready===ready)return;await new Promise(resolve=>setTimeout(resolve,400));}throw Error('presence did not change to '+ready);}
    await waitPresence(false);
    await right.locator('#receiving').check();
    await waitPresence(true);
    await right.locator('nav [data-tab="files"]').click();
    // Keep permission off: this fixture must not create a directory in the user's home.
    assert.equal(await right.locator('#filePermission').isChecked(),false);
    await right.locator('nav [data-tab="remote"]').click();
    await left.locator('#targetCode').fill(remoteCode);await left.locator('#targetPIN').fill(remotePIN);await left.locator('#connectButton').click();
    try{await left.waitForFunction(()=>document.getElementById('screen')?.dataset.ready==='1');}catch(error){throw Error('session failed: '+await left.locator('#notice').textContent().catch(()=> 'no session')+': '+error.message);}
    await left.screenshot({path:path.resolve('.smoke/unified-session.png')});
    await checkFooter(left);
    await rotate(right);
    assert.equal(await left.locator('#screen').getAttribute('data-ready'),'1');
    assert.equal(await left.locator('#disconnectForm').count(),1);
    left.on('dialog',dialog=>dialog.accept());
    const bounds=await left.locator('#screen').boundingBox();await left.mouse.move(bounds.x+20,bounds.y+20);await left.mouse.down();await left.mouse.move(bounds.x+100,bounds.y+100);await left.mouse.up();
    assert.equal(await left.locator('form[action^="/api/exit"]').count(),0);
    await left.locator('form[action^="/api/disconnect"] button').click();
    assert.equal(await left.locator('#disconnectDialog').isVisible(),true);
    await left.screenshot({path:path.resolve('.smoke/compact-confirm.png')});
    await left.locator('#cancelDisconnect').click();
    assert.equal(await left.locator('#disconnectDialog').isVisible(),false);
    await left.locator('form[action^="/api/disconnect"] button').click();
    await left.locator('#confirmDisconnect').click();
    await left.waitForFunction(()=>document.getElementById('localCode')?.textContent.match(/^\d{3} \d{3} \d{3}$/));
    assert.equal(await left.locator('#activationKey').isVisible(),false);
    await left.locator('nav [data-tab="devices"]').click();await left.waitForFunction(()=>document.querySelectorAll('.device-row').length===2);
    const deviceMatch=u=>u.pathname==='/api/devices';
    await left.route(deviceMatch,async route=>{const response=await route.fetch(),v=await response.json();v.devices=Array.from({length:20},(_,i)=>({deviceID:String(800000000+i),name:'分页测试 '+(i+1)}));await route.fulfill({json:v});});
    await left.locator('#refreshDevices').click();await left.waitForFunction(()=>document.querySelectorAll('.device-row').length===21);
    assert.ok(await left.locator('.device-row:visible').count()<=6);
    const firstPage=await left.locator('.device-row:visible').first().textContent();
    await left.locator('#deviceNext').click();assert.notEqual(await left.locator('.device-row:visible').first().textContent(),firstPage);
    await noPageScroll(left);
    await left.unroute(deviceMatch);
    await left.setViewportSize({width:720,height:640});assert.ok(await left.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
    await left.setViewportSize({width:390,height:780});await left.locator('nav [data-tab="remote"]').click();await noPageScroll(left);
    await left.screenshot({path:path.resolve('.smoke/unified-mobile.png')});
    await checkFooter(left);
    assert.deepEqual(errors,[]);
    console.log(JSON.stringify({dashboard:'compact, responsive, collapsed activation passed',identity:'9-digit unique alias and 6-digit PIN passed',devices:'add/search/status/reconnect passed',session:'pinned resolution, real desktop, return to unified dashboard passed',pause:'withdraw and restore receiving passed'}));
  }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
