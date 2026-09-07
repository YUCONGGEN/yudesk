'use strict';
const $=id=>document.getElementById(id), screen=$('screen'), desktop=$('desktop'), keyboardCapture=$('keyboardCapture');
const api=path=>path+'?access_token='+encodeURIComponent(document.body.dataset.token);
const canControl=document.body.dataset.control==='true', audioEnabled=document.body.dataset.audio==='true';
const disconnectDialog=$('disconnectDialog');
$('disconnectForm').onsubmit=e=>{e.preventDefault();releaseAll();disconnectDialog.showModal();};
$('cancelDisconnect').onclick=()=>disconnectDialog.close();
$('confirmDisconnect').onclick=()=>{$('confirmDisconnect').disabled=true;$('confirmDisconnect').textContent='正在结束…';HTMLFormElement.prototype.submit.call($('disconnectForm'));};
for(const button of document.querySelectorAll('[data-panel]'))button.onclick=()=>{for(const pane of document.querySelectorAll('[data-panel-pane]'))pane.hidden=pane.dataset.panelPane!==button.dataset.panel;for(const tab of document.querySelectorAll('[data-panel]'))tab.classList.toggle('selected',tab===button);};
let desktopWaiting=false,toastTimer;
function notify(message,failed=false){const box=$('sessionNotice');clearTimeout(toastTimer);box.textContent=message;box.className=failed?'session-notice failure':'session-notice';box.hidden=false;toastTimer=setTimeout(()=>{box.hidden=true;},5000);}
fetch(api('/api/ui/watch')).catch(()=>{});
const pressed=new Set(), keys=new Map();
function showError(error) { $('error').textContent=String(error.message||error); }
YuDeskFrames.run(screen,api('/api/frames'),showError,()=>{if(!desktopWaiting)$('desktopNotice').hidden=true;});
async function post(path,data) {
  const response=await fetch(api(path),{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(data),signal:AbortSignal.timeout(8000)});
  if(!response.ok) throw new Error(await response.text());
  return response;
}
const inputs=new YuDeskInput.InputQueue(data=>post('/api/input',data),()=>post('/api/input/release',{}),error=>{pressed.clear();keys.clear();showError(error);});
function position(e) { return !desktopWaiting&&screen.dataset.ready==='1'?YuDeskInput.point(e,screen.getBoundingClientRect(),screen.width,screen.height):null; }
function enqueue(event) { if(!desktopWaiting)inputs.push(event,screen.width,screen.height); }
function button(e) { return e.button===0?1:e.button===1?2:3; }
function releaseAll() { pressed.clear();keys.clear();inputs.releaseAll(); }
if(canControl){
  screen.addEventListener('pointermove',e=>{const p=position(e);if(p)enqueue({type:'move',...p});});
  screen.addEventListener('pointerdown',e=>{
    if(e.button>2)return;
    const p=position(e);if(!p)return;
    e.preventDefault();keyboardCapture.focus({preventScroll:true});screen.setPointerCapture(e.pointerId);
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
  function keydown(e){
    if(desktopWaiting)return;
    if(e.isComposing||['Process','Unidentified','Dead'].includes(e.key))return;
    e.preventDefault();const id=e.code||e.key;
    if([...e.key].length===1&&e.key.charCodeAt(0)>127&&!e.ctrlKey&&!e.altKey&&!e.metaKey){enqueue({type:'text',text:e.key});return;}
    const event={type:'key_down',key:e.key,code:e.code||''};keys.set(id,event);enqueue(event);
  }
  function keyup(e){const id=e.code||e.key,event=keys.get(id);if(!event)return;e.preventDefault();keys.delete(id);enqueue({...event,type:'key_up'});}
  for(const target of [keyboardCapture,screen]){target.addEventListener('keydown',keydown);target.addEventListener('keyup',keyup);}
  keyboardCapture.addEventListener('compositionend',e=>{keyboardCapture.value='';if(e.data)enqueue({type:'text',text:e.data});});
  keyboardCapture.addEventListener('blur',()=>{keyboardCapture.value='';if(pressed.size||keys.size)releaseAll();});
  window.addEventListener('blur',releaseAll);
  document.addEventListener('visibilitychange',()=>{if(document.hidden)releaseAll();});
  window.addEventListener('pagehide',()=>{navigator.sendBeacon(api('/api/input/release'),new Blob(['{}'],{type:'application/json'}));});
}
async function fullscreen(){try{if(document.fullscreenElement)await document.exitFullscreen();else await desktop.requestFullscreen();(canControl?keyboardCapture:screen).focus({preventScroll:true});}catch(e){showError(e);}}
$('fullscreen').onclick=fullscreen;$('exitFullscreen').onclick=fullscreen;
function panelState(){const visible=innerWidth<=900?document.body.classList.contains('show-panel'):!document.body.classList.contains('hide-panel');$('settings').setAttribute('aria-expanded',String(visible));}
$('settings').onclick=()=>{if(innerWidth<=900){document.body.classList.remove('hide-panel');document.body.classList.toggle('show-panel');}else{document.body.classList.toggle('hide-panel');}panelState();};
window.addEventListener('resize',panelState);panelState();
for(const id of ['fps','quality'])$(id).oninput=()=>$(id+'Value').textContent=$(id).value;
function setOptions(o){for(const id of ['fps','quality','mode','maxWidth','maxMbps'])$(id).value=o[id];$('saveIdle').checked=o.saveIdle;$('fpsValue').textContent=o.fps;$('qualityValue').textContent=o.quality;}
function preset(o){setOptions({...o,saveIdle:true});$('applyStatus').textContent='已选择，点击“应用设置”生效';}
$('fast').onclick=()=>preset({fps:30,quality:70,mode:'adaptive',maxWidth:1280,maxMbps:8});
$('clear').onclick=()=>preset({fps:30,quality:82,mode:'fixed',maxWidth:0,maxMbps:0});
$('apply').onclick=async()=>{const button=$('apply');button.disabled=true;button.textContent='应用中…';$('applyStatus').textContent='正在应用设置…';try{const o={fps:+$('fps').value,quality:+$('quality').value,mode:$('mode').value,maxWidth:+$('maxWidth').value,maxMbps:+$('maxMbps').value,saveIdle:$('saveIdle').checked};const response=await post('/api/stream/options',o);setOptions(await response.json());$('applyStatus').textContent='设置已生效并保存';$('error').textContent='画面设置已保存';notify('画面设置已生效并保存');}catch(e){$('applyStatus').textContent='应用失败：'+e.message;notify('应用失败：'+e.message,true);}finally{button.disabled=false;button.textContent='应用设置';}};
fetch(api('/api/stream/options')).then(r=>r.json()).then(setOptions).catch(showError);
const amount=n=>n<1048576?(n/1024).toFixed(1)+' KB':n<1073741824?(n/1048576).toFixed(1)+' MB':(n/1073741824).toFixed(2)+' GB';
async function stats(){
  try{
    const response=await fetch(api('/api/stats'),{cache:'no-store',signal:AbortSignal.timeout(4000)});if(!response.ok)throw Error('读取网络状态失败');
    const v=await response.json(),rtt=v.probeOK?v.rttMs.toFixed(0)+' ms':'测量中/超时';
    const waiting=!!v.desktopWaiting;
    if(waiting&&!desktopWaiting)releaseAll();
    desktopWaiting=waiting;screen.style.visibility=waiting?'hidden':'';
    $('desktopNotice').hidden=!waiting&&screen.dataset.ready==='1';
    $('desktopMessage').textContent=waiting?(v.desktopMessage||'桌面暂不可用，连接已保留，正在等待恢复。'):'连接已建立，正在等待第一帧画面…';
    const cadence=v.probeOK&&v.fps===0&&$('saveIdle').checked?'静止省流':v.fps.toFixed(1)+' FPS';
    $('stats').textContent='RTT '+rtt;
    $('stats').dataset.quality=!v.probeOK?'unknown':v.rttMs<80?'good':v.rttMs<180?'fair':'poor';
    $('stats').title=cadence+' · ↓ '+v.receiveMbps.toFixed(2)+' Mbps · 点击查看网络详情';
    $('networkDetail').textContent='往返延迟：'+rtt+'\n实测帧率：'+v.fps.toFixed(1)+' FPS / 目标 '+$('fps').value+'\n下行：'+v.receiveMbps.toFixed(2)+' Mbps　上行：'+v.sendMbps.toFixed(2)+' Mbps\n接收：'+amount(v.receivedBytes)+'　发送：'+amount(v.sentBytes)+'\n画面：'+v.Width+' × '+v.Height+'　当前画质：'+v.quality;
    $('networkDetail').textContent+='\n编码：'+(v.codec==='tiles-v1'?'变化区域 PNG/JPEG':'整屏 JPEG')+'\n采集＋编码：'+(v.captureMs||0).toFixed(1)+' ms　更新区域：'+(v.changedTiles||0);
    $('networkDetail').textContent+='\nACK 延迟增长估计：'+(v.queueMs||0).toFixed(1)+' ms';
    $('inputError').textContent=v.inputError||'';
    $('status').textContent=waiting?'桌面受限 · 连接保持中':v.inputError?'输入受限 · 画面保持连接':(canControl?'已连接 · 控制模式':'已连接 · 仅观看');
    if(v.error)showError(v.error);
  }catch(error){$('status').textContent='连接已断开或本机服务不可用';showError(error);return;}
  setTimeout(stats,1000);
}
stats();
$('getClip').onclick=async()=>{const button=$('getClip');button.disabled=true;try{const r=await fetch(api('/api/clipboard'),{signal:AbortSignal.timeout(8000)});if(!r.ok)throw Error(await r.text());$('clip').value=await r.text();notify('已读取远端剪贴板');}catch(e){notify('读取失败：'+e.message,true);}finally{button.disabled=false;}};
$('setClip').onclick=async()=>{if(!canControl)return;const button=$('setClip');button.disabled=true;try{await post('/api/clipboard',{text:$('clip').value});notify('已发送到远端剪贴板');}catch(e){notify('发送失败：'+e.message,true);}finally{button.disabled=false;}};
YuDeskFiles.mount({api,canControl});
if(!canControl){for(const id of ['clip','setClip'])$(id).disabled=true;}
const audioButton=$('audio');
const sound=new YuDeskAudio.SessionAudio({api,onState:(state,message)=>{
  const active=!!sound.current;audioButton.dataset.state=state;audioButton.setAttribute('aria-pressed',String(active));audioButton.setAttribute('aria-label',active?'关闭声音':'开启声音');audioButton.title=message;$('audioStatus').textContent=message;
  if(state==='error')notify('声音不可用：'+message,true);
}});
audioButton.onclick=()=>{if(!audioEnabled)return;if(sound.current)sound.stop();else void sound.start();};
$('audioVolume').oninput=()=>{sound.volume(+$('audioVolume').value/100);$('audioVolumeValue').textContent=$('audioVolume').value+'%';};
window.addEventListener('pagehide',()=>sound.stop());
function closeConnectionInfo(){$('connectionPanel').hidden=true;$('connectionInfo').setAttribute('aria-expanded','false');}
$('connectionInfo').onclick=()=>{releaseAll();const hidden=!$('connectionPanel').hidden;$('connectionPanel').hidden=hidden;$('connectionInfo').setAttribute('aria-expanded',String(!hidden));};
$('closeConnectionInfo').onclick=closeConnectionInfo;
document.addEventListener('pointerdown',e=>{if(!$('connectionPanel').contains(e.target)&&!$('connectionInfo').contains(e.target))closeConnectionInfo();});
document.addEventListener('keydown',e=>{if(e.key==='Escape')closeConnectionInfo();});
$('stats').onclick=()=>{closeConnectionInfo();document.querySelector('[data-panel="network"]').click();if($('settings').getAttribute('aria-expanded')!=='true')$('settings').click();};
fetch(api('/api/info')).then(r=>r.json()).then(v=>{const info=v.meta||{};$('info').textContent=info.name||'';$('remoteName').textContent=info.name||'YuDesk';document.title=(info.name?info.name+' · ':'')+'YuDesk';$('fileAccess').textContent=info.files?'已授权接收目录':'未开放';}).catch(showError);
