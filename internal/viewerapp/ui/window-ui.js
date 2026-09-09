// Local window actions never share the remote input queue or target computer.
(()=>{
  const token=document.body.dataset.token;
  const endpoint=path=>path+'?access_token='+encodeURIComponent(token);
  const style=document.createElement('link');style.rel='stylesheet';style.href=endpoint('/assets/window-ui.css');document.head.append(style);
  function node(tag,cls,text){const n=document.createElement(tag);if(cls)n.className=cls;if(text)n.textContent=text;return n;}
  function dialog(id,title){
    const d=node('dialog','yu-dialog');d.id=id;
    const mark=node('div','yu-dialog-mark','Yu'),h=node('h2','',title),p=node('p'),buttons=node('div','yu-dialog-actions');
    d.append(mark,h,p,buttons);document.body.append(d);return {d,h,p,buttons};
  }
  const confirmUI=dialog('appConfirm','请确认操作');
  const cancelConfirm=node('button','secondary','取消'),acceptConfirm=node('button','primary','继续');
  cancelConfirm.id='confirmCancel';acceptConfirm.id='confirmAccept';confirmUI.buttons.append(cancelConfirm,acceptConfirm);
  window.yudeskConfirm=message=>new Promise(resolve=>{
    if(confirmUI.d.open){resolve(false);return;}
    confirmUI.p.textContent=message;
    const finish=value=>{confirmUI.d.close();resolve(value);};
    cancelConfirm.onclick=()=>finish(false);acceptConfirm.onclick=()=>finish(true);
    confirmUI.d.oncancel=event=>{event.preventDefault();finish(false);};
    confirmUI.d.showModal();cancelConfirm.focus();
  });
  // Connecting/return pages also need controls when the system caption is gone.
  if(!document.querySelector('[data-window-action="minimize"]')){
    const bar=node('div','yu-transition-controls');bar.dataset.windowDrag='';bar.setAttribute('aria-label','窗口拖动区域');
    for(const [action,label] of [['hide','隐藏'],['minimize','−']]){const b=node('button','secondary',label);b.type='button';b.dataset.windowAction=action;b.title=action==='hide'?'隐藏':'最小化';bar.append(b);}
    const form=node('form');form.method='post';form.action=endpoint('/api/exit');const close=node('button','window-close','×');close.title='关闭应用';close.setAttribute('aria-label','关闭应用');form.append(close);bar.append(form);document.body.append(bar);
  }
  const showError=message=>{
    const notice=document.getElementById('notice')||document.getElementById('sessionNotice');
    if(notice){notice.textContent=message;notice.hidden=false;}
  };
  async function command(path){
    const r=await fetch(endpoint(path),{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'});
    if(!r.ok)throw Error((await r.text()).trim()||'窗口操作失败');
  }
  for(const button of document.querySelectorAll('[data-window-action]')){
    button.addEventListener('click',async()=>{
      if(button.disabled)return;
      button.disabled=true;
      try{await command(button.dataset.windowAction==='hide'?'/api/local/hide':'/api/ui/minimize');}
      catch(error){showError(error.message);}finally{button.disabled=false;}
    });
  }
  for(const form of document.querySelectorAll('form[action^="/exit?"],form[action^="/api/exit?"]')){
    form.addEventListener('submit',async event=>{
      event.preventDefault();
      const button=form.querySelector('button');
      if(button.disabled)return;
      button.disabled=true;
      try{await command('/api/exit');}catch(error){showError(error.message);button.disabled=false;}
    });
  }
  let dragPending=false;
  // Publish layout ahead of input. Windows owns transparent header hit areas,
  // so even a very short drag begins in its GUI thread, not after a fetch.
  if(/Win/.test(navigator.platform)){
    let inFlight=false,scheduled=false,stoppedRegions=false;
    const interactive='button,a,input,label,select,textarea,form,[contenteditable],[role="button"],[role="link"]';
    function dragLayout(){
      const rects=[];
      if(!document.querySelector('dialog[open]')&&!document.fullscreenElement){
        for(const bar of document.querySelectorAll('[data-window-drag]')){
          const b=bar.getBoundingClientRect();
          if(b.width<=0||b.height<=0||b.y<0||b.bottom>100)continue;
          let spans=[[Math.max(0,b.left),Math.min(innerWidth,b.right)]];
          for(const control of bar.querySelectorAll(interactive)){
            const c=control.getBoundingClientRect();
            if(c.width<=0||c.height<=0||c.bottom<=b.top||c.top>=b.bottom)continue;
            spans=spans.flatMap(([l,r])=>c.right<=l||c.left>=r?[[l,r]]:[[l,Math.max(l,c.left-2)],[Math.min(r,c.right+2),r]].filter(([a,z])=>z>a));
          }
          for(const [l,r] of spans){if(r-l>=4&&rects.length<16)rects.push({x:l,y:b.y,width:r-l,height:b.height});}
        }
      }
      return {width:innerWidth,height:innerHeight,scale:devicePixelRatio,rects};
    }
    async function publish(){
      scheduled=false;if(stoppedRegions||inFlight)return;
      inFlight=true;
      try{await fetch(endpoint('/api/ui/drag-regions'),{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(dragLayout()),signal:AbortSignal.timeout(1500)});}catch(_){}
      finally{inFlight=false;}
    }
    const schedule=()=>{if(!scheduled){scheduled=true;requestAnimationFrame(publish);}};
    new ResizeObserver(schedule).observe(document.documentElement);
    const changes=new MutationObserver(schedule);changes.observe(document.body,{subtree:true,childList:true,attributes:true,attributeFilter:['class','style','hidden','open']});
    window.addEventListener('resize',schedule);document.addEventListener('fullscreenchange',schedule);
    window.addEventListener('pagehide',()=>{stoppedRegions=true;changes.disconnect();fetch(endpoint('/api/ui/drag-regions'),{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({width:innerWidth,height:innerHeight,scale:devicePixelRatio,rects:[]}),keepalive:true}).catch(()=>{});});
    setInterval(publish,1000);schedule();
  }
  document.addEventListener('mousedown',event=>{
      if(!event.isTrusted||event.button!==0||!(event.target instanceof Element))return;
      if(!event.target.closest('[data-window-drag]')||event.target.closest('button,a,input,label,select,textarea,form,[contenteditable],[role="button"],[role="link"]'))return;
      // Windows drags its owned host through this API. macOS/Linux packaged
      // WebViews install their own trusted-pointer native drag bridge.
      if(!/Win/.test(navigator.platform)||dragPending)return;
      event.preventDefault();
      // Native dragging can interrupt a WebView pointer sequence. Listen for
      // each real mouse-down independently; the owner thread cancels capture.
      dragPending=true;
      fetch(endpoint('/api/ui/drag'),{method:'POST',headers:{'Content-Type':'application/json'},body:'{}',signal:AbortSignal.timeout(2000)})
        .then(async r=>{if(!r.ok)throw Error((await r.text()).trim()||'窗口拖动失败');})
        .catch(error=>showError(error.message)).finally(()=>{dragPending=false;});
  });

  const incoming=dialog('incomingApproval','远程连接请求');
  const countdown=node('p','yu-countdown'),safety=node('p','yu-security-note','请通过电话等方式核实对方身份。仅同意本次连接，不会保存免密许可。');
  incoming.d.insertBefore(countdown,incoming.buttons);incoming.d.insertBefore(safety,incoming.buttons);
  const deny=node('button','secondary','拒绝'),allow=node('button','primary','允许本次连接');
  deny.id='denyConnection';allow.id='allowConnection';incoming.buttons.append(deny,allow);
  let pending=null,resolving=false,stopped=false;
  async function resolve(accept){
    if(!pending||resolving)return;
    if(Date.parse(pending.deadline)<=Date.now()){incoming.d.close();pending=null;return;}
    resolving=true;allow.disabled=deny.disabled=true;
    try{const r=await fetch(endpoint('/api/local/approval'),{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({requestID:pending.id,accept}),signal:AbortSignal.timeout(5000)});if(!r.ok)throw Error((await r.text()).slice(0,200));incoming.d.close();pending=null;}
    catch(error){showError(error.message);}finally{resolving=false;allow.disabled=deny.disabled=false;}
  }
  deny.onclick=()=>resolve(false);allow.onclick=()=>resolve(true);
  incoming.d.oncancel=event=>{event.preventDefault();resolve(false);};
  async function pollApproval(){
    if(stopped)return;
    try{
      const r=await fetch(endpoint('/api/local/approval'),{cache:'no-store',signal:AbortSignal.timeout(4000)});
      if(r.status===404){stopped=true;return;}
      if(!r.ok)throw Error('status');
      const value=(await r.json()).pending;
      if(!resolving){
        if(value&&Date.parse(value.deadline)>Date.now()){
          pending=value;
          incoming.p.textContent=value.mode==='view'?'对方请求观看本机画面，不允许操作鼠标和键盘。':'对方请求观看画面并操作本机鼠标、键盘。';
          if(!incoming.d.open){incoming.d.showModal();deny.focus();}
        }else{pending=null;incoming.d.close();}
      }
    }catch(_){if(pending&&Date.parse(pending.deadline)<=Date.now()){incoming.d.close();pending=null;}}
    if(!stopped)setTimeout(pollApproval,1000);
  }
  setInterval(()=>{if(!pending)return;const seconds=Math.max(0,Math.ceil((Date.parse(pending.deadline)-Date.now())/1000));countdown.textContent=seconds+' 秒后自动拒绝';if(seconds===0){incoming.d.close();pending=null;}},250);
  window.addEventListener('pagehide',()=>{stopped=true;});
  pollApproval();
})();
