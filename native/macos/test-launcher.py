"""LaunchServices reopen regression using a no-window AppKit shell and fake core.

Launches only the uniquely named fixture .app, in the background (-g). This does
not run the real Go client or helper, control input, or touch production config.
"""
import os
from pathlib import Path
import plistlib
import shutil
import signal
import subprocess
import sys
import tempfile
import time

def rows(path):
    return path.read_text().splitlines() if path.exists() else []

def wait_for(test,description):
    deadline=time.monotonic()+8
    while time.monotonic()<deadline:
        if test(): return
        time.sleep(.1)
    raise AssertionError(description)

def alive(pid):
    try: os.kill(pid,0); return True
    except ProcessLookupError: return False

root=Path(tempfile.mkdtemp(prefix='yudesk-launcher-fixture-',dir=Path(sys.argv[3]).resolve()))
app=root/'YuDeskLauncherFixture.app'; mac=app/'Contents'/'MacOS'; mac.mkdir(parents=True)
bundle='com.yudesk.launcherfixture.p'+str(os.getpid())
info={'CFBundleExecutable':'yudesk-launcher','CFBundleIdentifier':bundle,'CFBundleName':'YuDeskLauncherFixture',
      'CFBundlePackageType':'APPL','CFBundleVersion':'1','LSUIElement':True,'LSMinimumSystemVersion':'12.3'}
(app/'Contents'/'Info.plist').write_bytes(plistlib.dumps(info))
shutil.copy2(sys.argv[1],mac/'yudesk-launcher'); shutil.copy2(sys.argv[2],mac/'yudesk')
for path in mac.iterdir(): path.chmod(0o700)
events=root/'events'
core=launcher=None
try:
    # open --env applies solely to this fixture launch, never launchctl's global environment.
    subprocess.run(['/usr/bin/open','-g','--env','YUDESK_LAUNCHER_FIXTURE_STATE='+str(root),str(app)],check=True)
    wait_for(lambda: any(r.startswith('primary ') for r in rows(events)),'primary did not start')
    primary=[r.split() for r in rows(events) if r.startswith('primary ')][0]
    core,launcher=int(primary[1]),int(primary[2])
    for expected in range(1,4):
        time.sleep(.4)
        subprocess.run(['/usr/bin/open','-g',str(app)],check=True)
        wait_for(lambda: len([r for r in rows(events) if r.startswith('secondary ')])>=expected,'Finder reopen did not reach launcher')
    assert len([r for r in rows(events) if r.startswith('primary ')])==1,'more than one primary'
    assert alive(core) and alive(launcher)
    os.kill(core,signal.SIGTERM)
    wait_for(lambda: not alive(core),'fake core did not exit')
    wait_for(lambda: not alive(launcher),'launcher did not exit with core')
    print('PASS LaunchServices initial open + three reopens: one fake core, bounded secondary launches, launcher exits with core; no NSWindow')
finally:
    # Exact PIDs obtained from this fixture only; never enumerate/kill YuDesk processes.
    for pid in (core,launcher):
        if pid and alive(pid): os.kill(pid,signal.SIGTERM)
