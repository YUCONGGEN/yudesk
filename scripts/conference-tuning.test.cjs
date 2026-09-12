const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const vm=require('node:vm');

const source=fs.readFileSync(path.join(__dirname,'../internal/viewerapp/ui/dashboard.js'),'utf8');
function declaration(name){
  const line=source.split(/\r?\n/).find(value=>value.startsWith((name==='tuneConferenceVideoSender'?'async ':'')+'function '+name+'('));
  if(!line)throw Error('missing '+name);
  return line;
}
function policy(room){
  const context={conference:room};
  vm.runInNewContext(declaration('conferenceVideoBudget')+'\nthis.budget=conferenceVideoBudget;',context);
  return context.budget;
}

test('browser meeting budget scales with mesh size, RTT and available uplink',()=>{
  const room={screen:null,peers:new Map([['a',{}]])},budget=policy(room);
  assert.equal(budget({rttMS:20}),1_500_000);
  room.peers=new Map([['a',{}],['b',{}],['c',{}],['d',{}]]);
  assert.equal(budget({rttMS:220}),700_000);
  room.peers=new Map([['a',{}]]);
  assert.equal(budget({rttMS:20,availableOutgoingBitrate:800_000}),656_000);
  room.screen={};
  assert.equal(budget({rttMS:350,availableOutgoingBitrate:200_000}),250_000);
});

test('browser meeting sender prioritizes cadence and avoids tiny oscillations',async()=>{
  const room={screen:null,peers:new Map()},context={conference:room};
  vm.runInNewContext(declaration('conferenceVideoBudget')+'\n'+declaration('tuneConferenceVideoSender')+'\nthis.tune=tuneConferenceVideoSender;',context);
  let applied=0,last;
  const sender={getParameters:()=>({encodings:[{}]}),setParameters:async value=>{applied++;last=value;}};
  const entry={videoSender:sender,videoCap:0,videoMode:false,videoTune:false,rttMS:20};room.peers.set('a',entry);
  await context.tune(entry);
  assert.equal(last.encodings[0].maxBitrate,1_500_000);
  assert.equal(last.encodings[0].maxFramerate,24);
  assert.equal(last.degradationPreference,'maintain-framerate');
  entry.rttMS=21;await context.tune(entry);assert.equal(applied,1);
  room.screen={};await context.tune(entry,true);
  assert.equal(last.encodings[0].maxBitrate,3_500_000);
  assert.equal(last.encodings[0].maxFramerate,30);
});
