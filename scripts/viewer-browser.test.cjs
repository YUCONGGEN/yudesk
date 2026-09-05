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
    await page.waitForFunction(()=>document.querySelector('#screen').naturalWidth>0);
    await page.waitForFunction(()=>document.querySelector('#stats').textContent.includes('RTT'));
    const box=await page.locator('#screen').boundingBox();assert.ok(box.width>100);
    await page.mouse.move(box.x+10,box.y+10);await page.mouse.down();
    await page.mouse.move(box.x+box.width-10,box.y+box.height-10,{steps:12});await page.mouse.up();
    await page.waitForFunction(()=>true);await page.waitForTimeout(250);
    const down=events.findIndex(e=>e.type==='down'),up=events.findIndex(e=>e.type==='up');
    assert.ok(down>=0&&up>down);assert.ok(events.slice(down+1,up).some(e=>e.type==='move'));
    await page.locator('#mode').selectOption('fixed');await page.locator('#maxWidth').selectOption('1280');await page.locator('#apply').click();
    await page.waitForFunction(()=>document.querySelector('#error').textContent==='画面设置已保存');
    await page.setViewportSize({width:700,height:700});await page.locator('#settings').click();
    assert.ok(await page.locator('#mode').isVisible());
    assert.deepEqual(errors,[]);
    console.log(JSON.stringify({browser:'passed',orderedDrag:events.length,stats:await page.locator('#stats').textContent()}));
  }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
