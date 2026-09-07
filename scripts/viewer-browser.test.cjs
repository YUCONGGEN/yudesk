// Opt-in browser regression against a local test Viewer, never a public device.
const assert=require('node:assert/strict');
const {chromium}=require('playwright');
(async()=>{
  const target=new URL(process.argv[2]);
  assert.equal(target.hostname,'127.0.0.1');
  const browser=await chromium.launch({executablePath:process.env.YUDESK_TEST_BROWSER,headless:true});
  try {
    const page=await browser.newPage({viewport:{width:1400,height:900}});
    const errors=[],events=[];
    page.on('pageerror',error=>errors.push(String(error)));
    // Exercise browser input capture and ordering without clicking the user's real desktop.
    await page.route(url=>url.pathname.startsWith('/api/input'),route=>{const body=route.request().postDataJSON();if(body.events)events.push(...body.events);return route.fulfill({json:{ok:true}});});
    await page.goto(target.href,{waitUntil:'domcontentloaded'});
    await page.waitForFunction(()=>document.querySelector('#screen').dataset.ready==='1');
    await page.waitForFunction(()=>document.querySelector('#stats').textContent.includes('RTT'));
    if(process.env.YUDESK_AUDIO_TEST_EXE){
      const {spawn}=require('node:child_process');
      // Inspect decoded PCM before it reaches the browser output. Never send
      // captured bytes out of this local fixture; only report sample counts.
      await page.evaluate(()=>{window.audioSamples=0;window.audioPeak=0;const original=AudioContext.prototype.createBufferSource;AudioContext.prototype.createBufferSource=function(){const source=original.call(this),start=source.start.bind(source);source.start=(...args)=>{if(source.buffer){const samples=source.buffer.getChannelData(0);window.audioSamples+=samples.length;for(const value of samples)window.audioPeak=Math.max(window.audioPeak,Math.abs(value));}return start(...args);};return source;};});
      await page.locator('#audio').click();
      try{await page.waitForFunction(()=>document.getElementById('audio').getAttribute('aria-pressed')==='true',{}, {timeout:5000});}catch(e){throw Error('audio start failed: '+JSON.stringify(await page.evaluate(()=>({state:document.getElementById('audio').dataset.state,reason:document.getElementById('audio').title,message:document.getElementById('audioStatus').textContent})))+' JS errors: '+errors.join('; '));}
      await page.waitForTimeout(300);
      const tone=spawn(process.env.YUDESK_AUDIO_TEST_EXE,['-test.run=^TestWindowsPlayTestTone$','-test.timeout=8s'],{windowsHide:true,env:{...process.env,YUDESK_TEST_AUDIO_TONE:'1'},stdio:'ignore'});
      const exited=new Promise((resolve,reject)=>{tone.on('error',reject);tone.on('exit',code=>code===0?resolve():reject(Error('test tone failed: '+code)));});
      await page.waitForFunction(()=>window.audioSamples>4800&&window.audioPeak>.005,{},{timeout:10000});await exited;
      await page.locator('#connectionInfo').click();assert.equal(await page.locator('#connectionPanel').isVisible(),true);
      await page.locator('#audioVolume').fill('35');assert.equal(await page.locator('#audioVolumeValue').textContent(),'35%');
      await page.screenshot({path:require('node:path').resolve('.smoke/session-sound.png')});
      await page.locator('#closeConnectionInfo').click();
      await page.locator('#audio').click();await page.waitForFunction(()=>document.getElementById('audio').getAttribute('aria-pressed')==='false');
      // Rapid stop/start must preserve the newest request; final silence must
      // not affect the actual remote desktop connection.
      for(let i=0;i<4;i++){await page.locator('#audio').click();await page.waitForTimeout(80);await page.locator('#audio').click();}
      await page.locator('#audio').click();await page.waitForTimeout(1200);
      assert.equal(await page.locator('#audio').getAttribute('aria-pressed'),'true');
      await page.locator('#audio').click();
      console.log('native WASAPI -> encrypted agent/viewer -> browser PCM: PASS; on/off/restart/volume passed');
    }
    const assertArrow=async()=>assert.deepEqual(await page.evaluate(()=>['#screen','#desktop'].map(selector=>getComputedStyle(document.querySelector(selector)).cursor)),['default','default']);
    await assertArrow();
    const box=await page.locator('#screen').boundingBox();assert.ok(box.width>100);
    await page.mouse.move(box.x+10,box.y+10);await page.mouse.down();
    await assertArrow();
    await page.mouse.move(box.x+box.width-10,box.y+box.height-10,{steps:12});await page.mouse.up();
    await page.waitForFunction(()=>true);await page.waitForTimeout(250);
    const down=events.findIndex(e=>e.type==='down'),up=events.findIndex(e=>e.type==='up');
    assert.ok(down>=0&&up>down);assert.ok(events.slice(down+1,up).some(e=>e.type==='move'));
    assert.equal(await page.locator('form[action^="/api/exit"]').count(),0);
    assert.equal(await page.locator('form[action^="/api/disconnect"]').count(),1);
    await page.keyboard.press('Period');await page.keyboard.press('Shift+Digit1');await page.keyboard.press('CapsLock');
    await page.evaluate(()=>{
      const input=document.getElementById('keyboardCapture');
      input.dispatchEvent(new CompositionEvent('compositionstart',{bubbles:true}));
      input.dispatchEvent(new KeyboardEvent('keydown',{key:'Process',code:'KeyA',isComposing:true,bubbles:true}));
      input.dispatchEvent(new CompositionEvent('compositionend',{data:'中文🖱',bubbles:true}));
    });
    await page.waitForTimeout(150);
    assert.ok(events.some(e=>e.type==='key_down'&&e.code==='Period'));
    assert.ok(events.some(e=>e.type==='key_up'&&e.code==='Digit1'));
    assert.ok(events.some(e=>e.type==='key_down'&&e.code==='CapsLock'));
    assert.equal(events.filter(e=>e.type==='text'&&e.text==='中文🖱').length,1);
    assert.equal(events.some(e=>e.key==='Process'),false);
    await assertArrow();
    // Check view-only styling without sending any commands to the real desktop.
    assert.equal(await page.evaluate(()=>{const previous=document.body.dataset.control;try{document.body.dataset.control='false';return getComputedStyle(document.querySelector('#screen')).cursor;}finally{document.body.dataset.control=previous;}}),'default');
    await page.locator('#fullscreen').click();
    await page.waitForFunction(()=>document.fullscreenElement?.id==='desktop');
    await assertArrow();
    await page.locator('#exitFullscreen').click();
    await page.waitForFunction(()=>!document.fullscreenElement);
    await page.locator('#mode').selectOption('fixed');await page.locator('#maxWidth').selectOption('1280');await page.locator('#apply').click();
    await page.waitForFunction(()=>document.querySelector('#error').textContent==='画面设置已保存');
    assert.equal(await page.locator('#applyStatus').textContent(),'设置已生效并保存');
    assert.equal(await page.locator('#sessionNotice').isVisible(),true);
    assert.match(await page.locator('#sessionNotice').textContent(),/已生效并保存/);
    // Browser feedback and Unicode transport are mocked at HTTP boundary: never
    // read or replace the user's real clipboard during regression tests.
    const clipboard='中文简体／繁體 🖱️ 😀\n第二行\n  保留空格  ';
    await page.locator('[data-panel="clipboard"]').click();
    let clipSent='';
    await page.route(u=>u.pathname==='/api/clipboard',route=>{
      if(route.request().method()==='POST'){clipSent=route.request().postDataJSON().text;return route.fulfill({json:{ok:true}});}
      return route.fulfill({contentType:'text/plain; charset=utf-8',body:clipboard});
    });
    await page.locator('#getClip').click();
    await page.waitForFunction(text=>document.querySelector('#clip').value===text,clipboard);
    await page.locator('#setClip').click();
    await page.waitForFunction(()=>document.querySelector('#sessionNotice').textContent==='已发送到远端剪贴板');
    assert.equal(clipSent,clipboard);
    const optionsMatch=u=>u.pathname==='/api/stream/options';
    await page.locator('[data-panel="display"]').click();
    await page.route(optionsMatch,async route=>{
      if(route.request().method()!=='POST')return route.continue();
      await new Promise(resolve=>setTimeout(resolve,250));
      return route.fulfill({status:503,body:'模拟设置失败'});
    });
    await page.locator('#apply').click();
    assert.equal(await page.locator('#apply').isDisabled(),true);
    await page.waitForFunction(()=>document.querySelector('#applyStatus').textContent.includes('模拟设置失败'));
    assert.equal(await page.locator('#apply').isDisabled(),false);
    assert.match(await page.locator('#sessionNotice').getAttribute('class'),/failure/);
    await page.unroute(optionsMatch);
    // Protected-desktop UI must retain the session, hide stale pixels and block
    // wheel/IME input, then recover without navigation or reconnecting.
    let simulateLocked=true;
    const statsMatch=u=>u.pathname==='/api/stats';
    await page.route(statsMatch,async route=>{
      const response=await route.fetch(),value=await response.json();
      value.desktopWaiting=simulateLocked;value.desktopMessage='测试：桌面暂不可用';
      await route.fulfill({json:value});
    });
    await page.waitForFunction(()=>document.querySelector('#desktopNotice').hidden===false&&document.querySelector('#status').textContent.includes('连接保持中'));
    assert.equal(await page.locator('#screen').isVisible(),false);
    await page.waitForTimeout(200);
    const before=events.length;
    await page.evaluate(()=>{
      document.getElementById('screen').dispatchEvent(new WheelEvent('wheel',{deltaY:100}));
      document.getElementById('keyboardCapture').dispatchEvent(new CompositionEvent('compositionend',{data:'不应发送',bubbles:true}));
    });
    await page.waitForTimeout(150);assert.equal(events.length,before);
    simulateLocked=false;
    await page.waitForFunction(()=>document.querySelector('#desktopNotice').hidden===true);
    assert.equal(await page.locator('#screen').isVisible(),true);
    await page.unroute(statsMatch);
    const payload=Buffer.alloc(98321,99);
    await page.locator('[data-panel="files"]').click();
    await page.locator('#file').setInputFiles({name:'browser-transfer.bin',mimeType:'application/octet-stream',buffer:payload});
    await page.locator('#upload').click();
    await page.mouse.move(box.x+20,box.y+20);await page.mouse.down();await page.mouse.move(box.x+100,box.y+100,{steps:5});await page.mouse.up();
    await page.waitForFunction(()=>document.querySelector('#transferStatus').textContent.includes('上传完成'));
    // Replace only the OS save picker; still test the real HTTP download stream.
    await page.evaluate(()=>{window.savedChunks=[];window.downloadSaved=false;window.showSaveFilePicker=async()=>({createWritable:async()=>({write:async value=>window.savedChunks.push(Array.from(value)),close:async()=>{window.downloadSaved=true},abort:async()=>{window.savedChunks=[]}})});});
    await page.locator('#fileList button').filter({hasText:'下载'}).first().click();
    await page.waitForFunction(()=>window.downloadSaved);
    const received=Buffer.from(await page.evaluate(()=>window.savedChunks.flat()));
    assert.deepEqual(received,payload);
    await page.setViewportSize({width:700,height:700});await page.locator('#settings').click();await page.locator('[data-panel="display"]').click();
    assert.ok(await page.locator('#mode').isVisible());
    assert.deepEqual(errors,[]);
    console.log(JSON.stringify({browser:'passed',settings:'pending/success/failure visible',clipboard:'Unicode HTTP roundtrip passed (mocked OS clipboard)',recovery:'waiting/input suspension/resume passed',keyboard:'punctuation/modifiers/IME passed',files:'binary upload/download integrity passed',arrowCursor:'normal/drag/view-only/fullscreen passed',orderedDrag:events.length,stats:await page.locator('#stats').textContent()}));
  }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
