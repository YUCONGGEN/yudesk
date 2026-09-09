"""XTEST drag regression on our own fixture in a private, non-root Xvfb.

Accepts existing production or diagnostic helpers; never rebuilds frozen files.
The page reports trusted input over fixture-only loopback HTTP, so production
checks need no test IPC and do not touch any clipboard or real device.
"""
import hashlib
import http.server
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import threading
import time
from urllib.parse import parse_qs, urlsplit

spec = importlib.util.spec_from_file_location('window_fixture', Path(__file__).with_name('test-window.py'))
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)

PAGE = b'''<!doctype html><meta charset="utf-8"><title>YuDesk drag fixture</title>
<style>body{margin:0;user-select:none}header{height:100px;background:#def}
button{position:absolute;left:20px;top:15px;width:100px;height:40px}
a{position:absolute;left:140px;top:15px;width:100px;height:40px}
input{position:absolute;left:260px;top:15px;width:100px;height:40px}
main{height:450px}iframe{position:absolute;left:500px;top:200px;width:200px;height:100px}</style>
<header data-window-drag><button id="button">Button</button><a href="#fixture" id="link" draggable="false">Link</a><input id="input"><span style="position:absolute;left:420px;top:25px">Drag title</span><span id="delayed" style="position:absolute;left:650px;top:25px">Delayed title</span></header>
<main id="main">Non-drag content</main><iframe src="/frame"></iframe>
<script>
window.blockedMs=0;
document.addEventListener('pointerdown',e=>{if(e.isTrusted&&e.target.id==='delayed'){const start=performance.now();while(performance.now()-start<350){}window.blockedMs=performance.now()-start;}},true);
for(const type of ['pointerdown','pointerup','pointercancel','mousedown','mouseup'])document.addEventListener(type,e=>{
 fetch('/input?'+new URLSearchParams({type:e.type,trusted:e.isTrusted,button:e.button,target:e.target.id||e.target.tagName,x:e.clientX,y:e.clientY,blockedMs:window.blockedMs}));
 if(e.type==='mousedown'&&e.target.id==='main'){
  document.querySelector('header').dispatchEvent(new PointerEvent('pointerdown',{bubbles:true,button:0,isPrimary:true}));
  document.querySelector('header').dispatchEvent(new MouseEvent('mousedown',{bubbles:true,button:0}));
 }
},true);
document.addEventListener('click',e=>{if(e.target.closest('a'))e.preventDefault();});
document.addEventListener('contextmenu',e=>e.preventDefault());
</script>'''


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        url = urlsplit(self.path)
        if url.path == '/input':
            self.server.inputs.append({k:v[0] for k,v in parse_qs(url.query).items()})
        self.send_response(200)
        self.send_header('Content-Type', 'text/html; charset=utf-8')
        self.end_headers()
        body = b'''<header data-window-drag style="height:90px;user-select:none">Iframe drag forbidden</header><script>document.addEventListener('mousedown',e=>fetch('/input?'+new URLSearchParams({type:e.type,trusted:e.isTrusted,button:e.button,target:'iframe',x:e.clientX,y:e.clientY})),true);</script>''' if url.path == '/frame' else PAGE
        self.wfile.write(b'ok' if url.path == '/input' else body)
    def log_message(self, *args):
        pass


def xdo(*args):
    return subprocess.check_output(['xdotool', *map(str,args)], text=True, timeout=5)


def geometry(window):
    value = dict(line.split('=',1) for line in xdo('getwindowgeometry','--shell',window).splitlines())
    return {k:int(value[k]) for k in ('X','Y','WIDTH','HEIGHT')}


def run(binary):
    print(json.dumps({'binary':binary,'sha256':hashlib.sha256(Path(binary).read_bytes()).hexdigest(),'display':os.environ['DISPLAY'],'uid':os.getuid()}),flush=True)
    manager = subprocess.Popen(['openbox'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    server = http.server.ThreadingHTTPServer(('127.0.0.1',0), Handler)
    server.inputs = []
    threading.Thread(target=server.serve_forever,daemon=True).start()
    helper = fixture.Helper(binary)
    failures = []
    try:
        helper.send('open',f'http://127.0.0.1:{server.server_port}/?access_token=fixture-drag-only')
        helper.event('ready')
        windows = xdo('search','--onlyvisible','--pid',helper.process.pid).splitlines()
        assert len(windows) == 1, windows
        window = windows[0]
        xdo('windowactivate','--sync',window)
        time.sleep(.3)
        # These are pointer positions in THIS fixture. No --window on mousedown:
        # XTEST generates actual server input instead of XSendEvent messages.
        cases = [('title-'+str(i),450,40,1,True) for i in range(4)] + [('button',60,35,1,False),
                 ('link',180,35,1,False), ('input',300,35,1,False),
                 ('main-and-synthetic-title',200,250,1,False),
                 ('iframe',550,240,1,False), ('right-title',450,40,3,False),
                 ('delayed-after-release',680,40,1,False), ('title-repeat',450,40,1,True)]
        for name,x,y,button,should_move in cases:
            xdo('windowmove','--sync',window,180,100)
            xdo('mousemove','--sync','--window',window,x,y)
            time.sleep(.35)
            server.inputs.clear()
            before = geometry(window)
            try:
                xdo('mousedown',button)
                time.sleep(.05 if name=='delayed-after-release' else .15)
                if name=='delayed-after-release':
                    xdo('mouseup',button)
                    time.sleep(.5)
                for dx,dy in ((30,15),(60,30),(90,45)):
                    xdo('mousemove','--sync',before['X']+x+dx,before['Y']+y+dy)
                    time.sleep(.08)
            finally:
                xdo('mouseup',button)
            time.sleep(.2)
            after = geometry(window)
            xdo('mousemove','--sync',before['X']+x+110,before['Y']+y+55)
            time.sleep(.1)
            released = geometry(window)
            delta = [after['X']-before['X'],after['Y']-before['Y']]
            trusted = any(e['type']=='mousedown' and e['trusted']=='true' and e['button']==str(0 if button==1 else 2) for e in server.inputs)
            ok = (delta == ([90,45] if should_move else [0,0]) and
                  after['WIDTH']==before['WIDTH'] and after['HEIGHT']==before['HEIGHT'] and released==after and
                  trusted)
            if name=='delayed-after-release':
                ok = ok and any(float(e.get('blockedMs',0))>=350 for e in server.inputs)
            result = dict(case=name,passed=ok,before=before,after=after,delta=delta,released=released,inputs=server.inputs[:])
            print(json.dumps(result),flush=True)
            if not ok:
                failures.append(name)
        helper.close()
        assert not failures, 'failed drag cases: '+', '.join(failures)
    finally:
        if helper.process.poll() is None:
            helper.process.kill(); helper.process.wait(timeout=5)
        manager.terminate(); manager.wait(timeout=5)
        server.shutdown(); server.server_close()


if __name__ == '__main__':
    assert os.getuid()!=0 and Path('/.dockerenv').exists(), 'isolated non-root Docker fixture only'
    assert os.environ.get('YUDESK_NATIVE_TEST_DISPLAY')=='1' and os.environ.get('DISPLAY'), 'use test-drag.sh'
    assert 'WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS' not in os.environ, 'keep WebKit sandbox enabled'
    run(sys.argv[1])
