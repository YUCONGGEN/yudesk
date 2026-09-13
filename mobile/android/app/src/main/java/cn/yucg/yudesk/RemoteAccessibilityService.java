package cn.yucg.yudesk;

import android.accessibilityservice.AccessibilityService;
import android.accessibilityservice.GestureDescription;
import android.content.Intent;
import android.graphics.Path;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.util.DisplayMetrics;
import android.view.WindowManager;
import android.view.accessibility.AccessibilityEvent;
import android.view.accessibility.AccessibilityNodeInfo;
import cn.yucg.bridge.core.Engine;
import org.json.JSONArray;
import org.json.JSONObject;

public final class RemoteAccessibilityService extends AccessibilityService {
    private static volatile RemoteAccessibilityService instance;
    private final Handler main=new Handler(Looper.getMainLooper());
    private final InputQueue<Command> queue=new InputQueue<>(64,c->c.value.optString("type"),c->c.value.optInt("button"),(a,b)->a.w==b.w&&a.h==b.h);
    private GestureDescription.StrokeDescription stroke;
    private boolean busy,down,releaseRequested;
    private float x,y;
    private long epoch;
    private static final class Command { final JSONObject value; final int w,h; Command(JSONObject v,int width,int height){value=v;w=width;h=height;} }
    static boolean available(){return instance!=null;}
    static void receive(String raw){RemoteAccessibilityService s=instance;if(s!=null)s.accept(raw);}
    private YuDeskApp app(){return (YuDeskApp)getApplication();}
    private boolean permitted(){Engine e=app().existingEngine();if(instance!=this||e==null)return false;try{return e.canInput();}catch(Throwable failure){if(!FailureBoundary.recoverable(failure))throw (Error)failure;return false;}}
    @Override protected void onServiceConnected(){instance=this;Engine e=app().existingEngine();if(e!=null)FailureBoundary.runQuietly(()->e.setAccessibility(true));}
    @Override public void onAccessibilityEvent(AccessibilityEvent event){/* Never collect UI events or window text. */}
    @Override public void onInterrupt(){clear();}
    @Override public boolean onUnbind(Intent intent){instance=null;Engine e=app().existingEngine();if(e!=null)FailureBoundary.runQuietly(()->e.setAccessibility(false));clear();return super.onUnbind(intent);}
    @Override public void onDestroy(){instance=null;Engine e=app().existingEngine();if(e!=null)FailureBoundary.runQuietly(()->e.setAccessibility(false));clear();super.onDestroy();}
    private void clear(){epoch++;queue.clear();stroke=null;down=false;busy=false;releaseRequested=false;main.removeCallbacksAndMessages(null);}
    // Called on the application's main thread by the capture service's single
    // bounded input pump. Re-check OS service state and session authorization.
    private void accept(String raw){
        try{JSONObject b=new JSONObject(raw);if(b.optBoolean("release")){finishContact();return;}if(!permitted()){clear();return;}
            JSONArray events=b.getJSONArray("events");
            for(int i=0;i<events.length();i++){if(!queue.offer(new Command(events.getJSONObject(i),b.getInt("width"),b.getInt("height")))){finishContact();failure("触控队列已满，请重试");return;}}
            pump();
        }catch(Exception ex){finishContact();failure("输入事件无法处理");}
    }
    private void finishContact(){
        queue.clear();releaseRequested=true;if(!busy)releaseOwnedContact();
    }
    private void releaseOwnedContact(){
        // Releasing our existing contact remains necessary after Go revokes
        // session permissions. It never begins a new gesture at a new location.
        if(stroke!=null&&down&&instance==this){try{Path p=new Path();p.moveTo(x,y);GestureDescription.StrokeDescription end=stroke.continueStroke(p,0,1,false);dispatchGesture(new GestureDescription.Builder().addStroke(end).build(),null,null);}catch(Exception ignored){}}
        clear();
    }
    private void failure(String message){app().notice=message;Engine e=app().existingEngine();if(e!=null)FailureBoundary.runQuietly(()->e.reportInputError(message));}
    private void pump(){
        if(busy||queue.isEmpty())return;if(!permitted()){clear();return;}
        Command command=queue.removeFirst();JSONObject event=command.value;String type=event.optString("type");
        DisplayMetrics metrics=new DisplayMetrics();((WindowManager)getSystemService(WINDOW_SERVICE)).getDefaultDisplay().getRealMetrics(metrics);
        float nx=Math.max(0,Math.min(metrics.widthPixels-1,event.optInt("x")*(float)metrics.widthPixels/Math.max(1,command.w)));
        float ny=Math.max(0,Math.min(metrics.heightPixels-1,event.optInt("y")*(float)metrics.heightPixels/Math.max(1,command.h)));
        try {
            if(type.equals("down")||type.equals("up")||type.equals("move")){
                if(event.optInt("button",1)>1){if(type.equals("up")&&!performGlobalAction(GLOBAL_ACTION_BACK))failure("系统未接受返回操作");pump();return;}
                if(type.equals("move")&&!down){pump();return;}
                if(type.equals("up")&&!down){pump();return;}
                Path path=new Path();GestureDescription.StrokeDescription next;
                if(type.equals("down")){if(down){finishContact();return;}path.moveTo(nx,ny);next=new GestureDescription.StrokeDescription(path,0,1,true);down=true;}
                else {path.moveTo(x,y);path.lineTo(nx,ny);next=stroke.continueStroke(path,0,type.equals("move")?16:1,!type.equals("up"));if(type.equals("up"))down=false;}
                x=nx;y=ny;stroke=next;dispatch(next);return;
            }
            if(type.equals("wheel")){
                int delta=event.optInt("deltaY");if(delta!=0){Path p=new Path();float cx=metrics.widthPixels*.5f,cy=metrics.heightPixels*.5f;p.moveTo(cx,cy);p.lineTo(cx,cy+(delta>0?-1:1)*metrics.heightPixels*.25f);dispatch(new GestureDescription.StrokeDescription(p,0,140));return;}
            }else if(type.equals("key_down")){
                String key=event.optString("key");if(key.equals("Escape")||key.equals("BrowserBack")){if(!performGlobalAction(GLOBAL_ACTION_BACK))failure("返回操作未被系统接受");}
                else if(key.equals("Home")){if(!performGlobalAction(GLOBAL_ACTION_HOME))failure("主页操作未被系统接受");}
                else if(key.equals("AppSwitch")){performGlobalAction(GLOBAL_ACTION_RECENTS);}
                else failure("该 Android 版本暂不支持此物理按键，请使用文字输入或屏幕键盘");
            }else if(type.equals("text")){
                setText(event.optString("text"));
            }
        }catch(Exception ex){finishContact();failure("系统拒绝触控操作，请检查权限或重新连接");}
        if(!queue.isEmpty())main.post(this::pump);
    }
    private void dispatch(GestureDescription.StrokeDescription next){
        if(!permitted()){clear();return;}busy=true;long generation=epoch;
        boolean accepted=dispatchGesture(new GestureDescription.Builder().addStroke(next).build(),new GestureResultCallback(){
            @Override public void onCompleted(GestureDescription d){if(generation!=epoch)return;busy=false;if(!down)stroke=null;if(releaseRequested){releaseOwnedContact();return;}pump();}
            @Override public void onCancelled(GestureDescription d){if(generation!=epoch)return;clear();failure("手势被系统或本机触摸取消，可以重新操作。");}
        },main);
        if(!accepted){clear();failure("系统未接受手势，请重新开启触控权限");}
    }
    private void setText(String value){
        AccessibilityNodeInfo root=getRootInActiveWindow();if(root==null){failure("当前窗口不支持远程文本");return;}
        AccessibilityNodeInfo focus=null;
        try{focus=root.findFocus(AccessibilityNodeInfo.FOCUS_INPUT);if(focus==null||!focus.isEditable()||focus.isPassword()){failure("请先点选普通文本框；不自动填充密码框");return;}
            // ACTION_SET_TEXT changes only the focused, explicitly editable field.
            CharSequence previous=focus.getText();String old=previous==null?"":previous.toString();int start=focus.getTextSelectionStart(),end=focus.getTextSelectionEnd();if(start<0||end<start||end>old.length()){start=old.length();end=start;}
            String updated=old.substring(0,start)+value+old.substring(end);if(updated.length()>65536){failure("文本超过安全长度");return;}
            Bundle args=new Bundle();args.putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE,updated);
            if(!permitted()||!focus.performAction(AccessibilityNodeInfo.ACTION_SET_TEXT,args))failure("此输入框不接受远程文本");
        }finally{if(focus!=null)focus.recycle();root.recycle();}
    }
}
