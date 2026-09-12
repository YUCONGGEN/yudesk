package cn.yucg.yudesk;

/** Pure meeting uplink policy shared by camera and screen senders. */
final class ConferenceTuning {
    private ConferenceTuning() {}

    static int videoBitrate(boolean screen,int peerCount,int rttMs,long availableOutgoingBitrate){
        int count=Math.max(1,peerCount),floor=screen?500_000:300_000,emergencyFloor=screen?250_000:150_000,ceiling=screen?3_500_000:1_500_000,total=screen?8_000_000:4_000_000;
        double target=Math.max(floor,Math.min(ceiling,Math.round((double)total/count)));
        if(rttMs>300)target*=.55;else if(rttMs>180)target*=.70;else if(rttMs>100)target*=.85;
        if(availableOutgoingBitrate>0)target=Math.min(target,Math.max(emergencyFloor,availableOutgoingBitrate*.82));
        return Math.max(emergencyFloor,(int)Math.round(target));
    }

    static boolean needsUpdate(int previous,int target,boolean previousScreen,boolean screen){
        return previous<=0||previousScreen!=screen||Math.abs(target-previous)>=Math.max(100_000,previous*.15);
    }
}
