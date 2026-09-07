const {test}=require('node:test');
const assert=require('node:assert/strict');
const {PCMPlayer,SessionAudio}=require('../internal/viewerapp/ui/audio.js');
function context(){return {state:'running',currentTime:0,destination:{},sources:[],createGain(){return {gain:{value:1},connect(){},disconnect(){}};},createBuffer(channels,length,rate){const data=Array.from({length:channels},()=>new Float32Array(length));return {duration:length/rate,getChannelData:i=>data[i]};},createBufferSource(){const source={connect(){},disconnect(){},stop(){this.stopped=true;},start(at){this.at=at;}};this.sources.push(source);return source;},async resume(){},async close(){this.state='closed';}};}
test('PCM preserves fragmented signed stereo samples and independent channels',()=>{
  const c=context(),p=new PCMPlayer(c),bytes=Buffer.alloc(8);bytes.writeInt16LE(-32768,0);bytes.writeInt16LE(32767,2);bytes.writeInt16LE(1234,4);bytes.writeInt16LE(-2345,6);
  for(const byte of bytes)p.push(Uint8Array.of(byte));
  assert.equal(p.frames,2);assert.equal(p.pending.length,0);
  assert.equal(c.sources[0].buffer.getChannelData(0)[0],-1);assert.equal(c.sources[0].buffer.getChannelData(1)[0],32767/32768);
  assert.equal(c.sources[1].buffer.getChannelData(0)[0],1234/32768);assert.equal(c.sources[1].buffer.getChannelData(1)[0],-2345/32768);
});
test('PCM bounds burst backlog and stops old scheduled sources before resync',()=>{
  const c=context(),p=new PCMPlayer(c);
  for(let i=0;i<30;i++){p.push(new Uint8Array(3840));assert.ok(p.next<=c.currentTime+.14);}
  assert.ok(c.sources.some(s=>s.stopped));
  p.push(new Uint8Array(192000));assert.ok(p.dropped>40000);assert.ok(p.next<=.14);
  p.close();assert.equal(p.sources.size,0);assert.ok(c.sources.every(s=>s.stopped));
});
test('PCM drops data while output is suspended, and clamps local volume',()=>{
  const c=context(),p=new PCMPlayer(c);p.volume(9);assert.equal(p.gain.gain.value,1);p.volume(-1);assert.equal(p.gain.gain.value,0);
  c.state='suspended';p.push(new Uint8Array(3840));assert.equal(c.sources.length,0);assert.equal(p.dropped,960);
});
test('late old audio request failure cannot stop a newly started player',async()=>{
  const waiting=[],states=[],contexts=[];
  const audio=new SessionAudio({api:p=>p,onState:(s)=>states.push(s),createContext:()=>{const c=context();contexts.push(c);return c;},fetcher:()=>new Promise((resolve,reject)=>waiting.push({resolve,reject}))});
  const first=audio.start();await Promise.resolve();audio.stop();const second=audio.start();await Promise.resolve();
  const current=audio.current;waiting[0].reject(Error('stale failure'));await first;
  assert.equal(audio.current,current);assert.notEqual(contexts[1].state,'closed');assert.equal(states.includes('error'),false);
  audio.stop();waiting[1].reject(Object.assign(Error('aborted'),{name:'AbortError'}));await second;
  assert.ok(contexts.every(c=>c.state==='closed'));
});
test('stream EOF resets playback and permits a clean restart',async()=>{
  const states=[];const audio=new SessionAudio({api:p=>p,onState:s=>states.push(s),createContext:context,fetcher:async path=>path.endsWith('/status')?{json:async()=>({error:'声卡已移除'})}:{ok:true,headers:{get:()=> 's16le;rate=48000;channels=2'},body:{getReader:()=>({read:async()=>({done:true}),cancel:async()=>{}})}}});
  await audio.start();assert.equal(audio.current,null);assert.equal(states.at(-1),'error');
});
test('slow audio status requests never overlap and stopping cancels their timer',async(t)=>{
  t.mock.timers.enable({apis:['setTimeout']});
  const waiting=[];let rejectStream;
  const audio=new SessionAudio({api:p=>p,onState:()=>{},createContext:context,fetcher:(path,options)=>path.endsWith('/status')?new Promise(resolve=>waiting.push({resolve,signal:options.signal})):new Promise((_,reject)=>{rejectStream=reject;})});
  const task=audio.start();await Promise.resolve();
  t.mock.timers.tick(1000);assert.equal(waiting.length,1);
  t.mock.timers.tick(10000);assert.equal(waiting.length,1);
  waiting[0].resolve({ok:true,json:async()=>({})});await Promise.resolve();await Promise.resolve();
  t.mock.timers.tick(1000);assert.equal(waiting.length,2);
  audio.stop();assert.equal(waiting[1].signal.aborted,true);
  waiting[1].resolve({ok:true,json:async()=>({})});rejectStream(Object.assign(Error('stopped'),{name:'AbortError'}));await task;await Promise.resolve();
  t.mock.timers.tick(10000);assert.equal(waiting.length,2);
});
