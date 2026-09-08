package cn.yucg.yudesk;

import org.junit.Test;
import static org.junit.Assert.*;

public class RemoteActionsTest {
    private void chord(RemoteActions.Key[] keys,String modifier,String target){assertEquals(4,keys.length);assertEquals("key_down",keys[0].type);assertEquals(modifier,keys[0].code);assertEquals(target,keys[1].code);assertEquals("key_up",keys[2].type);assertEquals(target,keys[2].code);assertEquals("key_up",keys[3].type);assertEquals(modifier,keys[3].code);}
    @Test public void windowsUsesDesktopAndBackShortcuts(){chord(RemoteActions.home("windows"),"MetaLeft","KeyD");chord(RemoteActions.back("windows"),"AltLeft","ArrowLeft");assertEquals("桌面",RemoteActions.homeLabel("windows"));assertEquals("后退",RemoteActions.backLabel("windows"));}
    @Test public void androidKeepsSystemNavigation(){assertEquals("Escape",RemoteActions.back("android")[0].key);assertEquals("Home",RemoteActions.home("android")[0].key);assertEquals("主页",RemoteActions.homeLabel("android"));assertEquals("返回",RemoteActions.backLabel("android"));}
    @Test public void unknownPlatformNeverAssumesWindows(){for(String platform:new String[]{"","darwin","linux"}){assertEquals(2,RemoteActions.home(platform).length);assertEquals("Home 键",RemoteActions.homeLabel(platform));assertEquals("Esc",RemoteActions.backLabel(platform));}}
}
