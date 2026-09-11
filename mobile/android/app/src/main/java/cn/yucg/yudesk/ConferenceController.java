package cn.yucg.yudesk;

import android.Manifest;
import android.app.Activity;
import android.content.ContentValues;
import android.content.Context;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.hardware.display.DisplayManager;
import android.hardware.display.VirtualDisplay;
import android.media.AudioDeviceInfo;
import android.media.AudioManager;
import android.media.MediaRecorder;
import android.media.projection.MediaProjection;
import android.media.projection.MediaProjectionManager;
import android.net.Uri;
import android.os.Build;
import android.os.Environment;
import android.os.Handler;
import android.os.Looper;
import android.provider.MediaStore;
import android.util.DisplayMetrics;
import cn.yucg.bridge.core.Conference;
import org.json.JSONArray;
import org.json.JSONObject;
import org.webrtc.AudioSource;
import org.webrtc.AudioTrack;
import org.webrtc.Camera2Enumerator;
import org.webrtc.CameraEnumerator;
import org.webrtc.CameraVideoCapturer;
import org.webrtc.DataChannel;
import org.webrtc.DefaultVideoDecoderFactory;
import org.webrtc.DefaultVideoEncoderFactory;
import org.webrtc.EglBase;
import org.webrtc.IceCandidate;
import org.webrtc.MediaConstraints;
import org.webrtc.MediaStream;
import org.webrtc.MediaStreamTrack;
import org.webrtc.PeerConnection;
import org.webrtc.PeerConnectionFactory;
import org.webrtc.RtpReceiver;
import org.webrtc.RtpSender;
import org.webrtc.RtpTransceiver;
import org.webrtc.ScreenCapturerAndroid;
import org.webrtc.SdpObserver;
import org.webrtc.SessionDescription;
import org.webrtc.SurfaceTextureHelper;
import org.webrtc.VideoCapturer;
import org.webrtc.VideoSource;
import org.webrtc.VideoTrack;
import java.io.File;
import java.io.FileInputStream;
import java.io.OutputStream;
import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.atomic.AtomicBoolean;

/** Native WebRTC mesh meeting. The relay sees signaling only; media uses DTLS/SRTP. */
final class ConferenceController {
    static final int REQUEST_INITIAL_MEDIA=51, REQUEST_MICROPHONE=52, REQUEST_CAMERA=53;
    private static final AtomicBoolean WEBRTC_INITIALIZED=new AtomicBoolean();
    private final MainActivity activity;
    private final Conference channel;
    private final String code, localName;
    private final boolean requestedHost;
    private final Handler main=new Handler(Looper.getMainLooper());
    private final ExecutorService reader=Executors.newSingleThreadExecutor(r->new Thread(r,"YuDesk-conference-read"));
    private final ExecutorService writer=Executors.newSingleThreadExecutor(r->new Thread(r,"YuDesk-conference-write"));
    private final Map<String,Participant> participants=new LinkedHashMap<>();
    private final Map<String,Peer> peers=new LinkedHashMap<>();
    private final EglBase egl;
    private final PeerConnectionFactory factory;
    private final ConferenceUi presentation;
    private final AudioManager audioManager;
    private final int oldAudioMode;
    private final boolean oldSpeaker;
    private final AudioDeviceInfo oldCommunicationDevice;
    private final AudioManager.OnAudioFocusChangeListener audioFocusListener=focusChange->{};
    private AudioSource audioSource;
    private AudioTrack audioTrack;
    private VideoSource cameraSource, screenSource;
    private VideoTrack cameraTrack, screenTrack;
    private VideoCapturer cameraCapturer;
    private ScreenCapturerAndroid screenCapturer;
    private SurfaceTextureHelper cameraTexture, screenTexture;
    private String selfID="", hostID="";
    private boolean closed, intentional, screenSharing, recording, speakerEnabled=true;
    private boolean frontCamera=true;
    private MediaRecorder recorder;
    private MediaProjection recordProjection;
    private VirtualDisplay recordDisplay;
    private File recordFile;
    private final Runnable projectionStopped=()->{if(closed)return;if(screenSharing)stopScreenShare();if(recording)stopRecording(true);};

    ConferenceController(MainActivity activity, Conference channel, String code, String name, boolean host, boolean microphone, boolean camera) throws Exception {
        this.activity=activity;this.channel=channel;this.code=code;this.localName=name;this.requestedHost=host;
        initializeWebRTC(activity.getApplicationContext());
        egl=EglBase.create();
        factory=PeerConnectionFactory.builder()
                .setVideoEncoderFactory(new DefaultVideoEncoderFactory(egl.getEglBaseContext(),true,true))
                .setVideoDecoderFactory(new DefaultVideoDecoderFactory(egl.getEglBaseContext()))
                .createPeerConnectionFactory();
        audioManager=(AudioManager)activity.getSystemService(Context.AUDIO_SERVICE);oldAudioMode=audioManager.getMode();oldSpeaker=audioManager.isSpeakerphoneOn();oldCommunicationDevice=Build.VERSION.SDK_INT>=Build.VERSION_CODES.S?audioManager.getCommunicationDevice():null;audioManager.requestAudioFocus(audioFocusListener,AudioManager.STREAM_VOICE_CALL,AudioManager.AUDIOFOCUS_GAIN_TRANSIENT);routeAudioToSpeaker();
        presentation=new ConferenceUi(activity,code,egl.getEglBaseContext(),new ConferenceUi.Actions(){
            @Override public void microphone(){toggleMicrophone();}
            @Override public void speaker(){toggleSpeaker();}
            @Override public void camera(){toggleCamera();}
            @Override public void switchCamera(){ConferenceController.this.switchCamera();}
            @Override public void share(){if(screenSharing)stopScreenShare();else activity.requestConferenceProjection(false);}
            @Override public void record(){if(recording)stopRecording(true);else activity.requestConferenceProjection(true);}
            @Override public void leave(){activity.requestConferenceLeave();}
            @Override public void transfer(String id,String name){activity.requestConferenceTransfer(id,name);}
        });
        Participant local=new Participant("local",name);local.host=host;participants.put(local.id,local);
        if(microphone)createMicrophone();if(camera)createCamera();
        local.microphone=audioTrack!=null&&audioTrack.enabled();local.camera=cameraTrack!=null&&cameraTrack.enabled();
        presentation.upsert(local.view(true));if(cameraTrack!=null)presentation.attachVideo("local",cameraTrack,true);render();
        ConferenceProjectionService.onStopped=projectionStopped;
        reader.execute(this::readLoop);
    }

    android.view.View view(){return presentation.root;}
    boolean isHost(){return !selfID.isEmpty()&&selfID.equals(hostID);}
    boolean isClosed(){return closed;}

    private static void initializeWebRTC(Context context){if(WEBRTC_INITIALIZED.compareAndSet(false,true)){PeerConnectionFactory.initialize(PeerConnectionFactory.InitializationOptions.builder(context).setFieldTrials("WebRTC-H264HighProfile/Enabled/").createInitializationOptions());}}

    private void readLoop(){try{while(!closed){String raw=channel.read();JSONObject message=new JSONObject(raw);main.post(()->handle(message));}}catch(Exception failure){main.post(()->{if(!closed)finish("会议连接已断开，请检查网络后重新加入",true);});}}

    private void handle(JSONObject message){if(closed)return;try{switch(message.optString("type")){
        case "welcome": welcome(message);break;
        case "peer-joined": {Participant value=new Participant(message.getString("id"),safeName(message.optString("name")));participants.put(value.id,value);render();break;}
        case "peer-left": removePeer(message.optString("id"));break;
        case "state": state(message);break;
        case "host": changeHost(message.optString("host"));break;
        case "signal": signal(message);break;
        case "ended": finish(message.optString("message","主持人已结束会议"),false);break;
        case "pong": break;
        default: break;
    }}catch(Exception failure){finish("会议数据异常，请重新加入",true);}}

    private void welcome(JSONObject message) throws Exception {
        selfID=message.getString("id");hostID=message.getString("host");Participant local=participants.remove("local");local.id=selfID;local.host=selfID.equals(hostID);participants.put(selfID,local);presentation.rename("local",selfID);
        JSONArray values=message.optJSONArray("peers");if(values!=null)for(int i=0;i<values.length();i++){JSONObject peer=values.getJSONObject(i);Participant p=Participant.from(peer);participants.put(p.id,p);createPeer(p.id,true);}
        presentation.setConnection("已端到端加密",false);sendState();render();
    }

    private void state(JSONObject message){Participant p=participants.get(message.optString("id"));if(p==null)return;p.microphone=message.optBoolean("microphone");p.camera=message.optBoolean("camera");p.screen=message.optBoolean("screen");p.recording=message.optBoolean("recording");render();}
    private void changeHost(String id){boolean wasHost=isHost();hostID=id;for(Participant p:participants.values())p.host=p.id.equals(hostID);if(wasHost&&!isHost()){if(screenSharing)stopScreenShare();if(recording)stopRecording(true);}render();}

    private void signal(JSONObject message) throws Exception {String from=message.getString("from"),kind=message.getString("signal");Peer peer=peers.get(from);if(peer==null)peer=createPeer(from,false);if(peer==null)return;if(kind.equals("candidate")){JSONObject value=new JSONObject(message.getString("candidate"));IceCandidate candidate=new IceCandidate(value.optString("sdpMid",null),value.optInt("sdpMLineIndex"),value.optString("candidate"));if(peer.remoteSet)peer.pc.addIceCandidate(candidate);else if(peer.candidates.size()<64)peer.candidates.add(candidate);return;}SessionDescription description=new SessionDescription(kind.equals("offer")?SessionDescription.Type.OFFER:SessionDescription.Type.ANSWER,message.getString("sdp"));setRemote(peer,description);}

    private Peer createPeer(String id,boolean offer){if(closed||id.equals(selfID))return null;Peer existing=peers.get(id);if(existing!=null)return existing;Peer peer=new Peer(id);PeerConnection.RTCConfiguration config=new PeerConnection.RTCConfiguration(Collections.singletonList(new PeerConnection.IceServer("stun:www.yucg.cn:8233")));config.bundlePolicy=PeerConnection.BundlePolicy.MAXBUNDLE;config.continualGatheringPolicy=PeerConnection.ContinualGatheringPolicy.GATHER_CONTINUALLY;config.iceCandidatePoolSize=2;peer.pc=factory.createPeerConnection(config,peer);if(peer.pc==null){notice("当前设备无法创建会议媒体连接");return null;}if(audioTrack!=null)peer.audioSender=peer.pc.addTrack(audioTrack,Collections.singletonList("yudesk"));VideoTrack outgoing=screenSharing?screenTrack:cameraTrack;if(outgoing!=null)peer.videoSender=peer.pc.addTrack(outgoing,Collections.singletonList("yudesk"));peers.put(id,peer);if(offer){peer.dataChannel=peer.pc.createDataChannel("yudesk-session",new DataChannel.Init());createOffer(peer);}return peer;}

    private void createOffer(Peer peer){if(closed||peer.makingOffer)return;peer.makingOffer=true;peer.pc.createOffer(new Sdp(){@Override public void onCreateSuccess(SessionDescription sdp){peer.pc.setLocalDescription(new Sdp(){@Override public void onSetSuccess(){peer.makingOffer=false;sendSignal(peer.id,"offer",sdp.description,null);}@Override public void onSetFailure(String error){peer.makingOffer=false;connectionProblem(error);}},sdp);}@Override public void onCreateFailure(String error){peer.makingOffer=false;connectionProblem(error);}},new MediaConstraints());}
    private void setRemote(Peer peer,SessionDescription description){peer.pc.setRemoteDescription(new Sdp(){@Override public void onSetSuccess(){peer.remoteSet=true;for(IceCandidate candidate:peer.candidates)peer.pc.addIceCandidate(candidate);peer.candidates.clear();if(description.type==SessionDescription.Type.OFFER)createAnswer(peer);}@Override public void onSetFailure(String error){connectionProblem(error);}},description);}
    private void createAnswer(Peer peer){peer.pc.createAnswer(new Sdp(){@Override public void onCreateSuccess(SessionDescription sdp){peer.pc.setLocalDescription(new Sdp(){@Override public void onSetSuccess(){sendSignal(peer.id,"answer",sdp.description,null);}@Override public void onSetFailure(String error){connectionProblem(error);}},sdp);}@Override public void onCreateFailure(String error){connectionProblem(error);}},new MediaConstraints());}
    private void sendSignal(String to,String type,String sdp,IceCandidate candidate){try{JSONObject message=new JSONObject().put("type","signal").put("to",to).put("signal",type);if(sdp!=null)message.put("sdp",sdp);if(candidate!=null){JSONObject c=new JSONObject().put("sdpMid",candidate.sdpMid).put("sdpMLineIndex",candidate.sdpMLineIndex).put("candidate",candidate.sdp);message.put("candidate",c.toString());}send(message);}catch(Exception failure){connectionProblem(failure.getMessage());}}

    private void createMicrophone(){if(audioTrack!=null)return;audioSource=factory.createAudioSource(new MediaConstraints());audioTrack=factory.createAudioTrack("yudesk-audio",audioSource);audioTrack.setEnabled(true);}
    private void routeAudioToSpeaker(){audioManager.setMode(AudioManager.MODE_IN_COMMUNICATION);if(Build.VERSION.SDK_INT>=Build.VERSION_CODES.S){for(AudioDeviceInfo device:audioManager.getAvailableCommunicationDevices())if(device.getType()==AudioDeviceInfo.TYPE_BUILTIN_SPEAKER&&audioManager.setCommunicationDevice(device))return;}audioManager.setSpeakerphoneOn(true);}
    private void toggleSpeaker(){speakerEnabled=!speakerEnabled;for(Peer peer:peers.values())for(AudioTrack track:peer.remoteAudioTracks)track.setEnabled(speakerEnabled);if(speakerEnabled)routeAudioToSpeaker();render();notice(speakerEnabled?"会议声音已开启":"会议声音已关闭");}
    private void createCamera() throws Exception {if(cameraTrack!=null){cameraTrack.setEnabled(true);return;}CameraEnumerator enumerator=new Camera2Enumerator(activity);String selected=null;for(String id:enumerator.getDeviceNames())if(enumerator.isFrontFacing(id)){selected=id;break;}if(selected==null&&enumerator.getDeviceNames().length>0)selected=enumerator.getDeviceNames()[0];if(selected==null)throw new IllegalStateException("没有找到可用摄像头");frontCamera=enumerator.isFrontFacing(selected);cameraCapturer=enumerator.createCapturer(selected,null);if(cameraCapturer==null)throw new IllegalStateException("摄像头正在被其他应用使用");cameraSource=factory.createVideoSource(false);cameraTexture=SurfaceTextureHelper.create("YuDesk-camera",egl.getEglBaseContext());cameraCapturer.initialize(cameraTexture,activity,cameraSource.getCapturerObserver());cameraCapturer.startCapture(1280,720,24);cameraTrack=factory.createVideoTrack("yudesk-camera",cameraSource);cameraTrack.setEnabled(true);}

    private void toggleMicrophone(){if(closed)return;if(audioTrack==null&&activity.checkSelfPermission(Manifest.permission.RECORD_AUDIO)!=PackageManager.PERMISSION_GRANTED){activity.requestPermissions(new String[]{Manifest.permission.RECORD_AUDIO},REQUEST_MICROPHONE);return;}try{if(audioTrack==null){createMicrophone();for(Peer peer:peers.values()){peer.audioSender=peer.pc.addTrack(audioTrack,Collections.singletonList("yudesk"));createOffer(peer);}}else audioTrack.setEnabled(!audioTrack.enabled());local().microphone=audioTrack.enabled();sendState();render();}catch(Exception failure){notice("麦克风开启失败："+failure.getMessage());}}
    private void toggleCamera(){if(closed)return;if(cameraTrack==null&&activity.checkSelfPermission(Manifest.permission.CAMERA)!=PackageManager.PERMISSION_GRANTED){activity.requestPermissions(new String[]{Manifest.permission.CAMERA},REQUEST_CAMERA);return;}try{if(cameraTrack==null){createCamera();if(!screenSharing)replaceVideo(cameraTrack);presentation.attachVideo(selfID.isEmpty()?"local":selfID,cameraTrack,frontCamera);}else cameraTrack.setEnabled(!cameraTrack.enabled());local().camera=cameraTrack.enabled();sendState();render();}catch(Exception failure){notice("摄像头开启失败："+failure.getMessage());}}
    void mediaPermissionResult(int request,boolean granted){if(!granted){notice(request==REQUEST_MICROPHONE?"未获得麦克风权限":"未获得摄像头权限");return;}if(request==REQUEST_MICROPHONE)toggleMicrophone();else if(request==REQUEST_CAMERA)toggleCamera();}
    private void switchCamera(){if(cameraCapturer instanceof CameraVideoCapturer)((CameraVideoCapturer)cameraCapturer).switchCamera(new CameraVideoCapturer.CameraSwitchHandler(){@Override public void onCameraSwitchDone(boolean front){frontCamera=front;presentation.attachVideo(selfID,cameraTrack,front);}@Override public void onCameraSwitchError(String error){notice("切换摄像头失败："+error);}});}

    void startScreenShare(Intent result){if(closed||!isHost()||screenSharing)return;try{screenCapturer=new ScreenCapturerAndroid(result,new MediaProjection.Callback(){@Override public void onStop(){main.post(ConferenceController.this::stopScreenShare);}});screenSource=factory.createVideoSource(true);screenTexture=SurfaceTextureHelper.create("YuDesk-screen",egl.getEglBaseContext());screenCapturer.initialize(screenTexture,activity,screenSource.getCapturerObserver());DisplayMetrics metrics=new DisplayMetrics();activity.getWindowManager().getDefaultDisplay().getRealMetrics(metrics);int width=metrics.widthPixels,height=metrics.heightPixels;float scale=Math.min(1f,1280f/Math.max(width,height));screenCapturer.startCapture(Math.max(2,Math.round(width*scale)),Math.max(2,Math.round(height*scale)),20);screenTrack=factory.createVideoTrack("yudesk-screen",screenSource);screenSharing=true;replaceVideo(screenTrack);presentation.attachVideo(selfID,screenTrack,false);local().screen=true;sendState();render();}catch(Exception failure){stopScreenShare();notice("共享屏幕未开启："+failure.getMessage());}}
    void stopScreenShare(){if(!screenSharing&&screenCapturer==null)return;screenSharing=false;try{if(screenCapturer!=null)screenCapturer.stopCapture();}catch(Exception ignored){}if(screenTrack!=null)screenTrack.dispose();if(screenSource!=null)screenSource.dispose();if(screenCapturer!=null)screenCapturer.dispose();if(screenTexture!=null)screenTexture.dispose();screenTrack=null;screenSource=null;screenCapturer=null;screenTexture=null;replaceVideo(cameraTrack);presentation.attachVideo(selfID,cameraTrack,frontCamera);Participant local=local();if(local!=null)local.screen=false;sendState();render();stopProjectionServiceIfIdle();}
    private void replaceVideo(VideoTrack track){for(Peer peer:peers.values()){if(peer.videoSender!=null)peer.videoSender.setTrack(track,false);else if(track!=null){peer.videoSender=peer.pc.addTrack(track,Collections.singletonList("yudesk"));createOffer(peer);}}}

    void startRecording(Intent result){if(closed||!isHost()||recording)return;try{File dir=activity.getExternalFilesDir(Environment.DIRECTORY_MOVIES);if(dir==null)dir=activity.getFilesDir();if(!dir.exists()&&!dir.mkdirs())throw new IllegalStateException("无法创建录制目录");recordFile=new File(dir,"YuDesk-会议-"+code+"-"+System.currentTimeMillis()+".mp4");recorder=new MediaRecorder();DisplayMetrics metrics=new DisplayMetrics();activity.getWindowManager().getDefaultDisplay().getRealMetrics(metrics);int width=metrics.widthPixels,height=metrics.heightPixels;float scale=Math.min(1f,1280f/Math.max(width,height));width=Math.max(2,Math.round(width*scale)/2*2);height=Math.max(2,Math.round(height*scale)/2*2);recorder.setVideoSource(MediaRecorder.VideoSource.SURFACE);recorder.setOutputFormat(MediaRecorder.OutputFormat.MPEG_4);recorder.setVideoEncoder(MediaRecorder.VideoEncoder.H264);recorder.setVideoSize(width,height);recorder.setVideoFrameRate(20);recorder.setVideoEncodingBitRate(4_000_000);recorder.setOutputFile(recordFile.getAbsolutePath());recorder.prepare();recordProjection=((MediaProjectionManager)activity.getSystemService(Context.MEDIA_PROJECTION_SERVICE)).getMediaProjection(Activity.RESULT_OK,result);recordProjection.registerCallback(new MediaProjection.Callback(){@Override public void onStop(){main.post(()->stopRecording(true));}},main);recordDisplay=recordProjection.createVirtualDisplay("YuDesk meeting recording",width,height,metrics.densityDpi,DisplayManager.VIRTUAL_DISPLAY_FLAG_AUTO_MIRROR,recorder.getSurface(),null,main);recorder.start();recording=true;local().recording=true;sendState();render();}catch(Exception failure){stopRecording(false);notice("录制无法开始："+failure.getMessage());}}
    void stopRecording(boolean save){if(!recording&&recorder==null)return;recording=false;if(recordDisplay!=null){recordDisplay.release();recordDisplay=null;}if(recorder!=null){try{recorder.stop();}catch(Exception ignored){}recorder.reset();recorder.release();recorder=null;}if(recordProjection!=null){recordProjection.stop();recordProjection=null;}Participant local=local();if(local!=null)local.recording=false;sendState();render();File completed=recordFile;recordFile=null;if(save&&completed!=null&&completed.isFile()&&completed.length()>0)writer.execute(()->publishRecording(completed));else if(completed!=null&&!save)completed.delete();stopProjectionServiceIfIdle();}
    private void publishRecording(File source){String result=source.getAbsolutePath();if(Build.VERSION.SDK_INT>=29){ContentValues values=new ContentValues();values.put(MediaStore.Video.Media.DISPLAY_NAME,source.getName());values.put(MediaStore.Video.Media.MIME_TYPE,"video/mp4");values.put(MediaStore.Video.Media.RELATIVE_PATH,Environment.DIRECTORY_MOVIES+"/YuDesk");values.put(MediaStore.Video.Media.IS_PENDING,1);Uri uri=activity.getContentResolver().insert(MediaStore.Video.Media.EXTERNAL_CONTENT_URI,values);if(uri!=null)try(FileInputStream input=new FileInputStream(source);OutputStream output=activity.getContentResolver().openOutputStream(uri)){byte[] buffer=new byte[128*1024];for(int n;(n=input.read(buffer))>0;)output.write(buffer,0,n);values.clear();values.put(MediaStore.Video.Media.IS_PENDING,0);activity.getContentResolver().update(uri,values,null,null);source.delete();result="相册 / Movies / YuDesk";}catch(Exception ignored){activity.getContentResolver().delete(uri,null,null);}}String message=result;main.post(()->notice("录制已保存到 "+message));}
    private void stopProjectionServiceIfIdle(){if(!screenSharing&&!recording)activity.stopService(new Intent(activity,ConferenceProjectionService.class));}

    void transfer(String id){if(isHost()&&participants.containsKey(id)&&!id.equals(selfID))try{send(new JSONObject().put("type","transfer").put("to",id));}catch(Exception ignored){}}
    void leave(boolean endForAll){if(closed)return;intentional=true;if(isHost()&&endForAll){try{JSONObject end=new JSONObject().put("type","end");writer.execute(()->{try{channel.send(end.toString());}catch(Exception ignored){}main.post(()->finish("",false));});}catch(Exception failure){finish("",false);}}else finish("",false);}

    private void sendState(){Participant local=local();if(local==null||selfID.isEmpty())return;try{send(new JSONObject().put("type","state").put("microphone",local.microphone).put("camera",local.camera).put("screen",local.screen).put("recording",local.recording));}catch(Exception ignored){}}
    private void send(JSONObject message){if(closed)return;writer.execute(()->{try{channel.send(message.toString());}catch(Exception failure){main.post(()->{if(!closed)finish("会议信令发送失败，请重新加入",true);});}});}
    private Participant local(){return participants.get(selfID.isEmpty()?"local":selfID);}
    private void render(){Participant local=local();List<ConferenceUi.Member> views=new ArrayList<>();for(Participant p:participants.values()){p.host=p.id.equals(hostID)||(hostID.isEmpty()&&p==local&&requestedHost);ConferenceUi.Member view=p.view(p==local);views.add(view);presentation.upsert(view);}presentation.renderMembers(views,isHost());if(local!=null)presentation.setControls(local.microphone,speakerEnabled,local.camera,local.screen,local.recording,isHost());}
    private void removePeer(String id){Peer peer=peers.remove(id);if(peer!=null)peer.close();participants.remove(id);presentation.remove(id);render();}
    private void connectionProblem(String detail){presentation.setConnection("部分成员网络不稳定",true);}
    private void notice(String message){activity.conferenceNotice(message);}
    private String safeName(String value){value=value==null?"":value.trim();return value.isEmpty()?"参会者":value;}

    private void finish(String reason,boolean unexpected){if(closed)return;closed=true;ConferenceProjectionService.onStopped=null;for(Peer peer:new ArrayList<>(peers.values()))peer.close();peers.clear();if(screenSharing||screenCapturer!=null)stopScreenShare();if(recording||recorder!=null)stopRecording(false);channel.close();reader.shutdownNow();writer.shutdown();if(cameraCapturer!=null){try{cameraCapturer.stopCapture();}catch(Exception ignored){}cameraCapturer.dispose();}if(cameraTrack!=null)cameraTrack.dispose();if(cameraSource!=null)cameraSource.dispose();if(cameraTexture!=null)cameraTexture.dispose();if(audioTrack!=null)audioTrack.dispose();if(audioSource!=null)audioSource.dispose();presentation.release();factory.dispose();egl.release();audioManager.abandonAudioFocus(audioFocusListener);if(Build.VERSION.SDK_INT>=Build.VERSION_CODES.S){if(oldCommunicationDevice!=null)audioManager.setCommunicationDevice(oldCommunicationDevice);else audioManager.clearCommunicationDevice();}audioManager.setSpeakerphoneOn(oldSpeaker);audioManager.setMode(oldAudioMode);activity.onConferenceClosed(reason,unexpected&&!intentional);}

    private final class Peer implements PeerConnection.Observer {final String id;PeerConnection pc;RtpSender audioSender,videoSender;DataChannel dataChannel;boolean remoteSet,makingOffer;final List<IceCandidate> candidates=new ArrayList<>();final List<AudioTrack> remoteAudioTracks=new ArrayList<>();Peer(String id){this.id=id;}void addRemoteAudio(AudioTrack track){if(!remoteAudioTracks.contains(track))remoteAudioTracks.add(track);track.setEnabled(speakerEnabled);}void close(){for(AudioTrack track:remoteAudioTracks)track.setEnabled(false);remoteAudioTracks.clear();if(dataChannel!=null){dataChannel.close();dataChannel.dispose();dataChannel=null;}if(pc!=null){pc.close();pc.dispose();pc=null;}}
        @Override public void onSignalingChange(PeerConnection.SignalingState state){}
        @Override public void onIceConnectionChange(PeerConnection.IceConnectionState state){main.post(()->{if(state==PeerConnection.IceConnectionState.FAILED||state==PeerConnection.IceConnectionState.DISCONNECTED)presentation.setConnection("部分成员网络不稳定",true);else if(state==PeerConnection.IceConnectionState.CONNECTED||state==PeerConnection.IceConnectionState.COMPLETED)presentation.setConnection("已端到端加密",false);});}
        @Override public void onIceConnectionReceivingChange(boolean receiving){}
        @Override public void onIceGatheringChange(PeerConnection.IceGatheringState state){}
        @Override public void onIceCandidate(IceCandidate candidate){main.post(()->sendSignal(id,"candidate",null,candidate));}
        @Override public void onIceCandidatesRemoved(IceCandidate[] candidates){}
        @Override public void onAddStream(MediaStream stream){for(AudioTrack track:stream.audioTracks)addRemoteAudio(track);if(!stream.videoTracks.isEmpty()){VideoTrack track=stream.videoTracks.get(0);main.post(()->presentation.attachVideo(id,track,false));}}
        @Override public void onRemoveStream(MediaStream stream){}
        @Override public void onDataChannel(DataChannel channel){if(dataChannel!=null){dataChannel.close();dataChannel.dispose();}dataChannel=channel;}
        @Override public void onRenegotiationNeeded(){}
        @Override public void onAddTrack(RtpReceiver receiver,MediaStream[] streams){MediaStreamTrack track=receiver.track();if(track instanceof AudioTrack)addRemoteAudio((AudioTrack)track);else if(track instanceof VideoTrack)main.post(()->presentation.attachVideo(id,(VideoTrack)track,false));}
        @Override public void onTrack(RtpTransceiver transceiver){MediaStreamTrack track=transceiver.getReceiver().track();if(track instanceof AudioTrack)addRemoteAudio((AudioTrack)track);else if(track instanceof VideoTrack)main.post(()->presentation.attachVideo(id,(VideoTrack)track,false));}
    }
    private abstract static class Sdp implements SdpObserver {public void onCreateSuccess(SessionDescription description){}public void onSetSuccess(){}public void onCreateFailure(String error){}public void onSetFailure(String error){}}
    private static final class Participant {String id,name;boolean host,microphone,camera,screen,recording;Participant(String id,String name){this.id=id;this.name=name;}static Participant from(JSONObject value)throws Exception{Participant p=new Participant(value.getString("id"),value.optString("name","参会者"));p.host=value.optBoolean("host");p.microphone=value.optBoolean("microphone");p.camera=value.optBoolean("camera");p.screen=value.optBoolean("screen");p.recording=value.optBoolean("recording");return p;}ConferenceUi.Member view(boolean self){return new ConferenceUi.Member(id,name,self,host,microphone,camera,screen,recording);}}
}
