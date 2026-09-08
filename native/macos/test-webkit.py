"""Offscreen WebKit probe; never opens an NSWindow or targets production data."""
import http.server
import subprocess
import sys
import threading

class Fixture(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header('Content-Type','text/plain' if self.path=='/check' else 'text/html; charset=utf-8')
        self.end_headers()
        self.wfile.write(b'ok' if self.path=='/check' else b'<!doctype html><meta charset="utf-8"><title>Offscreen test</title><body>YuDesk test</body>')
    def log_message(self,*args): pass

server=http.server.ThreadingHTTPServer(('127.0.0.1',0),Fixture)
threading.Thread(target=server.serve_forever,daemon=True).start()
try:
    result=subprocess.run([sys.argv[1],f'http://127.0.0.1:{server.server_port}/'],
                          stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,text=True,timeout=20)
    print(result.stdout,end=''); sys.exit(result.returncode)
finally: server.shutdown()
