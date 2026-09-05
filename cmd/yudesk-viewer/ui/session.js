'use strict';
const $=id=>document.getElementById(id), screen=$('screen'), desktop=$('desktop');
const api=path=>path+'?access_token='+encodeURIComponent(document.body.dataset.token);
const canControl=document.body.dataset.control==='true', audioEnabled=document.body.dataset.audio==='true';
fetch(api('/api/ui/watch')).catch(()=>{});
const pressed=new Set(), keys=new Set();
function showError(error) { $('error').textContent=String(error.message||error); }
async function post(path,data) {
  const response=await fetch(api(path),{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(data),signal:AbortSignal.timeout(8000)});
  if(!response.ok) throw new Error(await response.text());
  return response;
}
const inputs=new YuDeskInput.InputQueue(data=>post('/api/input',data),()=>post('/api/input/release',{}),error=>{pressed.clear();keys.clear();showError(error);});
function position(e) { return YuDeskInput.point(e,screen.getBoundingClientRect(),screen.naturalWidth,screen.naturalHeight); }
function enqueue(event) { inputs.push(event,screen.naturalWidth,screen.naturalHeight); }
function button(e) { return e.button===0?1:e.button===1?2:3; }
function releaseAll() { pressed.clear();keys.clear();inputs.releaseAll(); }
if(canControl){
  screen.addEventListener('pointermove',e=>{const p=position(e);if(p)enqueue({type:'move',...p});});
  screen.addEventListener('pointerdown',e=>{
    if(e.button>2)return;
    const p=position(e);if(!p)return;
    e.preventDefault();screen.focus({preventScroll:true});screen.setPointerCapture(e.pointerId);
    pressed.add(button(e));enqueue({type:'move',...p});enqueue({type:'down',button:button(e),...p});
  });
  window.addEventListener('pointerup',e=>{
    if(!pressed.has(button(e)))return;
    const p=position(e);pressed.delete(button(e));
    if(p){enqueue({type:'move',...p});enqueue({type:'up',button:button(e),...p});}else releaseAll();
  });
  screen.addEventListener('pointercancel',releaseAll);
  screen.addEventListener('lostpointercapture',()=>{if(pressed.size)releaseAll();});
  screen.addEventListener('contextmenu',e=>e.preventDefault());
  screen.addEventListener('wheel',e=>{e.preventDefault();enqueue({type:'wheel',deltaX:Math.sign(e.deltaX)*120,deltaY:Math.sign(e.deltaY)*120});},{passive:false});
  screen.addEventListener('keydown',e=>{e.preventDefault();keys.add(e.key);enqueue({type:'key_down',key:e.key});});
  screen.addEventListener('keyup',e=>{e.preventDefault();keys.delete(e.key);enqueue({type:'key_up',key:e.key});});
  screen.addEventListener('blur',()=>{if(pressed.size||keys.size)releaseAll();});
  window.addEventListener('blur',releaseAll);
  document.addEventListener('visibilitychange',()=>{if(document.hidden)releaseAll();});
  window.addEventListener('pagehide',()=>{navigator.sendBeacon(api('/api/input/release'),new Blob(['{}'],{type:'application/json'}));});
}
async function fullscreen(){try{if(document.fullscreenElement)await document.exitFullscreen();else await desktop.requestFullscreen();screen.focus();}catch(e){showError(e);}}
$('fullscreen').onclick=fullscreen;$('exitFullscreen').onclick=fullscreen;
$('settings').onclick=()=>{if(innerWidth<=900){document.body.classList.remove('hide-panel');document.body.classList.toggle('show-panel');}else{document.body.classList.toggle('hide-panel');}};
for(const id of ['fps','quality'])$(id).oninput=()=>$(id+'Value').textContent=$(id).value;
function setOptions(o){for(const id of ['fps','quality','mode','maxWidth','maxMbps'])$(id).value=o[id];$('saveIdle').checked=o.saveIdle;$('fpsValue').textContent=o.fps;$('qualityValue').textContent=o.quality;}
function preset(o){setOptions({...o,saveIdle:true});$('error').textContent='已选择，点击“应用并记住设置”生效';}
$('fast').onclick=()=>preset({fps:30,quality:60,mode:'adaptive',maxWidth:960,maxMbps:12});
$('clear').onclick=()=>preset({fps:30,quality:82,mode:'fixed',maxWidth:0,maxMbps:0});
$('apply').onclick=async()=>{try{const o={fps:+$('fps').value,quality:+$('quality').value,mode:$('mode').value,maxWidth:+$('maxWidth').value,maxMbps:+$('maxMbps').value,saveIdle:$('saveIdle').checked};const response=await post('/api/stream/options',o);setOptions(await response.json());$('error').textContent='画面设置已保存';}catch(e){showError(e);}};
fetch(api('/api/stream/options')).then(r=>r.json()).then(setOptions).catch(showError);
const amount=n=>n<1048576?(n/1024).toFixed(1)+' KB':n<1073741824?(n/1048576).toFixed(1)+' MB':(n/1073741824).toFixed(2)+' GB';
async function stats(){
  try{
    const response=await fetch(api('/api/stats'),{cache:'no-store',signal:AbortSignal.timeout(4000)});if(!response.ok)throw Error('读取网络状态失败');
    const v=await response.json(),rtt=v.probeOK?v.rttMs.toFixed(0)+' ms':'测量中/超时';
    $('stats').textContent=v.fps.toFixed(1)+' FPS · RTT '+rtt+' · ↓ '+v.receiveMbps.toFixed(2)+' Mbps';
    $('networkDetail').textContent='往返延迟：'+rtt+'\n实测帧率：'+v.fps.toFixed(1)+' FPS / 目标 '+$('fps').value+'\n下行：'+v.receiveMbps.toFixed(2)+' Mbps　上行：'+v.sendMbps.toFixed(2)+' Mbps\n接收：'+amount(v.receivedBytes)+'　发送：'+amount(v.sentBytes)+'\n画面：'+v.Width+' × '+v.Height+'　当前画质：'+v.quality;
    if(v.error)showError(v.error);
  }catch(error){$('status').textContent='连接已断开或本机服务不可用';showError(error);return;}
  setTimeout(stats,1000);
}
stats();
$('getClip').onclick=async()=>{try{const r=await fetch(api('/api/clipboard'));if(!r.ok)throw Error(await r.text());$('clip').value=await r.text();}catch(e){showError(e);}};
$('setClip').onclick=()=>{if(canControl)post('/api/clipboard',{text:$('clip').value}).catch(showError);};
$('upload').onclick=async()=>{if(!canControl)return;try{const f=$('file').files[0];if(!f)return;if(f.size>20*1024*1024)throw Error('文件不能超过 20 MB');const data=await new Promise((ok,fail)=>{const r=new FileReader();r.onload=()=>ok(r.result.split(',')[1]);r.onerror=fail;r.readAsDataURL(f);});await post('/api/files',{path:$('path').value||f.name,data});}catch(e){showError(e);}};
if(!canControl){for(const id of ['clip','setClip','file','path','upload'])$(id).disabled=true;}
const audioButton=$('audio');let audioController=null,audioContext=null,nextAudioTime=0;
async function stopAudio(){if(audioController)audioController.abort();audioController=null;if(audioContext)await audioContext.close();audioContext=null;nextAudioTime=0;if(audioButton)audioButton.textContent='播放声音';}
async function startAudio(){
  if(!audioEnabled)return;
  audioController=new AbortController();audioContext=new AudioContext({sampleRate:48000});await audioContext.resume();nextAudioTime=audioContext.currentTime+.08;audioButton.textContent='停止声音';
  const response=await fetch(api('/api/audio'),{signal:audioController.signal});if(!response.ok)throw Error(await response.text());
  const reader=response.body.getReader();let pending=new Uint8Array(0);
  while(true){const {value,done}=await reader.read();if(done)break;const merged=new Uint8Array(pending.length+value.length);merged.set(pending);merged.set(value,pending.length);const usable=merged.length-merged.length%4,payload=merged.subarray(0,usable);pending=merged.slice(usable);if(!payload.length)continue;const frames=payload.length/4,buffer=audioContext.createBuffer(2,frames,48000),view=new DataView(payload.buffer,payload.byteOffset,payload.byteLength),left=buffer.getChannelData(0),right=buffer.getChannelData(1);for(let i=0;i<frames;i++){left[i]=view.getInt16(i*4,true)/32768;right[i]=view.getInt16(i*4+2,true)/32768;}if(nextAudioTime<audioContext.currentTime||nextAudioTime>audioContext.currentTime+.5)nextAudioTime=audioContext.currentTime+.08;const source=audioContext.createBufferSource();source.buffer=buffer;source.connect(audioContext.destination);source.start(nextAudioTime);nextAudioTime+=buffer.duration;}
}
if(audioButton){if(!audioEnabled&&audioButton.dataset.reason)audioButton.title=audioButton.dataset.reason;audioButton.onclick=async()=>{try{if(audioController)await stopAudio();else await startAudio();}catch(e){if(e.name!=='AbortError')showError(e);await stopAudio();}};}
fetch(api('/api/info')).then(r=>r.json()).then(v=>{$('info').textContent=v.meta&&v.meta.name||'';}).catch(showError);
