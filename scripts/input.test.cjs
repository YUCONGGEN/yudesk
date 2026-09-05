const {test}=require('node:test');
const assert=require('node:assert/strict');
const {point,InputQueue}=require('../cmd/yudesk-viewer/ui/input.js');
const tick=()=>new Promise(resolve=>setImmediate(resolve));
test('coordinates account for letterbox offsets, scaling and out-of-frame drag',()=>{
  const rect={left:120,top:90,width:960,height:540};
  assert.deepEqual(point({clientX:120,clientY:90},rect,1920,1080),{x:0,y:0});
  assert.deepEqual(point({clientX:600,clientY:360},rect,1920,1080),{x:960,y:540});
  assert.deepEqual(point({clientX:1500,clientY:900},rect,1920,1080),{x:1919,y:1079});
  assert.equal(point({},rect,0,0),null);
});
test('slow network preserves down / drag / up order and coalesces only moves',async()=>{
  const sent=[];let finish;
  const queue=new InputQueue(async batch=>{sent.push(...batch.events);if(sent.length===1)await new Promise(resolve=>finish=resolve);},async()=>sent.push({type:'release'}),error=>{throw error});
  const push=(type,x,button)=>queue.push({type,x,button},1280,720);
  push('move',0);push('down',0,1);for(let x=1;x<100;x++)push('move',x);push('up',99,1);push('move',100);
  finish();await tick();await tick();
  assert.deepEqual(sent.map(e=>e.type),['move','down','move','up','move']);
  assert.equal(sent[2].x,99);
  queue.releaseAll();await tick();assert.equal(sent.at(-1).type,'release');
});
test('failed input releases pressed buttons before future input',async()=>{
  const log=[];const queue=new InputQueue(async()=>{throw Error('network lost')},async()=>log.push('release'),()=>log.push('error'));
  queue.push({type:'down',button:1},1920,1080);await tick();await tick();
  assert.deepEqual(log,['release','error']);assert.equal(queue.queue.length,0);
});
