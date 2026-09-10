package cn.yucg.yudesk;

import android.Manifest;
import android.app.Activity;
import android.app.Dialog;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.content.pm.ActivityInfo;
import android.content.res.Configuration;
import android.graphics.Color;
import android.graphics.Typeface;
import android.graphics.drawable.GradientDrawable;
import android.media.projection.MediaProjectionManager;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.os.SystemClock;
import android.provider.Settings;
import android.text.InputFilter;
import android.text.InputType;
import android.text.method.PasswordTransformationMethod;
import android.util.Log;
import android.view.Gravity;
import android.view.View;
import android.view.Window;
import android.view.WindowManager;
import android.widget.*;
import cn.yucg.bridge.core.Engine;
import cn.yucg.bridge.core.Session;
import org.json.JSONArray;
import org.json.JSONObject;
import java.time.Instant;
import java.util.HashSet;
import java.util.Set;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

public final class MainActivity extends Activity {
    private static final String TAG = "YuDeskStartup";
    private static final int BLUE = 0xff1677ff, INK = 0xff17253b, MUTED = 0xff75839a;
    private final Handler ui = new Handler(Looper.getMainLooper());
    private final ExecutorService worker = Executors.newSingleThreadExecutor();
    private YuDeskApp app;
    private TextView state, identity, remoteStatus;
    private EditText code, pin;
    private CheckBox viewOnly;
    private Button connect;
    private LinearLayout history;
    private DashboardUi dashboardUi;
    private Dialog approvalDialog, waitingDialog;
    private Dialog noticeDialog;
    private final Set<Dialog> ownedDialogs=new HashSet<>();
    private final Set<Dialog> remoteDialogs=new HashSet<>();
    private String deferredTitle,deferredMessage;
    private TextView approvalDescription;
    private String approvalID = "", shownNotice = "";
    private boolean connecting, meetingStarting, pendingMeetingStart, resumed, destroyed;
    private long meetingCaptureDeadline;
    private RemoteView remote;
    private RemoteToolbar remoteToolbar;
    private String remotePlatform="";
    private int dashboardOrientation=ActivityInfo.SCREEN_ORIENTATION_UNSPECIFIED;
    private boolean remoteModeSaved, savedMouseMode;
    private final Runnable refresh = new Runnable() {
        @Override public void run() { if (!destroyed && resumed) { update();ui.postDelayed(this,500); } }
    };

    @Override public void onCreate(Bundle saved) {
        super.onCreate(saved);app = (YuDeskApp)getApplication();
        try {
            app.engine();
            if(saved!=null){dashboardOrientation=saved.getInt("dashboardOrientation",ActivityInfo.SCREEN_ORIENTATION_UNSPECIFIED);remoteModeSaved=saved.containsKey("mouseMode");savedMouseMode=saved.getBoolean("mouseMode");}
            if(app.session!=null)showRemote();else dashboard();
        } catch (Throwable failure) {
            // Linkage errors (unsupported/missing native libraries) are Errors, not
            // Exceptions. Keep the Activity visible so a bad ABI or OEM runtime
            // cannot look like the application silently exited.
            showStartupFailure(failure);
        }
    }
    private int dp(int value) { return Math.round(value*getResources().getDisplayMetrics().density); }
    private GradientDrawable surface(int color,int radius) { GradientDrawable d=new GradientDrawable();d.setColor(color);d.setCornerRadius(dp(radius));return d; }
    private TextView text(String value,int size,int color) { TextView t=new TextView(this);t.setText(value);t.setTextSize(size);t.setTextColor(color);t.setPadding(0,dp(4),0,dp(4));return t; }
    private LinearLayout column() { LinearLayout l=new LinearLayout(this);l.setOrientation(LinearLayout.VERTICAL);return l; }
    private void content(View view) {
        if(remote==null)view.setOnApplyWindowInsetsListener((v,insets)->{v.setPadding(insets.getSystemWindowInsetLeft(),insets.getSystemWindowInsetTop(),insets.getSystemWindowInsetRight(),insets.getSystemWindowInsetBottom());return insets;});
        else view.setPadding(0,0,0,0);
        setContentView(view);view.requestApplyInsets();
    }
    private LinearLayout row() { LinearLayout l=new LinearLayout(this);l.setGravity(Gravity.CENTER_VERTICAL);return l; }
    private Button button(String title,Runnable action) { return DashboardUi.button(this,title,false,action); }
    private void addButton(LinearLayout row,Button b) { LinearLayout.LayoutParams p=new LinearLayout.LayoutParams(0,dp(48),1);p.setMargins(0,dp(4),dp(6),dp(4));row.addView(b,p); }
    private LinearLayout card(LinearLayout root,String title) { LinearLayout c=column();c.setPadding(dp(16),dp(12),dp(16),dp(12));c.setBackground(surface(Color.WHITE,16));LinearLayout.LayoutParams p=new LinearLayout.LayoutParams(-1,-2);p.setMargins(0,0,0,dp(12));root.addView(c,p);TextView h=text(title,16,INK);h.setTypeface(null,Typeface.BOLD);c.addView(h);return c; }
    private void dashboard() {
        for(Dialog d:new HashSet<>(remoteDialogs))d.dismiss();remoteDialogs.clear();
        if(remote!=null){remote.stop();remote=null;}
        remoteToolbar=null;
        getWindow().clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
        getWindow().setNavigationBarColor(0xfff4f7fb);getWindow().setStatusBarColor(0xfff4f7fb);
        getWindow().getDecorView().setSystemUiVisibility(View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR|View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR);
        setRequestedOrientation(dashboardOrientation);
        dashboardUi=new DashboardUi(this,new DashboardUi.Actions(){
            @Override public void connect(){MainActivity.this.connect();}
            @Override public void startSharing(){beginCapture();}
            @Override public void stopSharing(){stopService(new Intent(MainActivity.this,CaptureService.class));}
            @Override public void startMeeting(){MainActivity.this.startMeeting();}
            @Override public void endMeeting(){MainActivity.this.endMeeting();}
            @Override public void joinMeeting(){MainActivity.this.joinMeeting();}
            @Override public void accessibilitySettings(){showChoice("远程触控权限","开启系统无障碍服务后，已获准的控制者可以点击、拖动和输入文本。仅观看无需开启；可随时在系统设置撤销。若在连接后开启，请重新连接。","前往系统设置",()->startActivity(new Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS)));}
            @Override public void rotatePin(Runnable finished){worker.execute(()->{try{app.engine().rotatePIN();}catch(Exception ex){ui.post(()->showError("更换失败",ex.getMessage()));}finally{ui.post(()->{if(dashboardUi!=null)dashboardUi.update(app.state());finished.run();});}});}
            @Override public void clearHistory(){getPreferences(MODE_PRIVATE).edit().remove("devices").apply();renderHistory();}
            @Override public void exit(){MainActivity.this.exit();}
        });
        code=dashboardUi.code;pin=dashboardUi.pin;viewOnly=dashboardUi.viewOnly;connect=dashboardUi.connect;
        identity=dashboardUi.identity;state=dashboardUi.state;history=dashboardUi.history;
        content(dashboardUi.root);if(Build.VERSION.SDK_INT>=30)Api30.showSystemBars(getWindow());dashboardUi.update(app.state());dashboardUi.setConnectionPending(connecting||meetingStarting||pendingMeetingStart);renderHistory();
    }
    private void beginCapture() {
        if(app.sharing){showError("正在共享","如需重新授权，请先停止共享。无需重复开启。");return;}
        if(Build.VERSION.SDK_INT>=33&&checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS)!=PackageManager.PERMISSION_GRANTED){requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS},33);return;}
        requestProjection();
    }
    private void requestProjection() {
        MediaProjectionManager m=(MediaProjectionManager)getSystemService(MEDIA_PROJECTION_SERVICE);
        Intent request=Build.VERSION.SDK_INT>=34?Api34.createScreenCaptureIntent(m):m.createScreenCaptureIntent();
        startActivityForResult(request,40);
    }
    @Override public void onRequestPermissionsResult(int request,String[] permissions,int[] results){super.onRequestPermissionsResult(request,permissions,results);if(request==33){if(results.length>0&&results[0]==PackageManager.PERMISSION_GRANTED)requestProjection();else{pendingMeetingStart=false;if(dashboardUi!=null)dashboardUi.setConnectionPending(false);showError("需要共享通知","请允许通知后再共享，便于及时看到连接请求并随时停止共享。");}}}
    @Override protected void onActivityResult(int request,int result,Intent data){super.onActivityResult(request,result,data);if(request==40){if(result==RESULT_OK&&data!=null){Intent s=new Intent(this,CaptureService.class).putExtra("result",result).putExtra("projection",data);startForegroundService(s);}else{pendingMeetingStart=false;if(dashboardUi!=null)dashboardUi.setConnectionPending(false);showError("共享未开启","系统授权已取消，没有采集或发送屏幕。");}}}
    private void connect(){
        if(connecting)return;String id=code.getText().toString().trim(),secret=pin.getText().toString();boolean control=!viewOnly.isChecked();
        if(!id.matches("[1-9][0-9]{8}")||(!secret.isEmpty()&&!secret.matches("[0-9]{6}"))){showError("检查连接信息","请输入 9 位设备码；PIN 填 6 位数字或留空。");return;}
        connecting=true;if(dashboardUi!=null)dashboardUi.setConnectionPending(true);
        waitingDialog=dialog("正在连接",secret.isEmpty()?"正在检测连接，随后等待对方允许（最多 60 秒）。未获准前不会获取画面或操作。":"正在验证设备和 PIN…", "取消连接",()->{Engine e=app.existingEngine();if(e!=null)e.cancelConnect();},null,null);waitingDialog.setCancelable(false);waitingDialog.show();
        worker.execute(()->{Session session=null;Exception failure=null;try{session=app.engine().connect(id,secret,control);}catch(Exception ex){failure=ex;}Session value=session;Exception error=failure;ui.post(()->{connecting=false;if(waitingDialog!=null){waitingDialog.dismiss();waitingDialog=null;}if(destroyed){if(value!=null)value.close();return;}if(dashboardUi!=null)dashboardUi.setConnectionPending(false);if(error!=null){showError("连接未完成",error.getMessage());return;}app.session=value;remember(id);showRemote();});});
    }
    private void startMeeting(){
        if(meetingStarting)return;JSONObject s=app.state();
        if(!s.optBoolean("online")||!s.optBoolean("active")){showError("暂时不能发起会议","请确认服务器已连接并完成设备激活。");return;}
        if(s.optBoolean("meeting")){showError("会议已开始","当前会议号已显示，可直接复制给参会者。");return;}
        pendingMeetingStart=true;meetingCaptureDeadline=SystemClock.elapsedRealtime()+15000;if(dashboardUi!=null)dashboardUi.setConnectionPending(true);
        if(s.optBoolean("sharing")||app.sharing)createMeeting();else beginCapture();
    }
    private void createMeeting(){
        if(meetingStarting||!pendingMeetingStart)return;meetingStarting=true;pendingMeetingStart=false;if(dashboardUi!=null)dashboardUi.setConnectionPending(true);
        waitingDialog=dialog("正在发起会议","正在创建临时会议号，参会者输入后可直接加入。","取消",()->{Engine e=app.existingEngine();if(e!=null)e.cancelMeetingStart();},null,null);waitingDialog.setCancelable(false);waitingDialog.show();
        worker.execute(()->{String number="";Exception failure=null;try{number=app.engine().startMeeting();}catch(Exception ex){failure=ex;}String code=number;Exception error=failure;ui.post(()->{meetingStarting=false;if(waitingDialog!=null){waitingDialog.dismiss();waitingDialog=null;}if(destroyed)return;if(dashboardUi!=null){dashboardUi.update(app.state());dashboardUi.setConnectionPending(false);}if(error!=null){showError("会议未开始",error.getMessage());return;}showError("会议已开始","会议号 "+DashboardUi.groupCode(code)+"\n参会者输入会议号即可直接加入，最长有效 2 小时。");});});
    }
    private void endMeeting(){
        if(meetingStarting)return;meetingStarting=true;if(dashboardUi!=null)dashboardUi.setConnectionPending(true);
        worker.execute(()->{Exception failure=null;try{Engine e=app.engine();e.endMeeting();}catch(Exception ex){failure=ex;}Exception error=failure;ui.post(()->{meetingStarting=false;stopService(new Intent(MainActivity.this,CaptureService.class));if(dashboardUi!=null){dashboardUi.update(app.state());dashboardUi.setConnectionPending(false);}if(error!=null)showError("会议已在本机结束","服务器目录清理未确认："+error.getMessage());else showError("会议已结束","会议号已失效，当前参会连接已断开。");});});
    }
    private void joinMeeting(){
        if(connecting||dashboardUi==null)return;String number=dashboardUi.meetingCode.getText().toString().replace(" ","").trim();
        if(!number.matches("[1-9][0-9]{8}")){showError("检查会议号","请输入正确的 9 位会议号。");return;}
        connecting=true;dashboardUi.setConnectionPending(true);
        waitingDialog=dialog("正在加入会议","正在查找主持人并建立端到端加密画面，入会无需主持人确认。","取消加入",()->{Engine e=app.existingEngine();if(e!=null)e.cancelConnect();},null,null);waitingDialog.setCancelable(false);waitingDialog.show();
        worker.execute(()->{Session session=null;Exception failure=null;try{session=app.engine().joinMeeting(number);}catch(Exception ex){failure=ex;}Session value=session;Exception error=failure;ui.post(()->{connecting=false;if(waitingDialog!=null){waitingDialog.dismiss();waitingDialog=null;}if(destroyed){if(value!=null)value.close();return;}if(dashboardUi!=null)dashboardUi.setConnectionPending(false);if(error!=null){showError("入会未完成",error.getMessage());return;}app.session=value;showRemote();});});
    }
    private void showRemote(){
        if(app.session==null){dashboard();return;}
        if(remote!=null)remote.stop();
        JSONObject status;try{status=new JSONObject(app.session.statusJSON());}catch(Exception ex){app.endSession();dashboard();showError("连接未完成","无法读取连接状态，请重新连接。");return;}
        remotePlatform=status.optString("platform");
        if(!remoteModeSaved){dashboardOrientation=getResources().getConfiguration().orientation==Configuration.ORIENTATION_LANDSCAPE?ActivityInfo.SCREEN_ORIENTATION_SENSOR_LANDSCAPE:ActivityInfo.SCREEN_ORIENTATION_SENSOR_PORTRAIT;setRequestedOrientation(ActivityInfo.SCREEN_ORIENTATION_SENSOR_LANDSCAPE);}
        LinearLayout root=column();root.setBackgroundColor(0xff0c1420);
        remote=new RemoteView(this,app.session,message->showError("操作提示",message));remote.setControl(status.optBoolean("control"));
        remote.mouseMode(remoteModeSaved?savedMouseMode:!remotePlatform.equals("android"));remoteModeSaved=false;
        remoteToolbar=new RemoteToolbar(this,remote,this::rotateRemote,this::remoteTools,()->{app.endSession();dashboard();},this::sendText);
        remoteToolbar.orientation(getResources().getConfiguration().orientation==Configuration.ORIENTATION_LANDSCAPE);remoteToolbar.update(status);
        root.addView(remoteToolbar.top,new LinearLayout.LayoutParams(-1,dp(44)));
        root.addView(remote,new LinearLayout.LayoutParams(-1,0,1));root.addView(remoteToolbar.bottom,new LinearLayout.LayoutParams(-1,dp(48)));
        getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);getWindow().setNavigationBarColor(0xff0c1420);getWindow().setStatusBarColor(0xff0c1420);content(root);immersiveRemote();remote.start();
    }
    private void immersiveRemote(){
        if(Build.VERSION.SDK_INT>=30)Api30.hideSystemBars(getWindow());
        else getWindow().getDecorView().setSystemUiVisibility(View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY|View.SYSTEM_UI_FLAG_FULLSCREEN|View.SYSTEM_UI_FLAG_HIDE_NAVIGATION|View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN|View.SYSTEM_UI_FLAG_LAYOUT_HIDE_NAVIGATION|View.SYSTEM_UI_FLAG_LAYOUT_STABLE);
    }

    private void showStartupFailure(Throwable failure) {
        Log.e(TAG,"Android startup failed",failure);
        dashboardUi=null;remote=null;remoteToolbar=null;
        getWindow().clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
        getWindow().setStatusBarColor(0xfff4f7fb);getWindow().setNavigationBarColor(0xfff4f7fb);
        LinearLayout background=column();background.setGravity(Gravity.CENTER);background.setPadding(dp(24),dp(24),dp(24),dp(24));background.setBackgroundColor(0xfff4f7fb);
        LinearLayout card=column();card.setPadding(dp(22),dp(20),dp(22),dp(20));card.setBackground(surface(Color.WHITE,20));
        TextView title=text("YuDesk 暂时无法启动",20,INK);title.setTypeface(Typeface.create("sans-serif-medium",Typeface.NORMAL));card.addView(title);
        String reason=failure.getMessage();if(reason==null||reason.trim().isEmpty())reason=failure.getClass().getSimpleName();
        TextView detail=text("启动组件加载失败，应用没有在后台运行。\n\n"+reason+"\n\n请点“重试”；若仍失败，请先卸载旧 YuDesk，再安装官网最新版。",13,MUTED);detail.setLineSpacing(dp(3),1);detail.setTextIsSelectable(true);detail.setPadding(0,dp(10),0,dp(14));card.addView(detail);
        LinearLayout actions=row();addButton(actions,button("退出",this::exit));addButton(actions,button("重试",()->{app.closeEngine();recreate();}));card.addView(actions);
        background.addView(card,new LinearLayout.LayoutParams(-1,-2));content(background);if(Build.VERSION.SDK_INT>=30)Api30.showSystemBars(getWindow());
    }

    // Keep references to newer framework classes out of MainActivity's verified
    // method bodies. Some OEM Android 8/9 runtimes resolve guarded classes early.
    @android.annotation.TargetApi(30)
    private static final class Api30 {
        static void showSystemBars(Window window){window.getDecorView();window.setDecorFitsSystemWindows(true);android.view.WindowInsetsController bars=window.getInsetsController();if(bars!=null)bars.show(android.view.WindowInsets.Type.systemBars());}
        static void hideSystemBars(Window window){window.getDecorView();window.setDecorFitsSystemWindows(false);android.view.WindowInsetsController bars=window.getInsetsController();if(bars!=null){bars.hide(android.view.WindowInsets.Type.systemBars());bars.setSystemBarsBehavior(android.view.WindowInsetsController.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE);}}
    }

    @android.annotation.TargetApi(34)
    private static final class Api34 {
        static Intent createScreenCaptureIntent(MediaProjectionManager manager){return manager.createScreenCaptureIntent(android.media.projection.MediaProjectionConfig.createConfigForDefaultDisplay());}
    }
    private void rotateRemote(){if(remote==null)return;remote.cancelInput();boolean landscape=getResources().getConfiguration().orientation==Configuration.ORIENTATION_LANDSCAPE;setRequestedOrientation(landscape?ActivityInfo.SCREEN_ORIENTATION_SENSOR_PORTRAIT:ActivityInfo.SCREEN_ORIENTATION_SENSOR_LANDSCAPE);}
    @Override public void onConfigurationChanged(Configuration configuration){super.onConfigurationChanged(configuration);if(remote!=null){remote.cancelInput();remoteToolbar.orientation(configuration.orientation==Configuration.ORIENTATION_LANDSCAPE);immersiveRemote();}}
    @Override public void onWindowFocusChanged(boolean hasFocus){super.onWindowFocusChanged(hasFocus);if(hasFocus&&remote!=null)immersiveRemote();}
    @Override protected void onSaveInstanceState(Bundle saved){super.onSaveInstanceState(saved);saved.putInt("dashboardOrientation",dashboardOrientation);if(remote!=null)saved.putBoolean("mouseMode",remote.mouseMode());}
    private void sendKeys(RemoteActions.Key[] keys){try{if(app.session==null||remote==null)return;JSONArray events=new JSONArray();for(RemoteActions.Key key:keys)events.put(new JSONObject().put("type",key.type).put("key",key.key).put("code",key.code));remote.send(events);}catch(Exception ex){if(remote!=null)remote.cancelInput();showError("操作未发送",ex.getMessage());}}
    private void remoteTools(){
        if(remote==null)return;remote.cancelInput();Dialog d=new Dialog(this);d.requestWindowFeature(Window.FEATURE_NO_TITLE);LinearLayout box=column();box.setPadding(dp(20),dp(16),dp(20),dp(16));box.setBackground(surface(Color.WHITE,20));
        box.addView(text("远程工具",18,INK));
        TextView help=text(remote.mouseMode()?"单指移动 · 轻点左击 · 双指滚动\n点“拖动”后单指滑动，抬手释放。":"触屏模式：点哪里操作哪里，单指滑动拖动。",12,MUTED);box.addView(help);
        LinearLayout navigation=row();Button back=button(RemoteActions.backLabel(remotePlatform),()->{d.dismiss();sendKeys(RemoteActions.back(remotePlatform));});Button home=button(RemoteActions.homeLabel(remotePlatform),()->{d.dismiss();sendKeys(RemoteActions.home(remotePlatform));});addButton(navigation,back);addButton(navigation,home);box.addView(navigation);
        LinearLayout scroll=row();Button up=button("向上滚动",()->{if(remote!=null)remote.wheel(1);});Button down=button("向下滚动",()->{if(remote!=null)remote.wheel(-1);});addButton(scroll,up);addButton(scroll,down);box.addView(scroll);
        boolean permitted=false;try{JSONObject status=new JSONObject(app.session.statusJSON());permitted=status.optBoolean("control")&&status.optBoolean("ready");box.addView(text(status.optString("message"),12,MUTED));}catch(Exception ignored){}
        for(Button b:new Button[]{back,home,up,down}){b.setEnabled(permitted);b.setAlpha(permitted?1:.4f);}
        if(remotePlatform.equals("windows"))box.addView(text("后退：Alt+←，适用于浏览器、资源管理器等。\n桌面：Win+D。上方“结束”返回 YuDesk。",11,MUTED));
        box.addView(button("完成",d::dismiss));dialogContent(d,box);styleDialog(d);remoteDialogs.add(d);d.show();
    }
    private void sendText(){if(remote==null)return;remote.cancelInput();EditText input=new EditText(this);input.setHint("输入要发送的文字");input.setTextSize(15);input.setPadding(dp(12),dp(10),dp(12),dp(10));input.setBackground(surface(0xfff0f4fa,10));input.setImeOptions(android.view.inputmethod.EditorInfo.IME_FLAG_NO_EXTRACT_UI);input.setFilters(new InputFilter[]{new InputFilter.LengthFilter(1024)});LinearLayout box=column();box.setPadding(dp(20),dp(16),dp(20),dp(16));box.setBackground(surface(Color.WHITE,20));box.addView(text("发送文字",18,INK));box.addView(input);Dialog d=new Dialog(this);d.requestWindowFeature(Window.FEATURE_NO_TITLE);dialogContent(d,box);LinearLayout actions=row();addButton(actions,button("取消",d::dismiss));addButton(actions,button("发送",()->{try{if(remote!=null)remote.send(new JSONArray().put(new JSONObject().put("type","text").put("text",input.getText().toString())));d.dismiss();}catch(Exception ex){showError("发送失败",ex.getMessage());}}));box.addView(actions);styleDialog(d);remoteDialogs.add(d);d.show();}
    private void update(){
        JSONObject s=app.state();if(dashboardUi!=null&&remote==null)dashboardUi.update(s);if(pendingMeetingStart&&(s.optBoolean("sharing")||app.sharing))createMeeting();else if(pendingMeetingStart&&SystemClock.elapsedRealtime()>meetingCaptureDeadline){pendingMeetingStart=false;if(dashboardUi!=null)dashboardUi.setConnectionPending(false);showError("会议未开始","屏幕共享没有及时启动，请重新授权后再试。");}
        if(remote!=null&&app.session!=null){try{JSONObject session=new JSONObject(app.session.statusJSON());remote.setControl(session.optBoolean("control"));remote.setReady(session.optBoolean("ready"));remoteToolbar.update(session);if(session.optBoolean("closed")){String message=session.optString("message");app.endSession();dashboard();showError("连接已结束",message);}}catch(Exception ignored){}}
        if(!app.notice.isEmpty()&&!app.notice.equals(shownNotice)){shownNotice=app.notice;showError("YuDesk 提示",shownNotice);}
        if(!s.optBoolean("running",true)){stopService(new Intent(this,CaptureService.class));if(!s.optString("message").equals(shownNotice)){shownNotice=s.optString("message");showError("接收已停止",shownNotice);}}
        Engine e=app.existingEngine();if(e==null)return;
        try{String raw=e.pendingApprovalJSON();if(raw.equals("null")){if(approvalDialog!=null){approvalDialog.dismiss();approvalDialog=null;}approvalID="";return;}
            JSONObject p=new JSONObject(raw);String id=p.getString("id");long seconds=Math.max(0,(Instant.parse(p.getString("deadline")).toEpochMilli()-System.currentTimeMillis()+999)/1000);
            String detail="有人请求"+(p.optString("mode").equals("control")?"控制这台手机":"观看这台手机")+"。\n\n仅同意你认识的人；同意后可从通知停止共享。\n剩余 "+seconds+" 秒";
            if(!id.equals(approvalID)){if(approvalDialog!=null)approvalDialog.dismiss();approvalID=id;approvalDialog=dialog("远程连接请求",detail,"拒绝",()->resolve(id,false),"允许",()->resolve(id,true));approvalDescription=approvalDialog.findViewById(R.id.dialog_message);approvalDialog.setCancelable(false);approvalDialog.show();}else if(approvalDescription!=null){approvalDescription.setText(detail);}
        }catch(Exception ex){app.notice="连接请求已失效，请让对方重试。";}
    }
    private void resolve(String id,boolean yes){try{Engine e=app.existingEngine();if(e!=null)e.resolveApproval(id,yes);}catch(Exception ex){showError("请求已结束",ex.getMessage());}}
    private Dialog dialog(String title,String message,String left,Runnable cancel,String right,Runnable accept){
        Dialog d=new Dialog(this);d.requestWindowFeature(Window.FEATURE_NO_TITLE);
        LinearLayout box=column();box.setPadding(dp(20),dp(18),dp(20),dp(18));box.setBackground(surface(Color.WHITE,20));
        TextView h=text(title,18,INK);h.setTypeface(Typeface.create("sans-serif-medium",Typeface.NORMAL));box.addView(h);
        TextView body=text(message==null?"请重试":message,14,MUTED);body.setId(R.id.dialog_message);body.setLineSpacing(dp(3),1);body.setPadding(0,dp(10),0,dp(18));box.addView(body);
        LinearLayout actions=row();
        Button dismiss=DashboardUi.button(this,left,right==null,()->{d.dismiss();if(cancel!=null)cancel.run();});
        LinearLayout.LayoutParams dismissParams=new LinearLayout.LayoutParams(0,-2,1);if(right!=null)dismissParams.setMarginEnd(dp(8));actions.addView(dismiss,dismissParams);
        if(right!=null)actions.addView(DashboardUi.button(this,right,true,()->{d.dismiss();if(accept!=null)accept.run();}),new LinearLayout.LayoutParams(0,-2,1));
        box.addView(actions);dialogContent(d,box);styleDialog(d);return d;
    }
    private void dialogContent(Dialog d,View body){
        ScrollView scroll=new ScrollView(this){@Override protected void onMeasure(int width,int height){int available=MeasureSpec.getMode(height)==MeasureSpec.UNSPECIFIED?Integer.MAX_VALUE:MeasureSpec.getSize(height);int cap=Math.min(available,Math.round(getResources().getDisplayMetrics().heightPixels*.8f));super.onMeasure(width,MeasureSpec.makeMeasureSpec(cap,MeasureSpec.AT_MOST));}};
        scroll.setBackground(surface(Color.WHITE,20));scroll.setClipToOutline(true);scroll.setVerticalScrollBarEnabled(false);scroll.addView(body);d.setContentView(scroll);
    }
    private void styleDialog(Dialog d){DashboardUi.styleDialog(this,d);ownedDialogs.add(d);d.setOnDismissListener(ignored->{ownedDialogs.remove(d);remoteDialogs.remove(d);});Window w=d.getWindow();if(w!=null)w.setSoftInputMode(WindowManager.LayoutParams.SOFT_INPUT_ADJUST_RESIZE);}
    private void showError(String title,String message){if(destroyed||isFinishing())return;if(!resumed){deferredTitle=title;deferredMessage=message;return;}if(noticeDialog!=null)noticeDialog.dismiss();noticeDialog=dialog(title,message,"知道了",null,null,null);noticeDialog.show();}
    private void showChoice(String title,String message,String action,Runnable run){dialog(title,message,"取消",null,action,run).show();}
    private JSONArray saved(){try{return new JSONArray(getPreferences(MODE_PRIVATE).getString("devices","[]"));}catch(Exception ignored){return new JSONArray();}}
    private void remember(String id){JSONArray a=saved(),b=new JSONArray();b.put(id);for(int i=0;i<a.length()&&b.length()<12;i++){String old=a.optString(i);if(!old.equals(id))b.put(old);}getPreferences(MODE_PRIVATE).edit().putString("devices",b.toString()).apply();}
    private void renderHistory(){if(dashboardUi!=null)dashboardUi.renderHistory(saved());}
    private void exit(){stopService(new Intent(this,CaptureService.class));app.closeEngine();finishAndRemoveTask();}
    @Override public void onBackPressed(){if(remote!=null){app.endSession();dashboard();}else exit();}
    @Override protected void onResume(){super.onResume();resumed=true;app.uiVisible=true;ui.post(refresh);if(deferredTitle!=null){String title=deferredTitle,message=deferredMessage;deferredTitle=null;deferredMessage=null;showError(title,message);}}
    @Override protected void onPause(){super.onPause();resumed=false;app.uiVisible=false;ui.removeCallbacks(refresh);if(remote!=null)remote.cancelInput();else if(app.session!=null)app.session.releaseInput();if(approvalDialog!=null){approvalDialog.dismiss();approvalDialog=null;approvalID="";}}
    @Override protected void onDestroy(){destroyed=true;ui.removeCallbacksAndMessages(null);if(remote!=null)remote.stop();for(Dialog d:new HashSet<>(ownedDialogs))d.dismiss();ownedDialogs.clear();Engine e=app.existingEngine();if(e!=null)e.cancelConnect();worker.shutdownNow();if(isFinishing()&&!app.sharing)app.closeEngine();super.onDestroy();}
}
