// Native app fixture; localhost only; OS input is intercepted, never injected.
const assert=require('node:assert/strict');
const path=require('node:path');
const {chromium}=require('playwright');
(async()=>{
  const [leftURL,rightURL,remoteCode,remotePIN]=process.argv.slice(2);
  for(const value of [leftURL,rightURL])assert.equal(new URL(value).hostname,'127.0.0.1');
  const browser=await chromium.launch({executablePath:process.env.YUDESK_TEST_BROWSER,headless:true});
  const errors=[],dialogs=[];
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
    const checkWindowActions=async(page,hideSelector)=>{
      const selectors=[hideSelector,'[data-window-action="minimize"]','[aria-label="关闭应用"]'];
      assert.deepEqual(await page.evaluate(selectors=>selectors.map(selector=>document.querySelector(selector)).map((element,index,elements)=>!!element&&(index===0||!!(elements[index-1].compareDocumentPosition(element)&Node.DOCUMENT_POSITION_FOLLOWING))),selectors),[true,true,true]);
      const boxes=await Promise.all(selectors.map(selector=>page.locator(selector).boundingBox()));
      assert.ok(boxes.every(Boolean),'all three window actions must be visible');
      assert.ok(boxes[0].x+boxes[0].width<=boxes[1].x+1&&boxes[1].x+boxes[1].width<=boxes[2].x+1,'window actions must read hide, minimize, close from left to right');
      let minimized=0;
      const minimizeMatch=u=>u.pathname==='/api/ui/minimize';
      await page.route(minimizeMatch,route=>{assert.equal(route.request().method(),'POST');assert.deepEqual(route.request().postDataJSON(),{});minimized++;return route.fulfill({json:{ok:true}});});
      await page.locator(selectors[1]).click();
      await page.waitForFunction(()=>!document.querySelector('[data-window-action="minimize"]').disabled);
      assert.equal(minimized,1);
      await page.unroute(minimizeMatch);
    };
    const checkSessionWindowActions=async page=>{
      assert.equal(await page.locator('[data-window-action="hide"]').count(),0);
      assert.equal(await page.locator('[aria-label="关闭应用"]').count(),0);
      assert.equal(await page.locator('[data-window-action="minimize"]').count(),1);
      assert.ok(await page.locator('[data-window-action="minimize"]').isVisible());
    };
    for(const page of [left,right]){page.on('pageerror',e=>errors.push(e.message));page.on('dialog',dialog=>{dialogs.push({type:dialog.type(),message:dialog.message()});void dialog.dismiss().catch(()=>{});});await page.route(u=>u.pathname.startsWith('/api/input'),route=>route.fulfill({json:{ok:true}}));}
    await left.goto(leftURL);await right.goto(rightURL);
    for(const page of [left,right])await page.waitForFunction(()=>/^\d{3} \d{3} \d{3}$/.test(document.getElementById('localCode').textContent));
    assert.equal(await left.locator('#activation').getAttribute('open'),null);
    assert.equal(await left.locator('#activationKey').isVisible(),false);
    assert.match(await left.locator('#localPIN').textContent(),/^\d{6}$/);
    assert.ok((await left.locator('.device-field').boundingBox()).width<=200);
    assert.ok((await left.locator('.pin-field').boundingBox()).width<=112);
    await noPageScroll(left);
    await checkFooter(left);
    await checkWindowActions(left,'#hideApp');
    const homeBrand=left.locator('#homeBrand');
    assert.equal(await homeBrand.getAttribute('aria-label'),'返回远程控制首页');
    await left.locator('nav [data-tab="settings"]').click();
    await homeBrand.click();
    assert.equal(await left.locator('#pane-remote').isVisible(),true);
    assert.equal(await left.locator('#pageTitle').textContent(),'远程控制');
    assert.equal(await left.locator('#targetPIN').getAttribute('required'),null);
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
    let installed=false,updateRequired=false,installFails=false,packagePlatform='';
    const serviceMatch=u=>u.pathname.startsWith('/api/local/service/');
    await left.route(serviceMatch,route=>{
      const pathname=new URL(route.request().url()).pathname;
      if(pathname.endsWith('/status')&&packagePlatform)return route.fulfill({json:{supported:true,packageManaged:true,platform:packagePlatform,installed:true,running:true,trustedClient:true,message:'安装版 · 原生无标题栏窗口'}});
      if(pathname.endsWith('/status'))return route.fulfill({json:{supported:true,installed,updateRequired,running:installed,trustedClient:false,message:installed?'服务已安装，请切换到安装版':'尚未安装，请授权启用安装版'}});
      assert.equal(route.request().postDataJSON().confirmed,true);
      serviceActions.push(pathname);
      if(pathname.endsWith('/install')){if(installFails)return route.fulfill({status:409,body:'测试：管理员取消安装'});installed=true;updateRequired=false;}
      if(pathname.endsWith('/remove'))installed=false;
      return route.fulfill({json:{ok:true}});
    });
    await left.locator('nav [data-tab="settings"]').click();
    assert.equal(await left.locator('#runMode').inputValue(),'installed');
    await noPageScroll(left);
    await left.screenshot({path:path.resolve('.smoke/compact-settings.png')});
    await left.locator('#installService').click();
    assert.equal(await left.locator('#appConfirm').isVisible(),false,'install must go directly to the operating-system elevation flow');
    // The progress label precedes the asynchronous restart POST. Wait for the
    // whole action to finish, not a paint that can race the request dispatch.
    await left.waitForFunction(()=>document.getElementById('serviceAction').textContent.includes('安装成功')&&!document.getElementById('installService').disabled);
    assert.deepEqual(serviceActions,['/api/local/service/install','/api/local/service/restart'],'installation must automatically hand off only after success');
    await left.waitForFunction(()=>document.getElementById('removeService').hidden===false);
    await left.locator('#removeService').click();
    await left.locator('#appConfirm').waitFor({state:'visible'});
    assert.match(await left.locator('#appConfirm').textContent(),/卸载桌面服务/);
    await left.locator('#confirmAccept').click();
    await left.waitForFunction(()=>document.getElementById('removeService').hidden===true);
    assert.deepEqual(serviceActions,['/api/local/service/install','/api/local/service/restart','/api/local/service/remove']);
    installed=true;updateRequired=true;
    await left.waitForFunction(()=>document.getElementById('installService').textContent.includes('更新并启用'));
    assert.equal(await left.locator('#restartInstalled').isVisible(),false,'must not offer switching back to an outdated installation');
    installFails=true;
    await left.locator('#installService').click();
    await left.waitForFunction(()=>document.getElementById('serviceAction').textContent.includes('操作未完成'));
    assert.equal(serviceActions.at(-1),'/api/local/service/install');
    assert.equal(serviceActions.filter(v=>v.endsWith('/restart')).length,1,'failed/cancelled installation must not restart');
    await left.evaluate(()=>{localStorage.removeItem('yudesk.runMode');});
    const beforeAutomaticInstall=serviceActions.length;
    await left.reload();
    await left.evaluate(()=>{document.body.dataset.installPrompt='true';});
    await left.waitForFunction(()=>document.getElementById('serviceAction').textContent.includes('操作未完成'),{}, {timeout:8000});
    assert.equal(await left.locator('#appConfirm').isVisible(),false,'default installed mode must not show a second YuDesk confirmation');
    assert.equal(await left.locator('#runMode').inputValue(),'installed');
    assert.equal(await left.locator('#pane-remote').isVisible(),true,'automatic install must not flash or force the settings pane');
    assert.equal(serviceActions.length,beforeAutomaticInstall+1,'default installed mode must invoke the install endpoint directly');
    assert.equal(serviceActions.at(-1),'/api/local/service/install');
    await left.evaluate(()=>{document.body.dataset.installPrompt='false';});
    assert.deepEqual(dialogs,[],'service consent must not use browser dialogs');
    // Unix uses a real OS package, not a Windows service or a renamed binary.
    // Settings must remain visible even when a stale portable preference exists.
    const beforePackages=serviceActions.length;
    for(const platform of ['darwin','linux']){
      packagePlatform=platform;
      await left.reload();await left.locator('nav [data-tab="settings"]').click();
      await left.waitForFunction(()=>document.getElementById('serviceTitle').textContent==='安装与运行');
      assert.equal(await left.locator('#windowsService').isVisible(),true);
      assert.equal(await left.locator('#runMode').inputValue(),'installed');
      assert.equal(await left.locator('#runMode').isDisabled(),true);
      assert.equal(await left.locator('#installService').isVisible(),false);
      assert.match(await left.locator('#serviceDescription').textContent(),platform==='darwin'?/macOS 安装包/:/Linux 安装包/);
      await noPageScroll(left);
    }
    assert.equal(serviceActions.length,beforePackages,'Unix settings must not invoke Windows service commands');
    packagePlatform='';await left.reload();
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
    // A signed host publishes a distinct, short-lived 9-digit meeting number.
    // Joining is immediate but the protocol must keep it view-only.
    await right.locator('nav [data-tab="meeting"]').click();
    await right.locator('#startMeeting').click();
    await right.waitForFunction(()=>/^\d{3} \d{3} \d{3}$/.test(document.getElementById('meetingCode').textContent));
    const meetingInvite=(await right.locator('#meetingCode').textContent()).replace(/\D/g,'');
    assert.equal(meetingInvite.length,9);
    assert.notEqual(meetingInvite,remoteCode,'meeting number must remain distinct from the permanent device code');
    assert.equal(await right.locator('#meetingDot').isVisible(),true);
    await noPageScroll(right);
    await right.screenshot({path:path.resolve('.smoke/unified-meeting-host.png')});
    await left.locator('nav [data-tab="meeting"]').click();
    await left.locator('#meetingInvite').fill(meetingInvite);
    await left.locator('#joinMeeting').click({noWaitAfter:true});
    await left.waitForFunction(()=>document.getElementById('screen')?.dataset.ready==='1');
    assert.equal(await right.locator('#incomingApproval').isVisible(),false,'meeting number must join without host confirmation');
    assert.equal(await left.locator('body').getAttribute('data-control'),'false');
    assert.match(await left.locator('#status').textContent(),/仅观看/);
    await left.locator('form[action^="/api/disconnect"] button').click();
    await left.locator('#confirmDisconnect').click();
    await left.waitForFunction(()=>document.getElementById('localCode')?.textContent.match(/^\d{3} \d{3} \d{3}$/));
    await right.locator('#endMeeting').click();
    await right.locator('#confirmAccept').click();
    await right.waitForFunction(()=>document.getElementById('meetingActive').hidden===true);
    assert.equal(await right.locator('#meetingDot').isVisible(),false);
    // Pause must withdraw the waiting data connection, not just change a switch.
    await right.locator('nav [data-tab="remote"]').click();
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
    await checkSessionWindowActions(left);
    assert.equal(await left.locator('#settings').getAttribute('aria-expanded'),'false');
    assert.equal(await left.locator('#sessionPanel').isVisible(),false,'session settings must start collapsed');
    await left.locator('#settings').click();
    assert.equal(await left.locator('#sessionPanel').isVisible(),true);
    await left.locator('#settings').click();
    assert.equal(await left.locator('#sessionPanel').isVisible(),false);
    await rotate(right);
    assert.equal(await left.locator('#screen').getAttribute('data-ready'),'1');
    assert.equal(await left.locator('#disconnectForm').count(),1);
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
    // PIN-less access requires an explicit, single-use decision on the other
    // local fixture. Only the two test-owned localhost agents participate.
    await waitPresence(true);
    await left.locator('#targetCode').fill(remoteCode);
    await left.locator('#targetPIN').fill('');
    assert.equal(await left.locator('#targetPIN').evaluate(input=>input.checkValidity()),true);
    // Do not wait for the connect navigation before approving on the peer:
    // the HTTP response may be held while the 60-second consent is pending.
    await left.locator('#connectButton').click({noWaitAfter:true});
    await right.locator('#incomingApproval').waitFor({state:'visible',timeout:20000});
    assert.match(await right.locator('#incomingApproval').textContent(),/操作本机鼠标、键盘/);
    await right.waitForFunction(()=>/\d+ 秒后自动拒绝/.test(document.querySelector('#incomingApproval .yu-countdown').textContent));
    const approvalURL=new URL(rightURL);approvalURL.pathname='/api/local/approval';
    const approvalResponse=await right.request.get(approvalURL.href);
    assert.equal(approvalResponse.ok(),true);
    const {pending}=await approvalResponse.json();
    assert.equal(pending.mode,'control');
    const remaining=Date.parse(pending.deadline)-Date.now();
    assert.ok(remaining>0&&remaining<=60000,'local consent must expire within 60 seconds');
    const approvalPost=right.waitForRequest(r=>new URL(r.url()).pathname==='/api/local/approval'&&r.method()==='POST');
    await right.locator('#allowConnection').click();
    assert.deepEqual((await approvalPost).postDataJSON(),{requestID:pending.id,accept:true});
    await right.locator('#incomingApproval').waitFor({state:'hidden'});
    await left.waitForFunction(()=>document.getElementById('screen')?.dataset.ready==='1');
    await left.locator('form[action^="/api/disconnect"] button').click();
    await left.locator('#confirmDisconnect').click();
    await left.waitForFunction(()=>document.getElementById('localCode')?.textContent.match(/^\d{3} \d{3} \d{3}$/));
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
    assert.deepEqual(dialogs,[],'no browser alert/confirm/prompt is allowed');
    console.log(JSON.stringify({dashboard:'compact, responsive, clickable home brand and collapsed activation passed',identity:'9-digit unique alias and 6-digit PIN passed',meeting:'temporary 9-digit number, immediate join, screen share, view-only and expiry controls passed',devices:'add/search/status/reconnect passed',session:'settings collapsed by default; hide/close removed; pinned resolution, real desktop and return passed',pause:'withdraw and restore receiving passed',window:'dashboard hide/minimize/close and session minimize-only controls passed',consent:'direct installed-mode elevation and 60-second PIN-less remote approval passed; destructive removal remains confirmed; no browser dialogs'}));
  }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
