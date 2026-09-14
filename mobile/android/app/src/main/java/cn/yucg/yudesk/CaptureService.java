package cn.yucg.yudesk;

import android.app.*;
import android.content.*;
import android.content.pm.ServiceInfo;
import android.graphics.*;
import android.hardware.display.DisplayManager;
import android.hardware.display.VirtualDisplay;
import android.media.Image;
import android.media.ImageReader;
import android.media.projection.MediaProjection;
import android.media.projection.MediaProjectionManager;
import android.os.*;
import android.util.DisplayMetrics;
import android.view.WindowManager;
import cn.yucg.bridge.core.Engine;
import org.json.JSONObject;
import java.io.ByteArrayOutputStream;
import java.nio.ByteBuffer;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

public final class CaptureService extends Service {
    private static final String CAPTURE="yudesk-sharing", REQUEST="yudesk-requests", STOP="cn.yucg.yudesk.STOP";
    private final Handler main=new Handler(Looper.getMainLooper());
    private HandlerThread captureThread;
    private Handler capture;
    private Thread inputThread;
    private MediaProjection projection;
    private VirtualDisplay display;
    private ImageReader reader;
    private Bitmap padded,frame;
    private ByteBuffer compactPixels;
    private final Canvas canvas=new Canvas();
    private final Paint paint=new Paint(Paint.FILTER_BITMAP_FLAG);
    private final ByteArrayOutputStream jpeg=new ByteArrayOutputStream(256*1024);
    private Engine engine;
    private YuDeskApp app;
    private volatile boolean stopping;
    private boolean registered,displayListenerRegistered;
    private int width,height,density;
    private final CapturePacer pacer=new CapturePacer(33);
    private final Runnable captureLatest=()->{pacer.begin();if(reader!=null&&!stopping)image(reader);};
    private String lastRequest="";
    private final BroadcastReceiver screenOff=new BroadcastReceiver(){@Override public void onReceive(Context c,Intent i){app.notice="手机已锁屏，屏幕共享已停止；解锁后需要重新授权。";stopSelf();}};
    private final DisplayManager.DisplayListener displayChanges=new DisplayManager.DisplayListener(){
        @Override public void onDisplayAdded(int id){}
        @Override public void onDisplayRemoved(int id){}
        @Override public void onDisplayChanged(int id){if(Build.VERSION.SDK_INT<34&&id==android.view.Display.DEFAULT_DISPLAY){DisplayMetrics m=metrics();resize(m.widthPixels,m.heightPixels);}}
    };
    @Override public void onCreate(){super.onCreate();MulticastLease.acquire(this);app=(YuDeskApp)getApplication();NotificationManager nm=getSystemService(NotificationManager.class);nm.createNotificationChannel(new NotificationChannel(CAPTURE,"屏幕共享状态",NotificationManager.IMPORTANCE_LOW));nm.createNotificationChannel(new NotificationChannel(REQUEST,"远程连接请求",NotificationManager.IMPORTANCE_HIGH));}
    private PendingIntent open(){return PendingIntent.getActivity(this,1,new Intent(this,MainActivity.class).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),PendingIntent.FLAG_UPDATE_CURRENT|PendingIntent.FLAG_IMMUTABLE);}
    private Notification notification(){PendingIntent stop=PendingIntent.getService(this,2,new Intent(this,CaptureService.class).setAction(STOP),PendingIntent.FLAG_UPDATE_CURRENT|PendingIntent.FLAG_IMMUTABLE);return new Notification.Builder(this,CAPTURE).setSmallIcon(R.drawable.ic_notification).setContentTitle("YuDesk · 屏幕共享已授权").setContentText("获准连接后发送屏幕；点击查看，随时停止。").setContentIntent(open()).setOngoing(true).addAction(new Notification.Action.Builder(null,"停止共享",stop).build()).build();}
    @Override public int onStartCommand(Intent intent,int flags,int startId){
        if(intent==null||STOP.equals(intent.getAction())){stopSelf();return START_NOT_STICKY;}
        if(projection!=null){app.sharingStarting=false;return START_NOT_STICKY;}
        try{
            if(Build.VERSION.SDK_INT>=29)Api29.startForeground(this,notification());else startForeground(7,notification());
            Intent result=Build.VERSION.SDK_INT>=33?Api33.projection(intent):intent.getParcelableExtra("projection");
            if(intent.getIntExtra("result",Activity.RESULT_CANCELED)!=Activity.RESULT_OK||result==null){stopSelf();return START_NOT_STICKY;}
            engine=app.engine();if(!app.state().optBoolean("running"))throw new IllegalStateException("设备已被服务器停止，请处理授权状态后重新打开");
            captureThread=new HandlerThread("YuDesk-capture",android.os.Process.THREAD_PRIORITY_DISPLAY);captureThread.start();capture=new Handler(captureThread.getLooper());
            projection=((MediaProjectionManager)getSystemService(MEDIA_PROJECTION_SERVICE)).getMediaProjection(Activity.RESULT_OK,result);
            projection.registerCallback(new MediaProjection.Callback(){
                @Override public void onStop(){main.post(()->{app.notice="系统已结束屏幕共享，如需继续请重新授权。";stopSelf();});}
                @Override public void onCapturedContentResize(int w,int h){resize(w,h);}
            },capture);
            DisplayMetrics metrics=metrics();density=metrics.densityDpi;
            capture.post(()->{try{configureReader(metrics.widthPixels,metrics.heightPixels);display=projection.createVirtualDisplay("YuDesk authorized sharing",width,height,density,DisplayManager.VIRTUAL_DISPLAY_FLAG_AUTO_MIRROR,reader.getSurface(),null,capture);if(display==null)throw new IllegalStateException("无法创建共享显示器");engine.setSharing(true);app.sharing=true;app.sharingStarting=false;}catch(Throwable failure){if(!FailureBoundary.recoverable(failure))throw (Error)failure;fail("无法开始共享："+FailureBoundary.message(failure,"设备不支持当前共享方式"));}});
            getSystemService(DisplayManager.class).registerDisplayListener(displayChanges,capture);
            displayListenerRegistered=true;
            if(Build.VERSION.SDK_INT>=33)registerReceiver(screenOff,new IntentFilter(Intent.ACTION_SCREEN_OFF),Context.RECEIVER_NOT_EXPORTED);else registerReceiver(screenOff,new IntentFilter(Intent.ACTION_SCREEN_OFF));registered=true;
            main.post(statusTick);startInputPump();
        }catch(Throwable failure){if(!FailureBoundary.recoverable(failure))throw (Error)failure;fail("屏幕共享启动失败："+FailureBoundary.message(failure,"请重新授权后重试"));}
        return START_NOT_STICKY;
    }
    private DisplayMetrics metrics(){DisplayMetrics m=new DisplayMetrics();((WindowManager)getSystemService(WINDOW_SERVICE)).getDefaultDisplay().getRealMetrics(m);return m;}
    private void configureReader(int sourceWidth,int sourceHeight){
        float scale=Math.min(1f,1280f/Math.max(sourceWidth,sourceHeight));width=Math.max(2,Math.round(sourceWidth*scale));height=Math.max(2,Math.round(sourceHeight*scale));
        reader=ImageReader.newInstance(width,height,PixelFormat.RGBA_8888,2);reader.setOnImageAvailableListener(source->{
            if(source!=reader||stopping)return;
            long delay=pacer.schedule(SystemClock.elapsedRealtime());
            if(delay>=0)capture.postDelayed(captureLatest,delay);
        },capture);
    }
    private void resize(int w,int h){if(stopping||projection==null||display==null||w<1||h<1)return;float scale=Math.min(1f,1280f/Math.max(w,h));if(Math.max(2,Math.round(w*scale))==width&&Math.max(2,Math.round(h*scale))==height)return;
        try{capture.removeCallbacks(captureLatest);pacer.reset();display.setSurface(null);if(reader!=null)reader.close();configureReader(w,h);display.resize(width,height,density);display.setSurface(reader.getSurface());recycle();}catch(Exception ex){fail("屏幕尺寸改变后共享已停止，请重新授权");}
    }
    private void image(ImageReader source){
        if(source!=reader||stopping)return;
        try(Image image=source.acquireLatestImage()){
            if(image==null||stopping||engine==null||!engine.wantsFrame())return;pacer.captured(SystemClock.elapsedRealtime());
            Image.Plane plane=image.getPlanes()[0];int stride=plane.getPixelStride(),row=plane.getRowStride();if(stride!=4||row%4!=0)throw new IllegalStateException("设备不支持 RGBA 屏幕输出");int paddedWidth=row/4;
            if(padded==null||padded.getWidth()!=paddedWidth||padded.getHeight()!=height){recycle();padded=Bitmap.createBitmap(paddedWidth,height,Bitmap.Config.ARGB_8888);frame=Bitmap.createBitmap(width,height,Bitmap.Config.ARGB_8888);}
            ByteBuffer buffer=plane.getBuffer();buffer.rewind();
            if(buffer.remaining()>=row*height){padded.copyPixelsFromBuffer(buffer);canvas.setBitmap(frame);canvas.drawBitmap(padded,0,0,paint);}
            else{
                // Some vendors omit padding after the final row. Copy only real
                // pixels instead of overflowing that Image plane's buffer.
                int bytes=width*height*4;if(compactPixels==null||compactPixels.capacity()!=bytes)compactPixels=ByteBuffer.allocateDirect(bytes);compactPixels.clear();
                for(int line=0;line<height;line++){ByteBuffer slice=buffer.duplicate();slice.position(line*row);slice.limit(line*row+width*4);compactPixels.put(slice);}
                compactPixels.flip();frame.copyPixelsFromBuffer(compactPixels);
            }
            jpeg.reset();if(!frame.compress(Bitmap.CompressFormat.JPEG,75,jpeg))throw new IllegalStateException("JPEG 编码失败");engine.submitJPEG(jpeg.toByteArray(),width,height);
        }catch(Throwable failure){if(!FailureBoundary.recoverable(failure))throw (Error)failure;if(!stopping)fail("屏幕采集失败："+FailureBoundary.message(failure,"采集组件已停止"));}
    }
    private void recycle(){canvas.setBitmap(null);compactPixels=null;if(padded!=null){padded.recycle();padded=null;}if(frame!=null){frame.recycle();frame=null;}}
    private void fail(String message){if(stopping)return;app.notice=message;main.post(this::stopSelf);}
    private final Runnable statusTick=new Runnable(){@Override public void run(){if(stopping)return;JSONObject state=app.state();if(!state.optBoolean("running",true)){app.notice=state.optString("message");stopSelf();return;}try{String raw=engine.pendingApprovalJSON();if(raw.equals("null")){getSystemService(NotificationManager.class).cancel(8);lastRequest="";}else{JSONObject p=new JSONObject(raw);String id=p.getString("id");if(!id.equals(lastRequest)){lastRequest=id;Notification request=new Notification.Builder(CaptureService.this,REQUEST).setSmallIcon(R.drawable.ic_notification).setContentTitle("YuDesk · 请求"+(p.optString("mode").equals("control")?"控制手机":"观看手机")).setContentText("点击选择允许或拒绝，60 秒超时自动拒绝。").setContentIntent(open()).setAutoCancel(true).setTimeoutAfter(60000).build();getSystemService(NotificationManager.class).notify(8,request);}}}catch(Exception ignored){}main.postDelayed(this,300);}};
    private void startInputPump(){inputThread=new Thread(()->{try{while(!stopping){String message=engine.nextInputJSON(500);if(message==null||message.isEmpty())continue;CountDownLatch accepted=new CountDownLatch(1);main.post(()->{try{if(!stopping)RemoteAccessibilityService.receive(message);}finally{accepted.countDown();}});while(!stopping&&!accepted.await(500,TimeUnit.MILLISECONDS)){} }}catch(InterruptedException interrupted){Thread.currentThread().interrupt();}catch(Throwable failure){if(!FailureBoundary.recoverable(failure))throw (Error)failure;if(!stopping)fail("远程连接已断开，请检查网络后重新连接。");}},"YuDesk-input");inputThread.start();}
    @Override public void onTaskRemoved(Intent rootIntent){stopSelf();}
    @Override public void onDestroy(){
        stopping=true;app.sharing=false;app.sharingStarting=false;main.removeCallbacksAndMessages(null);if(inputThread!=null)inputThread.interrupt();
        if(engine!=null)FailureBoundary.runQuietly(()->engine.setSharing(false));RemoteAccessibilityService.receive("{\"release\":true}");
        if(registered){FailureBoundary.runQuietly(()->unregisterReceiver(screenOff));registered=false;}
        if(displayListenerRegistered){FailureBoundary.runQuietly(()->getSystemService(DisplayManager.class).unregisterDisplayListener(displayChanges));displayListenerRegistered=false;}
        getSystemService(NotificationManager.class).cancel(8);
        if(capture!=null){capture.removeCallbacksAndMessages(null);capture.post(()->{if(display!=null){FailureBoundary.runQuietly(display::release);display=null;}if(reader!=null){FailureBoundary.runQuietly(reader::close);reader=null;}if(projection!=null){FailureBoundary.runQuietly(projection::stop);projection=null;}recycle();if(captureThread!=null)captureThread.quitSafely();});}
        stopForeground(STOP_FOREGROUND_REMOVE);MulticastLease.release();super.onDestroy();
    }
    @Override public IBinder onBind(Intent intent){return null;}

    @android.annotation.TargetApi(29)
    @android.annotation.SuppressLint("UseRequiresApi")
    private static final class Api29 {static void startForeground(Service service,Notification notification){service.startForeground(7,notification,ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PROJECTION);}}
    @android.annotation.TargetApi(33)
    @android.annotation.SuppressLint("UseRequiresApi")
    private static final class Api33 {static Intent projection(Intent intent){return intent.getParcelableExtra("projection",Intent.class);}}
}
