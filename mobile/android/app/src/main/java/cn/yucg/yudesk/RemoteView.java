package cn.yucg.yudesk;

import android.content.Context;
import android.annotation.SuppressLint;
import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.graphics.Canvas;
import android.graphics.Color;
import android.graphics.Paint;
import android.graphics.RectF;
import android.os.SystemClock;
import android.view.MotionEvent;
import android.view.View;
import cn.yucg.bridge.core.Frame;
import cn.yucg.bridge.core.Session;
import org.json.JSONArray;
import org.json.JSONObject;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.function.Consumer;

// Created only with a live authenticated Session; never inflated from XML.
@SuppressLint("ViewConstructor")
final class RemoteView extends View {
    private final Session session;
    private final Consumer<String> error;
    private final Paint paint=new Paint(Paint.ANTI_ALIAS_FLAG|Paint.FILTER_BITMAP_FLAG);
    private final RectF frameRect=new RectF();
    private final FrameSlot<Bitmap> frames=new FrameSlot<>(Bitmap::recycle);
    private ScreenMapping mapping;
    private volatile boolean stopped,ready;
    private Thread decoder;
    private boolean pressed;
    private long lastMove, lastError;

    RemoteView(Context context,Session value,Consumer<String> errors){super(context);session=value;error=errors;setBackgroundColor(0xff0c1420);setContentDescription("远程画面，触摸执行远程点击和拖动");}
    void start(){decoder=new Thread(()->{
        long revision=0;
        while(!stopped){try{Frame f=session.nextFrame(revision,500);if(f==null)continue;revision=f.getRevision();byte[] bytes=f.getData();Bitmap decoded=BitmapFactory.decodeByteArray(bytes,0,bytes.length);if(decoded==null)throw new Exception("无法解码远程画面");
            CountDownLatch drawn=new CountDownLatch(1);
            if(!post(()->{try{if(stopped){frames.discardUnpublished(decoded);return;}if(!frames.publish(decoded))return;mapping=new ScreenMapping(getWidth(),getHeight(),decoded.getWidth(),decoded.getHeight());ready=true;invalidate();}finally{drawn.countDown();}})){frames.discardUnpublished(decoded);break;}
            // No chain of queued native images when the UI cannot consume them.
            while(!stopped&&!drawn.await(500,TimeUnit.MILLISECONDS)){}
        }catch(Exception ex){if(!stopped)post(()->notice(ex.getMessage()));break;}}
    },"YuDesk-render");decoder.start();}
    void stop(){stopped=true;ready=false;session.releaseInput();if(decoder!=null)decoder.interrupt();frames.close();invalidate();}
    void setReady(boolean value){if(ready!=value){ready=value;invalidate();}}
    @Override protected void onSizeChanged(int w,int h,int oldw,int oldh){Bitmap bitmap=frames.current();if(bitmap!=null)mapping=new ScreenMapping(w,h,bitmap.getWidth(),bitmap.getHeight());}
    @Override protected void onDraw(Canvas c){super.onDraw(c);Bitmap bitmap=frames.current();if(ready&&bitmap!=null&&mapping!=null){frameRect.set(mapping.left,mapping.top,mapping.left+mapping.width,mapping.top+mapping.height);c.drawBitmap(bitmap,null,frameRect,paint);}else{paint.setColor(0xff91a6c3);paint.setTextSize(16*getResources().getDisplayMetrics().scaledDensity);paint.setTextAlign(Paint.Align.CENTER);c.drawText("等待远端画面…",getWidth()/2f,getHeight()/2f,paint);}}
    @Override protected void onDetachedFromWindow(){stop();super.onDetachedFromWindow();}
    void send(JSONArray events)throws Exception{if(mapping==null||!ready)throw new Exception("画面尚未就绪");session.sendInputJSON(new JSONObject().put("events",events).put("width",mapping.sourceWidth).put("height",mapping.sourceHeight).toString());}
    private void pointer(String type,float x,float y)throws Exception{send(new JSONArray().put(new JSONObject().put("type",type).put("x",mapping.x(x)).put("y",mapping.y(y)).put("button",1)));}
    private void notice(String message){long now=SystemClock.elapsedRealtime();if(now-lastError>3000){lastError=now;error.accept(message);}}
    @Override public boolean onTouchEvent(MotionEvent event){
        if(!ready||mapping==null)return true;
        try{switch(event.getActionMasked()){
            case MotionEvent.ACTION_DOWN:if(!mapping.contains(event.getX(),event.getY()))return true;pressed=true;getParent().requestDisallowInterceptTouchEvent(true);pointer("down",event.getX(),event.getY());break;
            case MotionEvent.ACTION_MOVE:if(pressed&&SystemClock.elapsedRealtime()-lastMove>=16){lastMove=SystemClock.elapsedRealtime();pointer("move",event.getX(),event.getY());}break;
            case MotionEvent.ACTION_UP:if(pressed){pointer("up",event.getX(),event.getY());performClick();}pressed=false;getParent().requestDisallowInterceptTouchEvent(false);break;
            case MotionEvent.ACTION_CANCEL:pressed=false;session.releaseInput();getParent().requestDisallowInterceptTouchEvent(false);break;
            case MotionEvent.ACTION_POINTER_DOWN:pressed=false;session.releaseInput();notice("预览版支持单指操作，多点触控暂不可用。");break;
        }}catch(Exception ex){pressed=false;session.releaseInput();notice(ex.getMessage());}return true;
    }
    @Override public boolean performClick(){super.performClick();return true;}
}
