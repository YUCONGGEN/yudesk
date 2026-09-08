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
    const bar=node('div','yu-transition-controls');
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
  for(const region of document.querySelectorAll('[data-window-drag]')){
    region.addEventListener('pointerdown',event=>{
      if(event.button!==0||event.target.closest('button,a,input,label,select,form'))return;
      // Only Windows uses the custom caption; other OSes keep their native one.
      if(!/Win/.test(navigator.platform))return;
      event.preventDefault();
      command('/api/ui/drag').catch(()=>{});
    });
  }

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
