// Opt-in Windows/Android public meeting smoke test. The UI token stays local.
const fs=require('node:fs');
const path=require('node:path');
const {chromium}=require('playwright');

(async()=>{
  const [stateDir,base='http://127.0.0.1:9450',name='Windows test']=process.argv.slice(2);
  if(!stateDir)throw Error('state directory is required');
  const token=fs.readFileSync(path.join(stateDir,'viewer-ui-token'),'utf8').trim();
  const target=new URL(base);
  if(target.hostname!=='127.0.0.1')throw Error('only a local YuDesk UI is allowed');
  target.searchParams.set('access_token',token);
  const browser=await chromium.launch({executablePath:process.env.YUDESK_TEST_BROWSER,headless:true,args:['--use-fake-device-for-media-stream','--use-fake-ui-for-media-stream','--autoplay-policy=no-user-gesture-required']});
  try{
    const context=await browser.newContext({permissions:['microphone','camera']});
    const page=await context.newPage();
    const errors=[];
    page.on('pageerror',error=>errors.push(String(error)));
    try{
      await page.goto(target.href,{waitUntil:'domcontentloaded'});
    }catch(_){
      throw Error('local YuDesk UI is unavailable');
    }
    await page.locator('[data-tab="meeting"]').click();
    await page.locator('#meetingHostName').fill(name);
    await page.locator('#meetingActive:not([hidden])').waitFor({timeout:15000});
    await page.locator('#enterMeeting').click();
    await page.locator('#conferenceRoom:not([hidden])').waitFor({timeout:20000});
    if(process.env.YUDESK_FORCE_TURN==='1')await page.evaluate(()=>{
      const Native=globalThis.RTCPeerConnection;
      globalThis.RTCPeerConnection=new Proxy(Native,{construct(Target,args){
        const source=args[0]||{};
        return Reflect.construct(Target,[{...source,iceServers:conference.rtc.iceServers,iceTransportPolicy:'relay'},...args.slice(1)]);
      }});
    });
    const code=(await page.locator('#conferenceCode').textContent()).replace(/\D/g,'');
    process.stdout.write(JSON.stringify({ready:true,code})+'\n');
    const deadline=Date.now()+120000;
    let last={};
    while(Date.now()<deadline){
      await page.waitForTimeout(1500);
      last=await page.evaluate(async()=>{
        const audio=[...document.querySelectorAll('.conference-remote-audio')];
        let inboundPackets=0,outboundPackets=0,inboundBytes=0,outboundBytes=0;
        for(const entry of conference?.peers?.values?.()||[]){
          const report=await entry.pc.getStats();
          for(const value of report.values()){
            if(value.type==='inbound-rtp'&&value.kind==='audio'){inboundPackets+=value.packetsReceived||0;inboundBytes+=value.bytesReceived||0;}
            if(value.type==='outbound-rtp'&&value.kind==='audio'){outboundPackets+=value.packetsSent||0;outboundBytes+=value.bytesSent||0;}
          }
        }
        return {members:conference?.states?.size||0,connection:document.querySelector('#conferenceConnection')?.textContent||'',audioTracks:audio.length,audioPlaying:audio.filter(item=>!item.paused&&!item.muted).length,inboundPackets,outboundPackets,inboundBytes,outboundBytes};
      });
      process.stdout.write(JSON.stringify(last)+'\n');
      if(last.members>=2&&/P2P|中转连接/.test(last.connection)&&last.outboundPackets>0&&last.audioTracks>0){
        if(errors.length)throw Error('browser errors: '+errors.join('; '));
        process.stdout.write(JSON.stringify({passed:true,...last})+'\n');
        return;
      }
    }
    throw Error('cross-platform media did not become ready: '+JSON.stringify(last));
  }finally{
    await browser.close();
  }
})().catch(error=>{process.stderr.write((error&&error.stack)||String(error));process.exitCode=1;});
