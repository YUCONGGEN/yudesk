"""Synthetic WebKit/IPC regression; never target the user's actual desktop."""
import http.server
import json
import os
from pathlib import Path
import queue
import subprocess
import sys
import threading
import time


TEST_PAGE = '''<!doctype html><meta charset="utf-8"><title>YuDesk native test</title>
<style>body{margin:0;background:#f4f7fc;font:16px sans-serif;color:#172c49}header{padding:20px;background:white;display:flex;gap:16px;align-items:center}b{color:#1673fb;font-size:25px}main{margin:24px;background:white;border-radius:14px;padding:24px}button{background:#1673fb;color:white;border:0;border-radius:8px;padding:10px 18px}canvas{border:1px solid #dce5f4}dialog{border:0;border-radius:14px;padding:24px}dialog::backdrop{background:#001b4433}</style>
<header data-window-drag><b>Yu</b> YuDesk <button id="button">Synthetic fixture</button></header>
<main><h2>Native frameless window</h2><p>Canvas / image decode / fetch / audio API / styled dialog</p><canvas id="canvas" width="640" height="260"></canvas><p id="transferStatus"></p><dialog id="dialog">YuDesk styled dialog <button onclick="this.parentNode.close()">Close</button></dialog></main>
<script>
window.fixtureRun = Math.random();
window.copyResult=null;window.copyTrusted=false;window.clipboardMode='write';
document.getElementById('button').onclick=async e=>{window.copyTrusted=e.isTrusted;try{if(window.clipboardMode==='read'){await navigator.clipboard.readText();window.copyResult='read-allowed';}else{await navigator.clipboard.writeText('设备码：123456789\\nPIN：123456');window.copyResult='written';}}catch(_){window.copyResult='denied';}};
const ctx=canvas.getContext('2d',{alpha:false,desynchronized:true});ctx.fillStyle='#1673fb';ctx.fillRect(0,0,640,260);ctx.fillStyle='white';ctx.font='26px sans-serif';ctx.fillText('Canvas rendering verified',30,140);
window.features={fetch:typeof fetch==='function',bitmap:typeof createImageBitmap==='function',audio:typeof AudioContext==='function',timeout:typeof AbortSignal.timeout==='function',dialog:typeof dialog.showModal==='function',clipboard:!!navigator.clipboard,canvas:!!ctx};
window.featureResult=null;
(async()=>{try{const c=document.createElement('canvas');c.width=2;c.height=2;const x=c.getContext('2d');x.fillStyle='#12ab34';x.fillRect(0,0,2,2);const blob=await new Promise(r=>c.toBlob(r,'image/png'));const bitmap=await createImageBitmap(blob);x.clearRect(0,0,2,2);x.drawImage(bitmap,0,0);bitmap.close();const p=x.getImageData(0,0,1,1).data;const response=await fetch('/check',{signal:AbortSignal.timeout(2000)});const audio=new AudioContext({latencyHint:'interactive'});await audio.close();dialog.showModal();const open=dialog.open;dialog.close();window.featureResult={...window.features,pixel:p[0]===18&&p[1]===171&&p[2]===52,request:(await response.text())==='ok',modal:open};}catch(_){window.featureResult={failure:true};}})();
</script>'''.encode('utf-8')


class Handler(http.server.BaseHTTPRequestHandler):
    hits = []
    def do_GET(self):
        self.server.hits.append(self.path)
        if self.path.startswith('/download'):
            self.send_response(200)
            self.send_header('Content-Type','application/octet-stream')
            self.send_header('Content-Disposition','attachment; filename="native-fixture.txt"')
            self.send_header('Content-Length','18'); self.end_headers()
            self.wfile.write(b'YuDesk native file'); return
        if self.path.startswith('/deny'):
            self.send_response(403); self.end_headers(); return
        if self.path.startswith('/redirect-remote'):
            self.send_response(302); self.send_header('Location', self.server.remote); self.end_headers(); return
        self.send_response(200)
        self.send_header('Content-Type', 'text/plain' if self.path == '/check' else 'text/html; charset=utf-8')
        self.end_headers(); self.wfile.write(b'ok' if self.path == '/check' else TEST_PAGE)
    def log_message(self, *args): pass


class Helper:
    def __init__(self, binary, stderr=subprocess.DEVNULL):
        self.process = subprocess.Popen([binary], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=stderr, text=True, encoding='utf-8', bufsize=1)
        self.events = queue.Queue(); self.all = []
        def read():
            for line in self.process.stdout:
                value=json.loads(line); self.all.append(value); self.events.put(value)
        threading.Thread(target=read, daemon=True).start()
    def send(self, action, url=None):
        value={'action':action}
        if url is not None: value['url']=url
        self.process.stdin.write(json.dumps(value)+'\n'); self.process.stdin.flush()
    def event(self, name, timeout=15):
        deadline=time.monotonic()+timeout
        while time.monotonic()<deadline:
            try: event=self.events.get(timeout=max(.01,deadline-time.monotonic()))
            except queue.Empty: break
            if event['event']==name: return event
            if event['event']=='error' and event.get('code')=='download_failed': continue
            if event['event']=='error': raise AssertionError('helper error: '+event.get('code','unknown'))
        raise AssertionError('missing event: '+name)
    def evaluate(self, script):
        self.send('test-evaluate',script); return self.event('test_result')['value']
    def state(self):
        self.send('test-state'); return self.event('test_state')
    def wait(self):
        result=self.process.wait(timeout=5); assert result==0, result
    def close(self):
        if self.process.poll() is None:
            self.process.stdin.close(); self.wait()
    def click_fixture_button(self):
        windows=subprocess.check_output(['xdotool','search','--onlyvisible','--pid',str(self.process.pid)],text=True).splitlines()
        assert len(windows)==1,'expected only the fixture native window'
        # XTEST events in the virtual server, never the physical host pointer.
        subprocess.run(['xdotool','windowactivate','--sync',windows[0],'mousemove','--window',windows[0],'200','37','click','1'],check=True)


def run(test_binary, production_binary, output):
    manager=subprocess.Popen(['openbox'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    a=http.server.ThreadingHTTPServer(('127.0.0.1',0),Handler); a.hits=[]
    b=http.server.ThreadingHTTPServer(('127.0.0.1',0),Handler); b.hits=[]
    b_url=f'http://127.0.0.1:{b.server_port}/?access_token=fixture'
    a.remote=b_url
    for server in (a,b): threading.Thread(target=server.serve_forever,daemon=True).start()
    base=f'http://127.0.0.1:{a.server_port}'
    url=base+'/?access_token=fixture'
    helpers=[]
    try:
        p=Helper(test_binary); helpers.append(p)
        p.send('open',url); p.event('ready')
        state=p.state(); assert state['visible'] and not state['decorated'],state
        assert state['width']==860 and state['height']==600,state
        value=None
        for _ in range(30):
            value=p.evaluate('window.featureResult')
            if value is not None: break
            time.sleep(.1)
        assert value and all(value.values()) and 'failure' not in value,value
        print('PASS real WebKit fetch, image decode/pixels, AudioContext, timeout, dialog and clipboard API',flush=True)
        p.click_fixture_button()
        for _ in range(30):
            copied=p.evaluate('window.copyResult')
            if copied is not None: break
            time.sleep(.1)
        assert p.evaluate('window.copyTrusted') and copied=='written',copied
        p.send('test-clipboard-contents'); assert p.event('test_result')['value']
        # WebKit may read its own origin-tagged write without prompting. Replace
        # it with an unrelated native owner before testing read isolation.
        p.send('test-clipboard-replace'); time.sleep(.1)
        p.evaluate("window.copyResult=null;window.clipboardMode='read';true")
        p.click_fixture_button()
        for _ in range(30):
            copied=p.evaluate('window.copyResult')
            if copied is not None: break
            time.sleep(.1)
        assert copied=='denied',copied
        print('PASS trusted virtual-display button writes exact Chinese device+PIN clipboard; async clipboard read stays denied',flush=True)
        assert p.evaluate("navigator.userAgent.includes('YuDeskNative/2.0.0')")
        for action,text in [('complete','文件已保存'),('cancelled','已取消保存文件'),('failed','保存失败，请重试')]:
            p.send('test-transfer-'+action)
            assert p.evaluate("document.getElementById('transferStatus').textContent")==text
        print('PASS native user-agent marker and fixed safe save/cancel/failure page messages',flush=True)
        downloaded=output/('download-fixture-'+str(time.time_ns())+'.txt')
        for destination,expected in [(str(downloaded),'文件已保存'),('cancel','已取消保存文件'),(str(output/'missing-directory'/'failed.txt'),'保存失败，请重试')]:
            p.evaluate("document.getElementById('transferStatus').textContent='waiting';true")
            p.send('test-next-download',destination)
            p.evaluate("(()=>{const a=document.createElement('a');a.href='/download';a.download='native-fixture.txt';document.body.append(a);a.click();a.remove();return true})()")
            done=False
            for _ in range(30):
                text=p.evaluate("document.getElementById('transferStatus').textContent")
                if text==expected: done=True; break
                time.sleep(.1)
            assert done,(expected,text)
        assert downloaded.read_bytes()==b'YuDesk native file'
        assert any(e.get('code')=='download_failed' for e in p.all)
        print('PASS real WebKit downloads with test-only save chooser: exact file bytes, cancellation, failed save page status',flush=True)
        marker=p.evaluate('window.fixtureRun')
        for _ in range(4):
            p.send('hide'); assert not p.state()['visible']
            p.send('show'); assert p.state()['visible']
        assert p.evaluate('window.fixtureRun')==marker,'hide recreated page'
        p.send('minimize'); time.sleep(.3); assert p.state()['iconified']
        p.send('show'); time.sleep(.3); assert not p.state()['iconified']
        print('PASS 860x600 frameless, hide/show preserves page, minimize/restore',flush=True)
        before=p.state()
        p.evaluate("document.querySelector('header').dispatchEvent(new PointerEvent('pointerdown',{bubbles:true,button:0,isPrimary:true})); true")
        p.evaluate("document.querySelector('header').dispatchEvent(new MouseEvent('mousedown',{bubbles:true,button:0})); true")
        after=p.state(); assert (before['x'],before['y'])==(after['x'],after['y'])
        assert p.evaluate("window.webkit?.messageHandlers?.yudeskDrag === undefined")
        print('PASS synthetic drag ignored; isolated native bridge hidden from page world',flush=True)
        p.evaluate('location.href='+json.dumps(b_url)+'; true'); time.sleep(.3)
        assert not b.hits and p.evaluate('window.fixtureRun')==marker
        p.evaluate("window.open("+json.dumps(b_url)+"); true"); time.sleep(.2); assert not b.hits
        p.send('test-snapshot',str(output/'webkit-fixture.png')); p.event('test_snapshot_saved')
        assert (output/'webkit-fixture.png').stat().st_size>1000
        for _ in range(2):
            p.send('test-crash-web'); error=p.event('error'); assert error['code']=='web_process_terminated'; p.event('ready')
        p.send('test-crash-web'); assert p.event('error')['code']=='web_process_terminated'; time.sleep(3)
        assert p.process.poll() is None
        try: extra=p.events.get_nowait(); raise AssertionError('unexpected post-cap event '+extra['event'])
        except queue.Empty: pass
        print('PASS renderer crash reports; two recoveries only, helper stays alive',flush=True)
        p.close(); assert not any(e['event']=='closed' for e in p.all)

        p=Helper(test_binary); helpers.append(p); p.send('open',url); p.event('ready')
        p.send('test-close-native'); p.event('closed'); p.wait()
        print('PASS native close reports closed',flush=True)
        p=Helper(production_binary); helpers.append(p)
        p.send('test-state'); assert p.event('error')['code']=='invalid_command'
        p.send('open','http://example.invalid/?access_token=secret'); assert p.event('error')['code']=='invalid_url'
        p.send('open',url); p.event('ready'); p.send('open',url); assert p.event('error')['code']=='already_open'
        p.send('close'); p.wait(); assert not any(e['event']=='closed' for e in p.all)
        p=Helper(production_binary); helpers.append(p); p.send('open',url); p.event('ready'); p.close()
        assert not any(e['event']=='closed' for e in p.all)
        p=Helper(production_binary); helpers.append(p)
        p.process.stdin.write('x'*16385+'\n'); p.process.stdin.flush()
        assert p.event('error')['code']=='ipc_line_too_long'; p.wait()
        p=Helper(production_binary); helpers.append(p); p.send('open',base+'/deny?access_token=fixture')
        assert p.event('error')['code']=='load_http_error'; p.close()
        p=Helper(production_binary); helpers.append(p); p.send('open',base+'/redirect-remote?access_token=fixture')
        time.sleep(.5); assert not b.hits; p.close()
        assert all('secret' not in json.dumps(e) for helper in helpers for e in helper.all)
        print('PASS production IPC, no diagnostics, duplicate open, invalid URL, 16KiB cap, HTTP failure, cross-origin redirect, EOF/command-close semantics',flush=True)
    finally:
        for helper in helpers:
            if helper.process.poll() is None:
                helper.process.kill(); helper.process.wait(timeout=5)
        manager.terminate(); manager.wait(timeout=5)
        a.shutdown(); b.shutdown()


if __name__=='__main__':
    assert os.environ.get('DISPLAY') and os.environ.get('YUDESK_NATIVE_TEST_DISPLAY')=='1','use test.sh with an isolated Xvfb display'
    run(sys.argv[1],sys.argv[2],Path(sys.argv[3]).resolve())
