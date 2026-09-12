package cn.yucg.yudesk;

import android.os.Handler;
import org.json.JSONArray;
import org.json.JSONObject;
import org.webrtc.PeerConnection;
import org.webrtc.RTCStats;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.function.BooleanSupplier;

/** Tries direct ICE first, then adds authenticated TURN without changing SRTP. */
final class IcePathController {
    private final PeerConnection pc;
    private final String policy;
    private final Handler main;
    private final BooleanSupplier alive;
    private final Runnable changed, negotiate;
    private final boolean hasRelay;
    private boolean closed, relay, recovering;
    private int restarts;
    private int rttMs=-1;
    private long availableOutgoingBitrate;
    private String path="正在直连";
    private final Runnable deadline=this::fallback;
    private final Runnable recovery=()->{recovering=false;fallback();};
    private final Runnable statistics=new Runnable(){public void run(){if(!valid())return;readStats();main.postDelayed(this,2000);}};

    IcePathController(PeerConnection pc,String policy,Handler main,BooleanSupplier alive,Runnable changed,Runnable negotiate){
        this.pc=pc;this.policy=policy;this.main=main;this.alive=alive;this.changed=changed;this.negotiate=negotiate;
        hasRelay=configuration(policy,true).iceServers.size()>configuration(policy,false).iceServers.size();
        int timeout=3000;try{timeout=new JSONObject(policy).optInt("directTimeoutMs",3000);}catch(Exception ignored){}
        main.postDelayed(deadline,Math.max(1000,Math.min(30000,timeout)));main.post(statistics);
    }

    static PeerConnection.RTCConfiguration configuration(String raw,boolean relay){
        List<PeerConnection.IceServer> servers=new ArrayList<>();
        try{
            JSONArray input=new JSONObject(raw==null?"{}":raw).optJSONArray("iceServers");
            if(input!=null)for(int i=0;i<input.length()&&i<16;i++){
                Object value=input.opt(i);JSONObject object=value instanceof JSONObject?(JSONObject)value:null;
                Object urls=object==null?value:object.opt("urls");JSONArray list=urls instanceof JSONArray?(JSONArray)urls:new JSONArray().put(urls);
                List<String> allowed=new ArrayList<>();
                for(int j=0;j<list.length()&&j<8;j++){
                    String url=list.optString(j);
                    if(url.length()<=256&&!url.matches(".*[\\r\\n\\t ].*")&&(url.startsWith("stun:")||(relay&&(url.startsWith("turn:")||url.startsWith("turns:")))))allowed.add(url);
                }
                if(!allowed.isEmpty()){
                    PeerConnection.IceServer.Builder builder=PeerConnection.IceServer.builder(allowed);
                    if(object!=null)builder.setUsername(object.optString("username","")).setPassword(object.optString("credential",""));
                    servers.add(builder.createIceServer());
                }
            }
        }catch(Exception failure){android.util.Log.w("YuDeskMeeting","Invalid ICE configuration");}
        PeerConnection.RTCConfiguration config=new PeerConnection.RTCConfiguration(servers);
        config.bundlePolicy=PeerConnection.BundlePolicy.MAXBUNDLE;
        config.continualGatheringPolicy=PeerConnection.ContinualGatheringPolicy.GATHER_CONTINUALLY;
        config.iceTransportsType=PeerConnection.IceTransportsType.ALL;
        config.iceCandidatePoolSize=2;
        config.rtcpMuxPolicy=PeerConnection.RtcpMuxPolicy.REQUIRE;
        config.audioJitterBufferMaxPackets=40;
        config.audioJitterBufferFastAccelerate=true;
        config.enableDscp=true;
        config.enableCpuOveruseDetection=true;
        config.screencastMinBitrate=300_000;
        return config;
    }

    String path(){return path;}
    int rttMs(){return rttMs;}
    long availableOutgoingBitrate(){return availableOutgoingBitrate;}
    private boolean valid(){return !closed&&alive.getAsBoolean();}
    private boolean connected(){PeerConnection.IceConnectionState state=pc.iceConnectionState();return state==PeerConnection.IceConnectionState.CONNECTED||state==PeerConnection.IceConnectionState.COMPLETED;}
    private void path(String value){if(!path.equals(value)){path=value;changed.run();}}
    void update(){
        if(!valid())return;
        if(connected()){main.removeCallbacks(deadline);main.removeCallbacks(recovery);recovering=false;readStats();return;}
        PeerConnection.IceConnectionState state=pc.iceConnectionState();
        if(state==PeerConnection.IceConnectionState.FAILED){if(!relay)fallback();else if(restarts++<2)restart();else path("网络连接不稳定");}
        else if(state==PeerConnection.IceConnectionState.DISCONNECTED&&!recovering){recovering=true;path("正在重连");main.postDelayed(recovery,3000);}
    }
    private void fallback(){
        if(!valid()||connected())return;
        if(!hasRelay){path("网络连接不稳定");return;}
        main.removeCallbacks(deadline);relay=true;path("正在连接中转");
        try{if(!pc.setConfiguration(configuration(policy,true))){path("中转配置未生效");return;}pc.restartIce();negotiate.run();}catch(RuntimeException error){path("网络连接不稳定");}
    }
    private void restart(){if(!valid())return;path("正在重连");try{pc.restartIce();negotiate.run();}catch(RuntimeException error){path("网络连接不稳定");}}
    private void readStats(){
        if(!valid()||!connected())return;
        try{pc.getStats(report->main.post(()->{
            if(!valid())return;Map<String,RTCStats> stats=report.getStatsMap();RTCStats pair=null;
            for(RTCStats value:stats.values())if("transport".equals(value.getType())){Object id=value.getMembers().get("selectedCandidatePairId");if(id!=null)pair=stats.get(id.toString());}
            if(pair==null)for(RTCStats value:stats.values())if("candidate-pair".equals(value.getType())&&"succeeded".equals(value.getMembers().get("state"))&&Boolean.TRUE.equals(value.getMembers().get("nominated"))){pair=value;break;}
            if(pair==null)return;RTCStats local=stats.get(String.valueOf(pair.getMembers().get("localCandidateId"))),remote=stats.get(String.valueOf(pair.getMembers().get("remoteCandidateId")));
            int oldRTT=rttMs;long oldAvailable=availableOutgoingBitrate;Object rtt=pair.getMembers().get("currentRoundTripTime"),available=pair.getMembers().get("availableOutgoingBitrate");
            if(rtt instanceof Number)rttMs=Math.max(0,(int)Math.round(((Number)rtt).doubleValue()*1000));
            availableOutgoingBitrate=available instanceof Number?Math.max(0,((Number)available).longValue()):0;
            boolean viaRelay=local!=null&&"relay".equals(local.getMembers().get("candidateType"))||remote!=null&&"relay".equals(remote.getMembers().get("candidateType"));String next=viaRelay?"中转连接":"P2P 直连";
            if(!path.equals(next))path(next);else if(Math.abs(oldRTT-rttMs)>=10||Math.abs(oldAvailable-availableOutgoingBitrate)>=Math.max(100_000,oldAvailable/5))changed.run();
        }));}catch(RuntimeException ignored){}
    }
    void close(){closed=true;main.removeCallbacks(deadline);main.removeCallbacks(recovery);main.removeCallbacks(statistics);}
}
