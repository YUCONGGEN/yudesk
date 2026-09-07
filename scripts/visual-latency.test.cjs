// Only run with TestNativeVisualLatency's synthetic Agent. This transmits
// real input messages, but the helper changes pixels instead of OS input.
const assert=require('node:assert/strict');
const {chromium}=require('playwright');
(async()=>{
  const target=new URL(process.argv[2]);assert.equal(target.hostname,'127.0.0.1');
  const browser=await chromium.launch({executablePath:process.env.YUDESK_TEST_BROWSER,headless:true});
  try{
    const page=await browser.newPage({viewport:{width:1440,height:1000}});
    await page.goto(target.href,{waitUntil:'domcontentloaded'});
    await page.waitForFunction(()=>document.querySelector('#screen').dataset.ready==='1');
    await page.waitForFunction(()=>document.querySelector('#info').textContent==='visual-latency-fixture');
    const measurement=await page.evaluate(async()=>{
      performance.setResourceTimingBufferSize(3000);
      const canvas=document.querySelector('#screen'),ctx=canvas.getContext('2d'),values=[],slow=[];
      for(let i=0;i<60;i++){
        const x=10+i*4,box=canvas.getBoundingClientRect();
        const started=performance.now();
        // Pointer movement enters the normal session input queue. Synthetic
        // agent echoes original-screen X into the red channel of its marker.
        canvas.dispatchEvent(new PointerEvent('pointermove',{clientX:box.left+(x+.1)*box.width/canvas.width,clientY:box.top+box.height/2,bubbles:true}));
        const nativeX=Math.floor((x*1279+Math.floor((canvas.width-1)/2))/(canvas.width-1));
        const expected=nativeX%240+1;
        await new Promise((resolve,reject)=>{
          function sample(){const pixel=ctx.getImageData(8,8,1,1).data;
            if(pixel[0]===expected&&pixel[1]===77&&pixel[2]===123){values.push(performance.now()-started);resolve();return;}
            if(performance.now()-started>3000){reject(Error('matching remote pixels not displayed within 3s'));return;}
            requestAnimationFrame(sample);
          }sample();
        });
        if(values.at(-1)>80){
          const resources=performance.getEntriesByType('resource').filter(r=>r.responseEnd>=started&&r.startTime<=started+values.at(-1));
          const round=n=>+n.toFixed(1);
          slow.push({sample:i,elapsed:round(values.at(-1)),requests:resources.filter(r=>r.name.includes('/api/input')||r.name.includes('/api/frames')).slice(-5).map(r=>({type:r.name.includes('/api/input')?'input':'frame',start:round(r.startTime-started),first:round(r.responseStart-started),end:round(r.responseEnd-started)}))});
        }
        // Vary phase relative to the 60Hz browser clock instead of measuring
        // a single resonant cadence. An idle pause also exercises wake-up.
        await new Promise(resolve=>setTimeout(resolve,i===29?1300:12+(i*17)%50));
      }
      return {values,slow};
    });
    const samples=measurement.values;
    const sorted=samples.slice(5).sort((a,b)=>a-b),percentile=p=>+sorted[Math.ceil(sorted.length*p)-1].toFixed(1);
    const link=await page.evaluate(async()=>{const v=await(await fetch('/api/stats?access_token='+encodeURIComponent(document.body.dataset.token))).json();return {probeOK:v.probeOK,measuredRTTMs:+v.rttMs.toFixed(1)}});
    console.log(JSON.stringify({metric:'input-event-to-matching-canvas-pixels',syntheticDesktop:true,samples:sorted.length,p50Ms:percentile(.5),p95Ms:percentile(.95),maxMs:percentile(1),idleWakeMs:+samples[30].toFixed(1),...link}));
    if(process.env.YUDESK_VISUAL_TRACE==='1')console.log(JSON.stringify({slowSamples:measurement.slow}));
  }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1});
