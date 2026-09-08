// Feature-tested WebKit compatibility. Keep timeout/cancellation semantics on
// older supported system WebViews; never silently turn bounded I/O unbounded.
(()=>{
  'use strict';
  if(typeof AbortSignal.timeout!=='function'){
    AbortSignal.timeout=milliseconds=>{
      if(!Number.isSafeInteger(milliseconds)||milliseconds<0||milliseconds>2147483647)throw new RangeError('Invalid timeout');
      const controller=new AbortController();
      setTimeout(()=>controller.abort(new DOMException('The operation timed out','TimeoutError')),milliseconds);
      return controller.signal;
    };
  }
  if(typeof AbortSignal.any!=='function'){
    AbortSignal.any=signals=>{
      const sources=[...new Set(signals)],controller=new AbortController(),listeners=[];
      for(const signal of sources)if(!signal||typeof signal.addEventListener!=='function'||typeof signal.aborted!=='boolean')throw new TypeError('Invalid abort signal');
      const cleanup=()=>{for(const [signal,listener] of listeners)signal.removeEventListener('abort',listener);listeners.length=0;};
      for(const signal of sources){
        if(signal.aborted){cleanup();controller.abort(signal.reason);break;}
        const listener=()=>{cleanup();controller.abort(signal.reason);};
        signal.addEventListener('abort',listener,{once:true});listeners.push([signal,listener]);
      }
      return controller.signal;
    };
  }
})();
