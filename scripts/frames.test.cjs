const {test}=require('node:test');
const assert=require('node:assert/strict');
const {ByteReader,validate}=require('../internal/viewerapp/ui/frames.js');
test('binary frame parser handles every byte fragmented without base64',async()=>{
  const meta={width:256,height:256,reset:true,tiles:[{x:0,y:0,w:256,h:256,size:3,mime:'image/png'}]};
  const header=Buffer.from(JSON.stringify(meta)),sizes=Buffer.alloc(8);sizes.writeUInt32BE(header.length);sizes.writeUInt32BE(3,4);
  const wire=Buffer.concat([sizes,header,Buffer.from([1,2,3])]);let offset=0;
  const reader=new ByteReader({read:async()=>offset<wire.length?{value:wire.subarray(offset,++offset),done:false}:{done:true}});
  const result=await reader.frame();assert.deepEqual(result.meta,meta);assert.deepEqual([...result.data],[1,2,3]);validate(result.meta,result.data);await reader.finish();
});
test('frame completion rejects trailing bytes and consumes a clean EOF',async()=>{
  const reader=new ByteReader({read:async()=>({done:true})});
  await reader.finish();
  reader.chunk=new Uint8Array([1]);reader.offset=0;
  await assert.rejects(()=>reader.finish());
  const trailing=new ByteReader({read:async()=>({done:false,value:new Uint8Array([1])})});
  await assert.rejects(()=>trailing.finish());
});
test('invalid tile bounds and incomplete payload are rejected',()=>{
  assert.throws(()=>validate({width:1,height:1,tiles:[{x:0,y:0,w:2,h:1,size:1,mime:'image/png'}]},new Uint8Array(1)));
  assert.throws(()=>validate({width:1,height:1,tiles:[{x:0,y:0,w:1,h:1,size:2,mime:'image/png'}]},new Uint8Array(1)));
});
