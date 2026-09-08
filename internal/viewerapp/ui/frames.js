(function(root){
  'use strict';
  class ByteReader {
    constructor(reader){this.reader=reader;this.chunk=new Uint8Array(0);this.offset=0;}
    async read(size){
      const result=new Uint8Array(size);let offset=0;
      while(offset<size){
        if(this.offset===this.chunk.length){const next=await this.reader.read();if(next.done)throw Error('画面传输已断开');this.chunk=next.value;this.offset=0;}
        const count=Math.min(size-offset,this.chunk.length-this.offset);
        result.set(this.chunk.subarray(this.offset,this.offset+count),offset);offset+=count;this.offset+=count;
      }
      return result;
    }
    async frame(){
      const sizes=new DataView((await this.read(8)).buffer),header=sizes.getUint32(0),size=sizes.getUint32(4);
      if(header<1||header>1048576||size>33554432)throw Error('画面数据长度无效');
      const meta=JSON.parse(new TextDecoder().decode(await this.read(header)));
      return {meta,data:await this.read(size)};
    }
    async finish(){
      if(this.offset!==this.chunk.length)throw Error('画面包含多余数据');
      const next=await this.reader.read();
      if(!next.done)throw Error('画面包含多余数据');
    }
  }
  function validate(meta,data){
    if(!Number.isInteger(meta.width)||!Number.isInteger(meta.height)||meta.width<1||meta.height<1||meta.width>16384||meta.height>16384||meta.width*meta.height>33177600||!Array.isArray(meta.tiles)||meta.tiles.length>1024)throw Error('画面尺寸无效');
    let total=0;
    for(const t of meta.tiles){
      if(![t.x,t.y,t.w,t.h,t.size].every(Number.isInteger)||t.x<0||t.y<0||t.w<1||t.h<1||t.x+t.w>meta.width||t.y+t.h>meta.height||t.size<1||!['image/png','image/jpeg'].includes(t.mime))throw Error('画面区域无效');
      total+=t.size;
    }
    if(total!==data.length)throw Error('画面数据不完整');
  }
  async function paint(canvas,meta,data){
    validate(meta,data);
    if(!meta.reset&&(canvas.width!==meta.width||canvas.height!==meta.height))throw Error('需要重新同步画面');
    const images=[];let offset=0;
    try{
      // Bounded decoding concurrency, then present the frame in one task so
      // users never see a partially reconstructed dirty-region update.
      for(let i=0;i<meta.tiles.length;i+=8){
        const jobs=meta.tiles.slice(i,i+8).map(t=>{const part=data.subarray(offset,offset+t.size);offset+=t.size;return createImageBitmap(new Blob([part],{type:t.mime})).then(bitmap=>{images.push({t,bitmap});if(bitmap.width!==t.w||bitmap.height!==t.h)throw Error('画面区域尺寸不匹配');});});
        const results=await Promise.allSettled(jobs);const failure=results.find(x=>x.status==='rejected');if(failure)throw failure.reason;
      }
      if(meta.reset){canvas.width=meta.width;canvas.height=meta.height;}
      // A low-latency presentation hint, not a network RTT measurement.
      // Unsupported browsers ignore it and retain the normal 2D path.
      const ctx=canvas.getContext('2d',{alpha:false,desynchronized:true});
      for(const {t,bitmap} of images)ctx.drawImage(bitmap,t.x,t.y);
      canvas.dataset.ready='1';
    }finally{for(const {bitmap} of images)bitmap.close();}
  }
  async function run(canvas,url,onError,onPaint){
    let revision=0;
    while(true){
      let reader,failed=false;
      try{
        const response=await fetch(url+'&after='+revision,{cache:'no-store'});if(!response.ok)throw Error('读取画面失败');
        reader=response.body.getReader();const stream=new ByteReader(reader);
        const {meta,data}=await stream.frame();
        if(!Number.isSafeInteger(meta.revision)||meta.revision<1)throw Error('画面版本无效');
        await stream.finish();
        await paint(canvas,meta,data);revision=meta.revision;if(onPaint)onPaint();
      }catch(error){failed=true;revision=0;onError(error);}finally{if(reader){if(failed)await reader.cancel().catch(()=>{});reader.releaseLock();}}
      if(failed)await new Promise(ok=>setTimeout(ok,1000));
    }
  }
  const api={ByteReader,validate,paint,run};root.YuDeskFrames=api;
  if(typeof module!=='undefined'&&module.exports)module.exports=api;
})(globalThis);
