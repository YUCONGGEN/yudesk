package cn.yucg.yudesk;

import android.content.Context;
import android.net.wifi.WifiManager;

/** Keeps SSDP discovery available while a foreground UI or sharing service is active. */
final class MulticastLease {
    private static WifiManager.MulticastLock lock;
    private static int owners;
    private MulticastLease() {}

    static synchronized void acquire(Context context) {
        owners++;
        if (lock != null && lock.isHeld()) return;
        WifiManager manager=(WifiManager)context.getApplicationContext().getSystemService(Context.WIFI_SERVICE);
        if(manager==null)return;
        lock=manager.createMulticastLock("YuDesk-P2P-discovery");
        lock.setReferenceCounted(false);
        try{lock.acquire();}catch(RuntimeException ignored){lock=null;}
    }

    static synchronized void release() {
        if(owners>0)owners--;
        if(owners==0&&lock!=null){try{if(lock.isHeld())lock.release();}catch(RuntimeException ignored){}lock=null;}
    }
}
