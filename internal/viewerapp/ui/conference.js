/* Conference screen-sharing lifecycle. Kept separate from the general dashboard. */
const conferenceToast=document.createElement('div');
conferenceToast.id='conferenceToast';
conferenceToast.className='conference-toast';
conferenceToast.hidden=true;
conferenceToast.setAttribute('role','status');
conferenceToast.setAttribute('aria-live','polite');
conferenceToast.innerHTML='<span class="conference-toast-icon" aria-hidden="true"></span><span class="conference-toast-message"></span>';
$('conferenceRoom').append(conferenceToast);

function showConferenceToast(message,tone='info',active=conference){
  if(active&&conference!==active)return;
  clearTimeout(conferenceToast.dismissTimer);
  conferenceToast.className='conference-toast conference-toast-'+tone;
  conferenceToast.querySelector('.conference-toast-message').textContent=String(message||'会议操作未完成，请重试');
  conferenceToast.setAttribute('role',tone==='error'?'alert':'status');
  conferenceToast.hidden=false;
  conferenceToast.dismissTimer=setTimeout(()=>{conferenceToast.hidden=true;},tone==='error'?7000:4500);
}

function conferenceShareFailure(error){
  const name=error?.name||'',message=String(error?.message||'');
  if(name==='NotAllowedError'||name==='AbortError'||/permission denied|not allowed|cancel/i.test(message))return '已取消屏幕共享，会议仍会正常进行';
  if(name==='NotFoundError')return '没有找到可共享的屏幕或窗口';
  if(name==='NotReadableError'||name==='TrackStartError')return '系统暂时无法读取该画面，请关闭占用屏幕录制的应用后重试';
  if(name==='InvalidStateError')return '共享请求必须由你点击“共享屏幕”发起，请重新点击';
  if(name==='TypeError'||/not support|unsupported/i.test(message))return '当前系统组件不支持屏幕共享，请更新 YuDesk 或系统浏览器组件';
  return message||'屏幕共享未能启动，请重试';
}

function conferenceCurrentState(active=conference){
  return active?.states?.get(active.selfID)||active?.states?.get('local')||null;
}

updateConferenceControls=function(){
  if(!conference)return;
  const state=conferenceCurrentState(),isHost=conference.selfID!==''&&conference.hostID===conference.selfID;
  $('conferenceMic').setAttribute('aria-pressed',String(!!state?.microphone));
  $('conferenceMic').querySelector('small').textContent=state?.microphone?'静音':'解除静音';
  conferenceSpeaker.setAttribute('aria-pressed',String(conference.speaker));
  conferenceSpeaker.querySelector('small').textContent=conference.speaker?'关闭声音':'播放声音';
  $('conferenceCamera').setAttribute('aria-pressed',String(!!state?.camera));
  $('conferenceCamera').querySelector('small').textContent=state?.camera?'关闭视频':'开启视频';
  const share=$('conferenceShare'),busy=!!conference.shareBusy||!!conference.fullscreenBusy;
  share.hidden=!isHost;
  share.disabled=isHost&&busy;
  share.setAttribute('aria-disabled',String(!isHost||busy));
  share.setAttribute('aria-busy',String(!!conference.shareBusy));
  share.setAttribute('aria-pressed',String(!!state?.screen));
  share.title=!isHost?'仅主持人可以共享屏幕':conference.shareBusy?'正在等待系统选择共享内容':state?.screen?'停止共享当前画面':'选择屏幕、窗口或标签页';
  share.querySelector('small').textContent=conference.shareBusy?'等待选择…':state?.screen?'停止共享':'共享屏幕';
  $('conferenceRecord').hidden=!isHost;
  $('conferenceLeave').querySelector('small').textContent=isHost?'结束会议':'离开会议';
};

function preferredConferenceSharer(active=conference){
  if(!active)return null;
  const preferred=active.states.get(active.activeSharerID||'');
  if(preferred?.screen)return preferred;
  const sharing=[...active.states.values()].filter(state=>state.screen);
  sharing.sort((left,right)=>(right.shareChangedAt||right.joinedAt||0)-(left.shareChangedAt||left.joinedAt||0));
  return sharing[0]||null;
}

conferenceSharer=function(){return preferredConferenceSharer();};

updateConferenceSharing=function(){
  const active=conference;
  if(!active)return;
  const sharer=preferredConferenceSharer(active);
  $('conferenceSharing').hidden=!sharer;
  $('conferenceSharing').textContent=sharer?(sharer.name+(sharer.id===active.selfID?'（我）':'')+' 正在共享'):'';
  clearTimeout(active.shareSwitchTimer);
  active.shareSwitchTimer=0;
  if(!active.followShare)return;
  if(sharer){
    if(active.focusedID!==sharer.id)focusConferenceShare(sharer.id,false);
    return;
  }
  if(active.focusedID)active.shareSwitchTimer=setTimeout(()=>{
    if(conference===active&&active.followShare&&!preferredConferenceSharer(active))clearConferenceFocus(true);
  },300);
};

async function conferenceNegotiateChanged(active,changed){
  const failures=[];
  for(const id of changed){
    if(conference!==active)break;
    try{await negotiateConferencePeer(id);}catch(error){failures.push(error);}
  }
  return failures;
}

async function conferenceReplaceVideo(active,track,stream){
  const changed=new Set(),failures=[];
  for(const [id,entry] of active.peers){
    try{
      if(entry.videoSender)await entry.videoSender.replaceTrack(track);
      else if(track){entry.videoSender=entry.pc.addTrack(track,stream);changed.add(id);}
      await tuneConferenceVideoSender(entry,true);
    }catch(error){failures.push(error);}
  }
  return {changed,failures};
}

stopConferenceShare=async function(active=conference,fromSystem=false){
  if(!active?.screen)return;
  const screen=active.screen;
  active.screen=null;
  const changed=new Set(),failures=[];
  for(const [id,entry] of active.peers){
    const senders=entry.screenAudioSenders||[];
    for(const sender of senders){
      try{entry.pc.removeTrack(sender);}catch(error){failures.push(error);}
    }
    if(senders.length)changed.add(id);
    entry.screenAudioSenders=[];
  }
  for(const track of screen.getTracks()){
    track.onended=null;
    try{track.stop();}catch(error){failures.push(error);}
  }
  const camera=active.stream?.getVideoTracks().find(track=>track.readyState==='live')||null;
  const replaced=await conferenceReplaceVideo(active,camera,active.stream);
  for(const id of replaced.changed)changed.add(id);
  failures.push(...replaced.failures,...await conferenceNegotiateChanged(active,changed));
  const state=conferenceCurrentState(active);
  if(state){state.screen=false;state.shareChangedAt=Date.now();}
  if(active.activeSharerID===active.selfID)active.activeSharerID='';
  if(conference===active){
    const tile=ensureConferenceTile(active.selfID,state?.name||active.name,true,active.stream);
    tile?.querySelector('video')?.classList.remove('screen');
    sendConferenceState();
    updateConferenceControls();
    updateConferenceSharing();
    if(fromSystem)showConferenceToast('系统已停止共享，会议仍在继续','info',active);
    else showConferenceToast(failures.length?'共享已停止，部分成员画面正在重新连接':'屏幕共享已停止',failures.length?'warning':'success',active);
  }
};

function conferenceFullscreenSnapshot(active){
  return {enabled:conferenceFullscreenActive(active),focusedID:active?.focusedID||'',followShare:!!active?.followShare};
}

async function restoreConferenceFullscreen(active,snapshot){
  if(!snapshot.enabled||conference!==active)return;
  try{
    if(!conferenceFullscreenActive(active))await setConferenceFullscreen(true,active);
    if(snapshot.focusedID&&active.states.get(snapshot.focusedID)?.screen){
      active.followShare=snapshot.followShare;
      focusConferenceShare(snapshot.focusedID,false);
    }
  }catch(error){
    showConferenceToast('共享选择已完成，但全屏未能自动恢复，可点击右上角“全屏”重试','warning',active);
  }
}

$('conferenceShare').onclick=()=>{
  const active=conference;
  void (async()=>{
    if(!active)return;
    if(active.hostID!==active.selfID){showConferenceToast('仅主持人可以共享屏幕','warning',active);return;}
    if(active.shareBusy){showConferenceToast('系统共享选择窗口已经打开，请先完成选择或取消','info',active);return;}
    if(active.fullscreenBusy){showConferenceToast('正在切换全屏，请稍后再试','info',active);return;}
    active.shareBusy=true;
    updateConferenceControls();
    const snapshot=conferenceFullscreenSnapshot(active);
    try{
      if(active.screen){await stopConferenceShare(active,false);return;}
      if(typeof navigator.mediaDevices?.getDisplayMedia!=='function')throw new DOMException('unsupported','TypeError');
      // Start both operations in this click turn. Awaiting the native fullscreen
      // endpoint first loses Chromium's transient user activation and causes
      // getDisplayMedia to reject even though the user clicked the real button.
      const leaveFullscreen=snapshot.enabled?Promise.resolve(setConferenceFullscreen(false,active)).catch(error=>{
        showConferenceToast('未能暂退全屏，系统选择窗口可能出现在当前窗口后方','warning',active);
      }):Promise.resolve();
      const capture=navigator.mediaDevices.getDisplayMedia({
        video:{frameRate:{ideal:30,max:30},width:{ideal:1920},height:{ideal:1080}},
        audio:true
      }).then(stream=>({stream}),error=>({error}));
      showConferenceToast('请在系统窗口中选择屏幕、窗口或标签页；取消不会退出会议','info',active);
      await leaveFullscreen;
      const result=await capture;
      if(result.error)throw result.error;
      const screen=result.stream;
      hintConferenceTracks(screen,true);
      if(conference!==active||active.hostID!==active.selfID){for(const value of screen.getTracks())value.stop();return;}
      const track=screen.getVideoTracks()[0];
      if(!track)throw new DOMException('没有取得可共享的画面','NotFoundError');
      active.screen=screen;
      track.onended=()=>{void stopConferenceShare(active,true).catch(error=>showConferenceToast('停止共享时遇到问题：'+conferenceShareFailure(error),'error',active));};
      const replaced=await conferenceReplaceVideo(active,track,screen),changed=replaced.changed,failures=[...replaced.failures];
      for(const [id,entry] of active.peers){
        for(const audio of screen.getAudioTracks()){
          try{
            const sender=entry.pc.addTrack(audio,screen);
            entry.screenAudioSenders.push(sender);
            await tuneConferenceAudioSender(sender,true);
            changed.add(id);
          }catch(error){failures.push(error);}
        }
      }
      failures.push(...await conferenceNegotiateChanged(active,changed));
      if(conference!==active){for(const value of screen.getTracks())value.stop();return;}
      const state=conferenceCurrentState(active);
      if(state){state.screen=true;state.shareChangedAt=Date.now();}
      active.activeSharerID=active.selfID;
      ensureConferenceTile(active.selfID,state?.name||active.name,true,screen);
      sendConferenceState();
      updateConferenceControls();
      updateConferenceSharing();
      const withAudio=screen.getAudioTracks().length>0;
      showConferenceToast(failures.length?'画面已共享，个别成员正在重新连接':withAudio?'屏幕和系统声音正在共享':'屏幕正在共享；共享系统声音需在选择窗口勾选音频',failures.length?'warning':'success',active);
    }catch(error){
      if(conference===active&&active.screen)await stopConferenceShare(active,false).catch(()=>{});
      if(conference===active)showConferenceToast(conferenceShareFailure(error),error?.name==='NotAllowedError'||error?.name==='AbortError'?'info':'error',active);
    }finally{
      if(conference===active){
        active.shareBusy=false;
        updateConferenceControls();
        await restoreConferenceFullscreen(active,snapshot);
        updateConferenceControls();
      }
    }
  })().catch(error=>{
    if(conference===active){active.shareBusy=false;updateConferenceControls();showConferenceToast('共享操作未完成：'+conferenceShareFailure(error),'error',active);}
  });
};

const conferenceShareBaseMessage=handleConferenceMessage;
handleConferenceMessage=async function(message,active=conference){
  if(active&&message?.type==='welcome'){
    const sharing=(message.peers||[]).filter(peer=>peer.screen).sort((left,right)=>(right.joinedAt||0)-(left.joinedAt||0));
    active.activeSharerID=sharing[0]?.id||'';
  }else if(active&&message?.type==='state'){
    const state=active.states.get(message.id);
    if(state&&state.screen!==!!message.screen)state.shareChangedAt=Date.now();
    if(message.screen)active.activeSharerID=message.id;
    else if(active.activeSharerID===message.id)active.activeSharerID='';
  }else if(active&&message?.type==='peer-left'&&active.activeSharerID===message.id){
    active.activeSharerID='';
  }
  await conferenceShareBaseMessage(message,active);
  if(conference===active&&(message?.type==='welcome'||message?.type==='state'||message?.type==='peer-left'||message?.type==='host')){
    const sharer=preferredConferenceSharer(active);
    active.activeSharerID=sharer?.id||'';
    updateConferenceSharing();
    updateConferenceControls();
  }
};

const conferenceShareBaseCleanup=cleanupConference;
cleanupConference=function(active){
  clearTimeout(active?.shareSwitchTimer);
  if(conferenceToast.dismissTimer)clearTimeout(conferenceToast.dismissTimer);
  conferenceToast.hidden=true;
  return conferenceShareBaseCleanup(active);
};
