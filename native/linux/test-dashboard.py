"""Real repository dashboard on an existing diagnostic WebKit helper.

No production binaries are rebuilt. All APIs, IDs and device status are synthetic
and served on a newly allocated loopback port; no Go/relay or real device starts.
"""
import hashlib
import http.server
import importlib.util
import json
import os
from pathlib import Path
import re
import resource
import subprocess
import sys
import threading
import time
from urllib.parse import parse_qs, urlsplit

TOKEN = 'native-dashboard-synthetic-token'
ROOT = Path(__file__).resolve().parents[2]
UI = ROOT / 'internal' / 'viewerapp' / 'ui'
ASSET_NAMES = ('compat.js', 'dashboard.html', 'dashboard.css', 'dashboard.js',
               'footer.html', 'footer.css', 'window-ui.js', 'window-ui.css', 'icon.svg', 'beian.svg', 'session.js', 'session.html')
ASSETS = {name: (UI/name).read_bytes() for name in ASSET_NAMES}
LOCAL = {'id':'fixture-local-only','name':'我的电脑','code':'123456789','pin':'000000',
         'pinSynced':True,'pinRevision':1,'receiving':True,'online':True,'active':True,
         'connected':False,'files':False,'fileDirectory':'演示接收目录','activeUntil':'永久授权（演示）'}
DEVICES = [{'deviceID':'234567890','name':'办公室电脑'},
           {'deviceID':'345678901','name':'家里的电脑'},
           {'deviceID':'456789012','name':'工作笔记本'}]
STATUSES = [{'id':d['deviceID'],'deviceCode':d['deviceID'],'online':i!=2,
             'connected':i==1,'active':True,'ready':i==0} for i,d in enumerate(DEVICES)]
SERVICE = {'supported':True,'packageManaged':True,'platform':'linux','installed':True,
           'trustedClient':True,'running':True,'updateRequired':False,
           'message':'已通过 Linux 安装包安装（演示）'}
OBSERVE = '''<script>
window.__dashboardErrors=[];
const report=value=>{if(window.__dashboardErrors.length<20)window.__dashboardErrors.push(String(value).slice(0,400));};
addEventListener('error',e=>report(e.message||('resource:'+e.target?.tagName)),true);
addEventListener('unhandledrejection',e=>report('unhandled:'+String(e.reason)));
addEventListener('securitypolicyviolation',e=>report('CSP:'+e.violatedDirective));
const oldError=console.error;console.error=(...values)=>{report('console:'+values.join(' '));oldError.apply(console,values);};
</script>'''


def page():
    html=ASSETS['dashboard.html'].decode('utf-8')
    footer=ASSETS['footer.html'].decode('utf-8').replace('{{define "footer"}}','').replace('{{end}}','')
    html=html.replace('{{template "footer" .}}',footer)
    for key,value in {'{{.Version}}':'2.0.0','{{.Token}}':TOKEN,'{{.InstallPrompt}}':'false',
                      '{{if not .Message}}hidden{{end}}':'hidden','{{.Message}}':'','{{.DeviceID}}':''}.items():
        html=html.replace(key,value)
    assert '{{' not in html,'unhandled Go template directive; update fixture explicitly'
    return html.replace('<head>','<head>'+OBSERVE,1).encode('utf-8')


class Server(http.server.ThreadingHTTPServer):
    daemon_threads=True
    def __init__(self):
        super().__init__(('127.0.0.1',0),Handler)
        self.hits=[]; self.failures=[]


class Handler(http.server.BaseHTTPRequestHandler):
    def send(self,value,mime='application/json',status=200):
        body=value if isinstance(value,bytes) else json.dumps(value,ensure_ascii=False).encode('utf-8')
        self.send_response(status)
        self.send_header('Content-Type',mime)
        self.send_header('Content-Length',str(len(body)))
        self.send_header('Cache-Control','no-store')
        self.send_header('X-Content-Type-Options','nosniff')
        self.send_header('X-Frame-Options','DENY')
        self.send_header('Content-Security-Policy',"default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' blob: data:")
        self.end_headers(); self.wfile.write(body)
    def do_GET(self):
        url=urlsplit(self.path); self.server.hits.append(url.path)
        if parse_qs(url.query).get('access_token') != [TOKEN]:
            self.server.failures.append('missing fixture token: '+url.path)
            self.send({'error':'fixture token required'},status=403); return
        if url.path=='/': self.send(page(),'text/html; charset=utf-8'); return
        match=re.fullmatch(r'/assets/([a-z-]+\.(?:css|js|svg))',url.path)
        if match and match[1] in ASSETS:
            name=match[1]
            mime='text/css; charset=utf-8' if name.endswith('.css') else 'text/javascript; charset=utf-8' if name.endswith('.js') else 'image/svg+xml'
            self.send(ASSETS[name],mime); return
        routes={'/api/local/status':LOCAL,'/api/devices':{'local':LOCAL,'devices':DEVICES},
                '/api/device/status':{'devices':STATUSES},'/api/local/service/status':SERVICE,
                '/api/local/approval':{'pending':None},'/api/ui/watch':{}}
        if url.path in routes: self.send(routes[url.path]); return
        self.server.failures.append('unmocked GET: '+url.path)
        self.send({'error':'unmocked route'},status=404)
    def do_POST(self):
        self.server.failures.append('unexpected POST: '+urlsplit(self.path).path)
        self.send({'error':'mutations are not part of this fixture'},status=405)
    def log_message(self,*args): pass


LAYOUT = '''(()=>{
 const shown=e=>e.getClientRects().length && getComputedStyle(e).visibility!=='hidden';
 const pane=[...document.querySelectorAll('.pane')].find(e=>!e.hidden), footer=document.querySelector('.app-footer');
 const metrics=e=>({w:e.clientWidth,h:e.clientHeight,sw:e.scrollWidth,sh:e.scrollHeight,x:e.scrollLeft,y:e.scrollTop,overflow:getComputedStyle(e).overflow});
 const rect=e=>{const r=e.getBoundingClientRect();return {x:r.x,y:r.y,w:r.width,h:r.height,bottom:r.bottom,right:r.right};};
 const clipped=[...pane.querySelectorAll('button,input,summary,.settings-card,.device-table,.pagination,.recent-grid')].filter(shown).filter(e=>{
   const r=e.getBoundingClientRect(),p=pane.getBoundingClientRect();return r.left<p.left-1||r.right>p.right+1||r.bottom>p.bottom+1||r.top<p.top-1;
 }).map(e=>e.id||e.className||e.tagName);
 return {viewport:{w:innerWidth,h:innerHeight},root:metrics(document.documentElement),body:metrics(document.body),content:metrics(document.querySelector('.content')),pane:metrics(pane),paneID:pane.id,
  footer:rect(footer),credit:footer.textContent,clipped,errors:window.__dashboardErrors,
  mode:{value:document.getElementById('runMode').value,disabled:document.getElementById('runMode').disabled,serviceVisible:!document.getElementById('windowsService').hidden,title:document.getElementById('serviceTitle').textContent},
  activationOpen:document.getElementById('activation').open,fontStatus:document.fonts.status,
  buttons:{install:document.getElementById('installService').hidden,remove:document.getElementById('removeService').hidden,restart:document.getElementById('restartInstalled').hidden}};
})()'''


def wait(helper,expression,description):
    deadline=time.monotonic()+10
    while time.monotonic()<deadline:
        if helper.evaluate(expression): return
        time.sleep(.1)
    raise AssertionError(description)


def check_layout(helper,pane_name):
    info=helper.evaluate(LAYOUT)
    assert info['paneID']=='pane-'+pane_name,info
    assert info['viewport']=={'w':860,'h':600},info['viewport']
    for key in ('root','body','content','pane'):
        m=info[key]
        assert m['sw']<=m['w']+1 and m['sh']<=m['h']+1,(pane_name,key,m)
        assert m['x']==0 and m['y']==0,(pane_name,key,m)
        assert m['overflow']=='hidden',(pane_name,key,m)
    assert not info['clipped'],info['clipped']
    assert abs(info['footer']['bottom']-600)<=1 and info['footer']['h']==24,info['footer']
    assert '郁从根' in info['credit'] and '皖ICP备20003241号-3' in info['credit'] and '低延迟高响应' in info['credit']
    assert info['mode']=={'value':'installed','disabled':True,'serviceVisible':True,'title':'安装与运行'},info['mode']
    assert all(info['buttons'].values()) and not info['activationOpen'],info
    assert info['fontStatus']=='loaded' and info['errors']==[],info
    return info


def check_fullscreen(helper):
    # Exercise the exact current session.js handler, not a newly invented one.
    # Other session services (input/frames/relay) intentionally remain absent.
    handlers=[line for line in ASSETS['session.js'].decode('utf-8').splitlines() if line.startswith('async function fullscreen()')]
    assert len(handlers)==1,'update fixture if the session fullscreen handler changes'
    session_html=ASSETS['session.html'].decode('utf-8')
    buttons=[re.search(r'<button id="'+id+r'"[^>]*>[^<]*</button>',session_html).group() for id in ('fullscreen','exitFullscreen')]
    helper.evaluate('''(()=>{const desktop=document.querySelector('main'),screen=desktop,keyboardCapture=document.getElementById('targetCode'),canControl=false;
      const showError=e=>{window.__fullscreenError=String(e.message||e);};window.__fullscreenError=null;
      desktop.tabIndex=-1;desktop.insertAdjacentHTML('beforeend','''+json.dumps(''.join(buttons))+''');
      const b=document.getElementById('fullscreen'),exit=document.getElementById('exitFullscreen');
      for(const [i,button] of [b,exit].entries())button.style.cssText='position:fixed;left:'+(450+i*200)+'px;top:4px;z-index:10000;display:block;background:#1478f7;color:white;height:30px;padding:4px 8px';
      '''+handlers[0]+''';b.onclick=exit.onclick=e=>{window.__fullscreenTrusted=e.isTrusted;fullscreen();};return true;})()''')
    # The production safe fallback is explicitly native-window fullscreen, not
    # DOM fullscreen. The original page handler must remain safe if called by JS.
    # Some WebKit releases still advertise fullscreenEnabled after the native
    # setting disables requests. Assert the actual rejection, not that hint.
    helper.evaluate("document.getElementById('fullscreen').click();true")
    wait(helper,'window.__fullscreenError!==null','disabled DOM fullscreen did not reject safely')
    assert helper.state()['width']==860
    helper.evaluate('window.__fullscreenError=null;window.__fullscreenTrusted=null;true')
    window_pid=helper.process.pid
    if os.environ.get('YUDESK_NATIVE_DEBUG_BINARY'):
        child=subprocess.check_output(['pgrep','-P',str(window_pid),'-x','yudesk-window-t'],text=True).strip()
        window_pid=int(child)
    windows=subprocess.check_output(['xdotool','search','--onlyvisible','--pid',str(window_pid)],text=True).splitlines()
    assert len(windows)==1,'fullscreen test targets only its one virtual helper window'
    def click(id='fullscreen'):
        point=helper.evaluate("(()=>{const r=document.getElementById('"+id+"').getBoundingClientRect();return [Math.round(r.x+r.width/2),Math.round(r.y+r.height/2)]})()")
        subprocess.run(['xdotool','windowactivate','--sync',windows[0],'mousemove','--window',windows[0],str(point[0]),str(point[1]),'click','1'],check=True)
    for cycle in range(3):
        click()
        wait(helper,"document.fullscreenElement===null && innerWidth===1280 && innerHeight===800 && document.getElementById('fullscreen').getAttribute('aria-pressed')==='true'",'native fullscreen did not fill the virtual monitor')
        entered=helper.state(); assert entered['width']==1280 and entered['height']==800,entered
        # Even while fullscreen, IPC remains responsive and Show reuses the view.
        marker=helper.evaluate('window.__dashboardErrors.length')
        helper.send('hide'); assert not helper.state()['visible']
        helper.send('show'); assert helper.state()['visible']
        assert helper.evaluate('window.__dashboardErrors.length')==marker
        if cycle==2:
            subprocess.run(['xdotool','windowactivate','--sync',windows[0],'key','Escape'],check=True)
        else: click('exitFullscreen' if cycle==1 else 'fullscreen')
        try:
            wait(helper,"document.fullscreenElement===null && innerWidth===860 && innerHeight===600 && document.getElementById('fullscreen').getAttribute('aria-pressed')==='false'",'fullscreen exit did not restore original size')
        except AssertionError:
            print('Fullscreen exit diagnostic cycle='+str(cycle)+' native='+str(helper.state())+' page='+str(helper.evaluate("({width:innerWidth,height:innerHeight,label:document.getElementById('fullscreen').outerHTML})")),flush=True)
            raise
        exited=helper.state(); assert exited['width']==860 and exited['height']==600 and not exited['decorated'],exited
    assert helper.evaluate('window.__fullscreenError') is None
    assert helper.evaluate('window.__fullscreenTrusted===null'),'trusted click must not call affected DOM handler'
    helper.evaluate("document.getElementById('fullscreen').remove();document.getElementById('exitFullscreen').remove();true")
    print('PASS native-window fullscreen fallback: 3 trusted-click cycles 1280x800 / 860x600, hide/show preserves view, Escape exits; synthetic click rejected safely',flush=True)
    return {'mode':'native-window-fallback','entered':entered,'exited':exited,'trustedClicks':True,'cycles':3,'syntheticDenied':True,'escapeExit':True,'handlerSourceSHA256':hashlib.sha256(handlers[0].encode()).hexdigest()}


def run(binary,output):
    resource.setrlimit(resource.RLIMIT_CORE,(0,0))
    assert os.environ.get('YUDESK_NATIVE_TEST_DISPLAY')=='1' and os.environ.get('DISPLAY'),'use test-dashboard.sh in isolated Xvfb'
    assert 'WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS' not in os.environ,'final dashboard test must keep WebKit sandbox enabled'
    output.mkdir(parents=True,exist_ok=True)
    font=subprocess.check_output(['fc-match','-f','%{family}',':lang=zh-cn'],text=True)
    assert 'CJK' in font,'install fonts-noto-cjk in this test container first'
    spec=importlib.util.spec_from_file_location('native_window_tests',Path(__file__).with_name('test-window.py'))
    module=importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
    manager=subprocess.Popen(['openbox'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    server=Server(); threading.Thread(target=server.serve_forever,daemon=True).start()
    helper=None
    report={'font':font,'helperSHA256':hashlib.sha256(binary.read_bytes()).hexdigest(),
            'uiSHA256':{name:hashlib.sha256(value).hexdigest() for name,value in ASSETS.items()},'pages':{}}
    try:
        helper=module.Helper(str(binary),stderr=(output/'webkit-dashboard-stderr.log').open('w'))
        helper.send('open',f'http://127.0.0.1:{server.server_port}/?access_token={TOKEN}'); helper.event('ready')
        wait(helper,"document.getElementById('localCode').textContent==='123 456 789' && document.querySelectorAll('.device-row').length===4 && document.getElementById('runMode').disabled && document.fonts.status==='loaded'",'real dashboard did not initialize')
        helper.evaluate("document.getElementById('togglePIN').click();true")
        assert helper.evaluate("document.getElementById('localPIN').textContent==='••••••'")
        for pane,name in [('remote','dashboard'),('devices','devices'),('settings','settings')]:
            helper.evaluate("document.querySelector('nav [data-tab=\""+pane+"\"]').click();true")
            if pane=='devices':
                wait(helper,"document.querySelector('[data-presence-id=\"345678901\"]').textContent==='连接中'",'real device status rendering failed')
                assert helper.evaluate("document.querySelector('[data-presence-id=\"234567890\"]').textContent==='在线' && document.querySelector('[data-presence-id=\"456789012\"]').textContent==='离线'")
                assert helper.evaluate("document.getElementById('devicePageInfo').textContent.includes('共 4 台')")
            report['pages'][pane]=check_layout(helper,pane)
            path=output/('webkit-'+name+'.png'); helper.send('test-snapshot',str(path)); helper.event('test_snapshot_saved')
            assert path.stat().st_size>5000
            print('PASS real WebKit '+pane+': no scroll/clipping, footer visible, package-managed installed mode, no JS errors; '+path.name,flush=True)
        report['fullscreen']=check_fullscreen(helper)
        # Keep the real periodic status loops alive long enough to exercise them.
        time.sleep(3.2)
        assert helper.evaluate('window.__dashboardErrors')==[]
        assert not server.failures,server.failures
        requested=set(server.hits)
        for name in ('compat.js','dashboard.js','dashboard.css','footer.css','window-ui.js','window-ui.css','icon.svg','beian.svg'):
            assert '/assets/'+name in requested,'real asset not loaded: '+name
        report['requestedPaths']=sorted(requested); report['errors']=[]
        (output/'webkit-dashboard-report.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf-8')
        helper.close(); assert not any(e.get('event')=='closed' for e in helper.all)
        print('PASS actual repository HTML/CSS/JS/footer; synthetic loopback APIs only',flush=True)
    finally:
        if helper and helper.process.poll() is None: helper.process.kill(); helper.process.wait(timeout=5)
        manager.terminate(); manager.wait(timeout=5); server.shutdown(); server.server_close()


if __name__=='__main__':
    run(Path(sys.argv[1]).resolve(),Path(sys.argv[2]).resolve())
