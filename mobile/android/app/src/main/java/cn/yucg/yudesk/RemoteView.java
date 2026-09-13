package cn.yucg.yudesk;

import android.content.Context;
import android.annotation.SuppressLint;
import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.graphics.Canvas;
import android.graphics.Color;
import android.graphics.Paint;
import android.graphics.Path;
import android.graphics.RectF;
import android.os.SystemClock;
import android.view.MotionEvent;
import android.view.View;
import android.view.ViewConfiguration;
import cn.yucg.bridge.core.Frame;
import cn.yucg.bridge.core.Session;
import org.json.JSONArray;
import org.json.JSONObject;
import java.util.concurrent.atomic.AtomicBoolean;
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
    private final PointerController pointer;
    private final Path arrow=new Path();
    private final Paint cursorPaint=new Paint(Paint.ANTI_ALIAS_FLAG);
    private boolean control=true,multi;
    private float scrollY;
    private int scrollA=-1,scrollB=-1;
    private long lastError;
    private Runnable inputChanged=()->{};
    private final AtomicBoolean terminalDelivered=new AtomicBoolean();

    RemoteView(Context context,Session value,Consumer<String> errors){
        super(context);session=value;error=errors;setBackgroundColor(0xff0c1420);
        pointer=new PointerController(new PointerController.Sink(){
            @Override public void send(PointerController.Event... events)throws Exception{
                JSONArray batch=new JSONArray();for(PointerController.Event e:events)batch.put(new JSONObject().put("type",e.type).put("x",e.x).put("y",e.y).put("button",e.button).put("deltaY",e.deltaY));RemoteView.this.send(batch);
            }
            @Override public void release(){FailureBoundary.runQuietly(session::releaseInput);}
        },ViewConfiguration.get(context).getScaledTouchSlop());
        arrow.moveTo(0,0);arrow.lineTo(0,20);arrow.lineTo(5,15);arrow.lineTo(9,23);arrow.lineTo(13,21);arrow.lineTo(9,13);arrow.lineTo(16,13);arrow.close();
        setContentDescription("远程鼠标：单指移动，轻点左击，双指上下滚动；工具栏可右击或拖动");
    }
    void start(){if(decoder!=null||stopped)return;decoder=new Thread(()->{
        long revision=0;
        while(!stopped){try{Frame f=session.nextFrame(revision,500);if(f==null)continue;revision=f.getRevision();byte[] bytes=f.getData();Bitmap decoded=BitmapFactory.decodeByteArray(bytes,0,bytes.length);if(decoded==null)throw new Exception("无法解码远程画面");
            // At most one pending bitmap and one UI callback. A stalled UI
            // receives the newest completed decode when it resumes.
            if(frames.offer(decoded)&&!post(this::publishFrame)){frames.close();break;}
        }catch(Throwable failure){if(!FailureBoundary.recoverable(failure))throw (Error)failure;if(!stopped&&terminalDelivered.compareAndSet(false,true)){String message=FailureBoundary.message(failure,"远程连接已断开");post(()->{if(!stopped)error.accept(message);});}break;}}
    },"YuDesk-render");decoder.start();}
    private void publishFrame(){if(stopped||!frames.publishPending())return;Bitmap bitmap=frames.current();mapping=new ScreenMapping(getWidth(),getHeight(),bitmap.getWidth(),bitmap.getHeight());pointer.geometry(mapping);ready=true;invalidate();}
    void stop(){if(stopped)return;cancelInput();stopped=true;ready=false;if(decoder!=null)decoder.interrupt();frames.close();invalidate();}
    void setReady(boolean value){if(!value&&ready)cancelInput();if(ready!=value){ready=value;invalidate();}}
    void setControl(boolean value){if(control&&!value)cancelInput();control=value;invalidate();}
    void onInputChanged(Runnable listener){inputChanged=listener;}
    boolean mouseMode(){return pointer.relative();}
    boolean dragging(){return pointer.dragging();}
    void mouseMode(boolean value){cancelInput();pointer.mode(value);setContentDescription(value?"远程鼠标：单指移动，轻点左击，双指滚动":"远程触屏：点击位置直接操作，滑动拖动");inputChanged.run();invalidate();}
    void toggleDrag(){if(!control||!ready)return;boolean next=!pointer.dragging();cancelInput();pointer.drag(next);inputChanged.run();}
    void click(int button){if(!control||!ready)return;try{pointer.click(button);invalidate();}catch(Exception ex){cancelInput();notice(ex.getMessage());}}
    void wheel(int direction){if(!control||!ready)return;try{pointer.scroll(direction*24f);}catch(Exception ex){cancelInput();notice(ex.getMessage());}}
    void cancelInput(){pointer.cancel();multi=false;scrollA=scrollB=-1;inputChanged.run();}
    @Override protected void onSizeChanged(int w,int h,int oldw,int oldh){cancelInput();Bitmap bitmap=frames.current();if(bitmap!=null){mapping=new ScreenMapping(w,h,bitmap.getWidth(),bitmap.getHeight());pointer.geometry(mapping);}}
    @Override protected void onDraw(Canvas c){super.onDraw(c);Bitmap bitmap=frames.current();if(ready&&bitmap!=null&&mapping!=null){frameRect.set(mapping.left,mapping.top,mapping.left+mapping.width,mapping.top+mapping.height);c.drawBitmap(bitmap,null,frameRect,paint);
        if(control&&pointer.relative()){int save=c.save();c.clipRect(frameRect);c.translate(mapping.viewX(pointer.x()),mapping.viewY(pointer.y()));float density=getResources().getDisplayMetrics().density;c.scale(density,density);cursorPaint.setStyle(Paint.Style.FILL);cursorPaint.setColor(Color.WHITE);c.drawPath(arrow,cursorPaint);cursorPaint.setStyle(Paint.Style.STROKE);cursorPaint.setStrokeWidth(1.2f);cursorPaint.setColor(0xff1474e8);c.drawPath(arrow,cursorPaint);c.restoreToCount(save);}
    }else{paint.setColor(0xff91a6c3);paint.setTextSize(16*getResources().getDisplayMetrics().scaledDensity);paint.setTextAlign(Paint.Align.CENTER);c.drawText("等待远端画面…",getWidth()/2f,getHeight()/2f,paint);}}
    @Override protected void onDetachedFromWindow(){stop();super.onDetachedFromWindow();}
    void send(JSONArray events)throws Exception{if(!control)throw new Exception("当前为仅观看模式，对方未授权操作");if(mapping==null||!ready||stopped)throw new Exception("画面尚未就绪");session.sendInputJSON(new JSONObject().put("events",events).put("width",mapping.sourceWidth).put("height",mapping.sourceHeight).toString());}
    private void notice(String message){long now=SystemClock.elapsedRealtime();if(now-lastError>3000){lastError=now;error.accept(message);}}
    private void moveHistory(MotionEvent event)throws Exception{
        if(pointer.holdingButton())for(int i=0;i<event.getHistorySize();i++)pointer.move(event.getHistoricalX(i),event.getHistoricalY(i),event.getHistoricalEventTime(i));
    }
    @Override public boolean onTouchEvent(MotionEvent event){
        if(!control||!ready||mapping==null||stopped)return true;
        try{switch(event.getActionMasked()){
            case MotionEvent.ACTION_DOWN:multi=false;pointer.down(event.getX(),event.getY(),event.getEventTime());getParent().requestDisallowInterceptTouchEvent(true);break;
            case MotionEvent.ACTION_MOVE:
                if(multi){int a=event.findPointerIndex(scrollA),b=event.findPointerIndex(scrollB);if(pointer.relative()&&a>=0&&b>=0){float center=(event.getY(a)+event.getY(b))/2;pointer.scroll((center-scrollY)/getResources().getDisplayMetrics().density);scrollY=center;}}
                else{moveHistory(event);pointer.move(event.getX(),event.getY(),event.getEventTime());}break;
            case MotionEvent.ACTION_UP:if(!multi){moveHistory(event);pointer.up(event.getX(),event.getY(),event.getEventTime());performClick();}else cancelInput();multi=false;getParent().requestDisallowInterceptTouchEvent(false);inputChanged.run();break;
            case MotionEvent.ACTION_CANCEL:cancelInput();getParent().requestDisallowInterceptTouchEvent(false);break;
            case MotionEvent.ACTION_POINTER_DOWN:cancelInput();multi=true;scrollA=event.getPointerId(0);scrollB=event.getPointerId(1);scrollY=(event.getY(0)+event.getY(1))/2;break;
            case MotionEvent.ACTION_POINTER_UP:scrollA=scrollB=-1;break; // Wait for all fingers to lift; never turn a scroll into a click.
        }invalidate();}catch(Exception ex){cancelInput();notice(ex.getMessage());}return true;
    }
    @Override public boolean performClick(){super.performClick();return true;}
}
