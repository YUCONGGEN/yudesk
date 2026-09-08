(function(root){
  'use strict';
  function mount({api,canControl}) {
    const el=id=>document.getElementById(id), status=el('transferStatus'), progress=el('transferProgress');
    let directory='', requestedDirectory='', active=null, listing=null, disposed=false;
    let entries=[],filePage=0;
    const url=(p,params={})=>{const u=new URL(api(p),location.href);for(const [k,v] of Object.entries(params))u.searchParams.set(k,v);return u.href;};
    const message=text=>{status.textContent=text;};
    const failText=text=>{
      const known=[['disabled','请先在被控端开启文件传输授权'],['exists','已有同名文件，请更换文件名'],['expired','传输已超时，请重新开始'],['publication_failed','接收目录的文件系统不支持安全保存，请使用本机 NTFS/APFS/ext4 目录'],['hash_mismatch','文件校验失败，请重新传输'],['invalid_path','文件名或路径不安全，请更换名称']];
      for(const [key,value] of known)if(text.includes(key))return value;
      return text.slice(0,500);
    };
    async function request(p,body,signal,params){
      const response=await fetch(url(p,params),{method:body===undefined?'GET':'POST',headers:body===undefined?{}:{'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body),signal,cache:'no-store'});
      if(!response.ok)throw new Error(failText(await response.text()));
      return response.json();
    }
    function busy(value) {
      const locked=value||!!listing||!canControl;
      el('upload').disabled=locked;el('file').disabled=locked;el('path').disabled=locked;
      el('cancelTransfer').disabled=!value;el('refreshFiles').disabled=value||!canControl;el('fileUp').disabled=value||!canControl;
      for(const button of el('fileList').querySelectorAll('button'))button.disabled=value||!canControl;
      if(el('filesPrev')){el('filesPrev').disabled=value||!canControl||filePage===0;el('filesNext').disabled=value||!canControl||filePage>=Math.max(1,Math.ceil(entries.length/3))-1;}
    }
    const fullPath=name=>directory?directory+'/'+name:name;
    async function list(path=directory) {
      if(!canControl||active||disposed)return;
      listing?.abort();
      const task=new AbortController();listing=task;requestedDirectory=path;
      // Clear old clickable rows before changing directory. A late response must
      // never combine old entry names with a newer directory's path.
      entries=[];filePage=0;renderFiles();busy(false);
      el('fileDirectory').textContent='正在读取接收目录 /'+path;
      el('fileList').textContent='正在读取…';
      try {
        const data=await request('/api/files',undefined,AbortSignal.any([task.signal,AbortSignal.timeout(25000)]),{path});
        if(listing!==task||disposed)return;
        if(!Array.isArray(data.entries)||data.entries.length>1000)throw Error('文件列表格式无效，请重试');
        directory=path;
        el('fileDirectory').textContent='接收目录 /'+directory+(data.truncated?'（仅显示前 1000 项）':'');
        entries=data.entries;renderFiles();
      }catch(error){if(listing===task&&!disposed){requestedDirectory=directory;el('fileDirectory').textContent='接收目录 /'+directory;el('fileList').textContent=error.name==='TimeoutError'?'读取目录超时，请刷新重试':error.message;}}
      finally{if(listing===task){listing=null;busy(!!active);}}
    }
    function renderFiles(){
        const size=3,pages=Math.max(1,Math.ceil(entries.length/size));filePage=Math.min(filePage,pages-1);
        el('fileList').replaceChildren();
        for(const entry of entries.slice(filePage*size,(filePage+1)*size)) {
          const row=document.createElement('div'),name=document.createElement('span'),button=document.createElement('button');
          name.textContent=entry.name+(entry.directory?' /':' · '+(entry.size/1048576).toFixed(2)+' MiB');
          button.textContent=entry.directory?'打开文件夹':'下载';button.type='button';button.disabled=!!active||!canControl;
          button.onclick=()=>{if(active||listing||disposed||!canControl)return;if(entry.directory){list(fullPath(entry.name));}else download(fullPath(entry.name),entry.name,entry.size);};
          row.append(name,button);el('fileList').append(row);
        }
        if(!entries.length)el('fileList').textContent='接收目录暂无文件';
        if(el('filesPrev')){el('filesPrev').disabled=filePage===0||!!active;el('filesNext').disabled=filePage===pages-1||!!active;el('filesPageInfo').textContent=(filePage+1)+' / '+pages;}
    }
    if(el('filesPrev')){el('filesPrev').onclick=()=>{filePage=Math.max(0,filePage-1);renderFiles();};el('filesNext').onclick=()=>{filePage++;renderFiles();};}
    async function cancelRemote(id) {
      if(!id)return;
      try {await request('/api/files/cancel',{id},AbortSignal.timeout(5000));}catch(_){}
    }
    el('cancelTransfer').onclick=()=>{active?.abort();message('正在取消传输…');};
    el('upload').onclick=async()=>{
      if(active||listing||disposed||!canControl)return;
      const file=el('file').files[0];if(!file){message('请先选择文件');return;}
      if(file.size>1073741824){message('单个文件不能超过 1 GiB');return;}
      const name=el('path').value.trim()||file.name;
      const task=new AbortController();active=task;busy(true);progress.value=0;let id='',complete=false,committing=false;
      try {
        message('正在准备上传…');
        const begin=await request('/api/files/upload/begin',{path:fullPath(name),size:file.size},task.signal);
        id=begin.id;
        if(!id||begin.chunkSize!==32768)throw new Error('文件协议不兼容，请更新两端软件');
        for(let offset=0;offset<file.size;) {
          const data=await file.slice(offset,offset+32768).arrayBuffer();
          const response=await fetch(url('/api/files/upload/chunk',{id,offset}),{method:'POST',body:data,signal:task.signal,headers:{'Content-Type':'application/octet-stream'}});
          if(!response.ok)throw new Error(failText(await response.text()));
          const ack=await response.json();
          if(ack.offset!==offset+data.byteLength)throw new Error('文件确认位置异常，请重新传输');
          offset=ack.offset;progress.value=file.size?offset/file.size*100:100;
          message('正在上传 '+(progress.value).toFixed(1)+'% · '+(offset/1048576).toFixed(2)+' / '+(file.size/1048576).toFixed(2)+' MiB');
        }
        committing=true;el('cancelTransfer').disabled=true;message('数据已传完，正在校验并保存…');
        const result=await request('/api/files/upload/commit',{id},task.signal);
        complete=true;progress.value=100;message('上传完成，SHA-256 校验通过'+(result.cleanupWarning?'（临时文件清理需检查）':''));
      }catch(error){message(task.signal.aborted&&!committing?'已取消上传':committing?'保存结果未确认，请刷新接收目录检查后再操作：'+error.message:error.message);}
      finally {if(!complete)await cancelRemote(id);active=null;busy(false);await list();}
    };
    async function download(remotePath,name,size) {
      // Stream directly to disk when supported. Never allocate a whole-file Blob.
      // Other browsers use their native streaming downloader with native progress/cancel.
      if(!root.showSaveFilePicker) {
        const link=document.createElement('a');link.href=url('/api/download',{path:remotePath});link.download=name;document.body.append(link);link.click();link.remove();
        message(/YuDeskNative\//.test(root.navigator?.userAgent||'')?'请选择文件保存位置。传输中请保持本页面打开，完成或失败会在这里提示。':'下载已交给浏览器：在下载列表中查看进度或取消。传输中请保持本页面打开。');return;
      }
      const task=new AbortController();active=task;busy(true);progress.value=0;let writer,reader;
      try {
        const handle=await root.showSaveFilePicker({suggestedName:name});
        if(task.signal.aborted)throw new DOMException('cancelled','AbortError');
        writer=await handle.createWritable();
        message('正在下载…');
        const response=await fetch(url('/api/download',{path:remotePath}),{signal:task.signal,cache:'no-store'});
        if(!response.ok)throw new Error(failText(await response.text()));
        const length=response.headers.get('Content-Length'),expected=Number(length);
        if(length===null||!Number.isSafeInteger(expected)||expected<0||expected>1073741824)throw new Error('文件大小无效');
        reader=response.body.getReader();let received=0;
        while(true){const {value,done}=await reader.read();if(done)break;if(value.byteLength>expected-received)throw Error('下载大小超过声明值');await writer.write(value);received+=value.byteLength;progress.value=expected?received/expected*100:100;message('正在下载 '+progress.value.toFixed(1)+'% · '+(received/1048576).toFixed(2)+' MiB');}
        if(received!==expected)throw new Error('下载不完整');
        await writer.close();writer=null;progress.value=100;message('下载完成，SHA-256 校验通过');
      }catch(error){message(error.name==='AbortError'?'已取消下载':error.message);task.abort();if(writer)try{await writer.abort();}catch(_){} }
      finally {if(reader){await reader.cancel().catch(()=>{});reader.releaseLock();}active=null;busy(false);}
    }
    el('refreshFiles').onclick=()=>list(requestedDirectory);
    el('fileUp').onclick=()=>list(requestedDirectory.split('/').slice(0,-1).join('/'));
    root.addEventListener('pagehide',()=>{disposed=true;listing?.abort();listing=null;active?.abort();},{once:true});
    busy(false);
    if(!canControl)message('仅观看模式不可传输文件');else list();
  }
  root.YuDeskFiles={mount};
})(globalThis);
