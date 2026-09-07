(function(root){
  'use strict';
  // Small bounded PCM scheduler. Drop stale audio instead of letting delay grow
  // or scheduling new sound over sources already queued for the same time.
  class PCMPlayer {
    constructor(context){this.context=context;this.pending=new Uint8Array(0);this.sources=new Set();this.next=0;this.dropped=0;this.frames=0;this.peak=0;this.gain=context.createGain();this.gain.connect(context.destination);}
    volume(value){this.gain.gain.value=Math.max(0,Math.min(1,value));}
    clear(){for(const source of this.sources){try{source.stop();}catch{}source.disconnect();}this.sources.clear();this.next=0;}
    close(){this.clear();this.pending=new Uint8Array(0);this.gain.disconnect();}
    push(value){
      const context=this.context;
      const merged=new Uint8Array(this.pending.length+value.length);merged.set(this.pending);merged.set(value,this.pending.length);
      const length=merged.length-merged.length%4;this.pending=merged.slice(length);
      if(!length)return;
      const maxBytes=48000*4*.08; // At most 80 ms from a coalesced TCP read.
      const start=Math.max(0,length-maxBytes),payload=merged.subarray(start,length);
      if(start)this.dropped+=start/4;
      const count=payload.length/4,now=context.currentTime;
      if(context.state!=='running'){this.clear();this.dropped+=count;return;}
      if(this.next<now||this.next+count/48000>now+.14){this.clear();this.next=now+.025;}
      const buffer=context.createBuffer(2,count,48000),view=new DataView(payload.buffer,payload.byteOffset,payload.byteLength);
      let peak=0;
      for(let channel=0;channel<2;channel++){const out=buffer.getChannelData(channel);for(let i=0;i<count;i++){out[i]=view.getInt16(i*4+channel*2,true)/32768;peak=Math.max(peak,Math.abs(out[i]));}}
      this.peak=peak;this.frames+=count;
      const source=context.createBufferSource();source.buffer=buffer;source.connect(this.gain);this.sources.add(source);
      source.onended=()=>{this.sources.delete(source);source.disconnect();};source.start(this.next);this.next+=buffer.duration;
    }
  }
  class SessionAudio {
    constructor({api,onState,fetcher=(...args)=>fetch(...args),createContext=()=>new AudioContext({sampleRate:48000,latencyHint:'interactive'})}){this.api=api;this.onState=onState;this.fetcher=fetcher;this.createContext=createContext;this.current=null;this.level=1;}
    volume(value){this.level=value;if(this.current)this.current.player.volume(value);}
    stop(){const run=this.current;if(!run)return;this.current=null;run.abort.abort();clearTimeout(run.timer);run.player.close();void run.context.close().catch(()=>{});this.onState('off','声音已关闭');}
    start(){
      if(this.current)return;
      let context;try{context=this.createContext();}catch(e){this.onState('error',e.message);return;}
      const run={context,player:new PCMPlayer(context),abort:new AbortController(),lastSound:0};this.current=run;run.player.volume(this.level);
      this.onState('starting','正在开启声音…');
      const state=(name,text)=>{if(this.current===run)this.onState(name,text);};
      // Only one status request may be in flight, even on a stalled network.
      const status=async()=>{if(this.current!==run)return;try{const r=await this.fetcher(this.api('/api/audio/status'),{cache:'no-store',signal:AbortSignal.any([run.abort.signal,AbortSignal.timeout(4000)])});if(!r.ok)throw Error('无法读取声音状态');const v=await r.json();if(v.error)throw Error(v.error);if(this.current!==run)return;if(context.state!=='running')state('waiting','声音已暂停，点击关闭后重新开启');else if(!run.lastSound||Date.now()-run.lastSound>2000)state('silent','已开启 · 远端当前无声');}catch(e){if(this.current===run&&e.name!=='AbortError'){this.stop();this.onState('error',e.message);}}finally{if(this.current===run)run.timer=setTimeout(status,1000);}};
      const task=(async()=>{
        try{
          await context.resume();if(this.current!==run)return;
          run.timer=setTimeout(status,1000);
          const response=await this.fetcher(this.api('/api/audio'),{signal:run.abort.signal,cache:'no-store'});
          if(!response.ok)throw Error(await response.text());
          if(response.headers.get('X-YuDesk-Audio-Format')!=='s16le;rate=48000;channels=2')throw Error('不支持的声音格式，请更新两端');
          state('silent','已开启 · 等待远端声音');
          const reader=response.body.getReader();
          try{while(this.current===run){const {value,done}=await reader.read();if(done){if(run.abort.signal.aborted)return;const r=await this.fetcher(this.api('/api/audio/status'),{signal:AbortSignal.any([run.abort.signal,AbortSignal.timeout(3000)])});const v=await r.json();throw Error(v.error||'声音流已结束，请重新开启');}if(this.current!==run)return;run.player.push(value);if(run.player.peak>.001){run.lastSound=Date.now();state('playing','正在播放远端系统声音');}}}finally{await reader.cancel().catch(()=>{});reader.releaseLock?.();}
        }catch(e){if(this.current===run&&e.name!=='AbortError'){this.stop();this.onState('error',e.message);}}
      })();
      return task;
    }
  }
  const exported={PCMPlayer,SessionAudio};if(typeof module==='object'&&module.exports)module.exports=exported;else root.YuDeskAudio=exported;
})(globalThis);
