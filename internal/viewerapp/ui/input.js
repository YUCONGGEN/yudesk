(function(root) {
  'use strict';
  function point(e, rect, width, height) {
    if (!width || !height || rect.width <= 0 || rect.height <= 0) return null;
    return {
      x: Math.max(0, Math.min(width - 1, Math.floor((e.clientX - rect.left) * width / rect.width))),
      y: Math.max(0, Math.min(height - 1, Math.floor((e.clientY - rect.top) * height / rect.height)))
    };
  }
  class InputQueue {
    constructor(send, release, error) { this.send=send; this.release=release; this.error=error; this.queue=[]; this.busy=false; }
    push(event,width,height) {
      const last=this.queue[this.queue.length-1];
      const item={event,width,height};
      if (event.type==='move' && last && last.event && last.event.type==='move' && last.width===width && last.height===height) this.queue[this.queue.length-1]=item;
      else this.queue.push(item);
      if (this.queue.length>256) { this.queue=[{release:true}]; this.error(new Error('输入队列拥堵，已请求释放按键')); }
      this.flush();
    }
    releaseAll() { if(!this.queue.at(-1)?.release)this.queue.push({release:true}); this.flush(); }
    async flush() {
      if (this.busy) return;
      this.busy=true;
      try {
        while (this.queue.length) {
          const first=this.queue.shift();
          if (first.release) { await this.release(); continue; }
          const events=[first.event];
          while (events.length<32 && this.queue.length && !this.queue[0].release && this.queue[0].width===first.width && this.queue[0].height===first.height) events.push(this.queue.shift().event);
          await this.send({events,width:first.width,height:first.height});
        }
      } catch(error) {
        this.queue=[];
        try { await this.release(); } catch(_) {}
        this.error(error);
      } finally { this.busy=false; if(this.queue.length)queueMicrotask(()=>this.flush()); }
    }
  }
  const api={point,InputQueue};
  if (typeof module!=='undefined' && module.exports) module.exports=api;
  root.YuDeskInput=api;
})(globalThis);
