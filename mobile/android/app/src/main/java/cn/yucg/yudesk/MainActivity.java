package cn.yucg.yudesk;

import android.Manifest;
import android.app.Activity;
import android.app.Dialog;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.graphics.Color;
import android.graphics.Typeface;
import android.graphics.drawable.GradientDrawable;
import android.media.projection.MediaProjectionConfig;
import android.media.projection.MediaProjectionManager;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.provider.Settings;
import android.text.InputFilter;
import android.text.InputType;
import android.text.method.PasswordTransformationMethod;
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
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

public final class MainActivity extends Activity {
    private static final int BLUE = 0xff1677ff, INK = 0xff17253b, MUTED = 0xff75839a;
    private final Handler ui = new Handler(Looper.getMainLooper());
    private final ExecutorService worker = Executors.newSingleThreadExecutor();
    private YuDeskApp app;
    private TextView state, identity, remoteStatus;
    private EditText code, pin;
    private CheckBox viewOnly;
    private Button connect;
    private LinearLayout history;
    private Dialog approvalDialog, waitingDialog;
    private TextView approvalDescription;
    private String approvalID = "", shownNotice = "";
    private boolean connecting, resumed, destroyed;
    private RemoteView remote;
    private final Runnable refresh = new Runnable() {
        @Override public void run() { if (!destroyed && resumed) { update();ui.postDelayed(this,500); } }
    };

    @Override public void onCreate(Bundle saved) {
        super.onCreate(saved);app = (YuDeskApp)getApplication();
        try { app.engine(); } catch (Exception ex) { showError("启动失败",ex.getMessage());return; }
        dashboard();
    }
    private int dp(int value) { return Math.round(value*getResources().getDisplayMetrics().density); }
    private GradientDrawable surface(int color,int radius) { GradientDrawable d=new GradientDrawable();d.setColor(color);d.setCornerRadius(dp(radius));return d; }
    private TextView text(String value,int size,int color) { TextView t=new TextView(this);t.setText(value);t.setTextSize(size);t.setTextColor(color);t.setPadding(0,dp(4),0,dp(4));return t; }
    private LinearLayout column() { LinearLayout l=new LinearLayout(this);l.setOrientation(LinearLayout.VERTICAL);return l; }
    private void content(View view) {
        view.setOnApplyWindowInsetsListener((v,insets)->{v.setPadding(insets.getSystemWindowInsetLeft(),insets.getSystemWindowInsetTop(),insets.getSystemWindowInsetRight(),insets.getSystemWindowInsetBottom());return insets;});
        setContentView(view);view.requestApplyInsets();
    }
    private LinearLayout row() { LinearLayout l=new LinearLayout(this);l.setGravity(Gravity.CENTER_VERTICAL);return l; }
    private Button button(String title,Runnable action) { Button b=new Button(this);b.setText(title);b.setTextSize(13);b.setAllCaps(false);b.setMinHeight(dp(38));b.setMinimumHeight(dp(38));b.setPadding(dp(14),0,dp(14),0);b.setTextColor(BLUE);b.setBackground(surface(0xffeaf2ff,10));b.setOnClickListener(v->action.run());return b; }
    private void addButton(LinearLayout row,Button b) { LinearLayout.LayoutParams p=new LinearLayout.LayoutParams(0,dp(40),1);p.setMargins(0,dp(4),dp(6),dp(4));row.addView(b,p); }
    private LinearLayout card(LinearLayout root,String title) { LinearLayout c=column();c.setPadding(dp(16),dp(12),dp(16),dp(12));c.setBackground(surface(Color.WHITE,16));LinearLayout.LayoutParams p=new LinearLayout.LayoutParams(-1,-2);p.setMargins(0,0,0,dp(12));root.addView(c,p);TextView h=text(title,16,INK);h.setTypeface(null,Typeface.BOLD);c.addView(h);return c; }
    private void dashboard() {
        if(remote!=null){remote.stop();remote=null;}
        ScrollView scroll=new ScrollView(this);scroll.setFillViewport(true);scroll.setBackgroundColor(0xfff4f7fb);
        LinearLayout root=column();root.setPadding(dp(16),dp(12),dp(16),dp(12));scroll.addView(root);content(scroll);
        LinearLayout top=row();TextView brand=text("YuDesk",25,BLUE);brand.setTypeface(null,Typeface.BOLD);top.addView(brand,new LinearLayout.LayoutParams(0,-2,1));top.addView(button("退出",this::exit));root.addView(top);
        root.addView(text("2.0.0 · Android 预览版",12,MUTED));
        LinearLayout local=card(root,"此设备");identity=text("正在登记设备…",22,INK);identity.setTextIsSelectable(true);local.addView(identity);state=text("验证加密通道…",12,MUTED);local.addView(state);
        LinearLayout ids=row();addButton(ids,button("复制设备码 + PIN",()->{JSONObject s=app.state();String value=s.optString("deviceCode")+"  "+s.optString("pin");((ClipboardManager)getSystemService(CLIPBOARD_SERVICE)).setPrimaryClip(ClipData.newPlainText("YuDesk 设备",value));showError("已复制","设备码和 PIN 已复制。仅发给可信任的人。为了安全，历史记录不会保存 PIN。");}));
        addButton(ids,button("更换 PIN",()->worker.execute(()->{try{app.engine().rotatePIN();}catch(Exception ex){ui.post(()->showError("更换失败",ex.getMessage()));}})));local.addView(ids);
        LinearLayout capture=row();addButton(capture,button("授权共享屏幕",this::beginCapture));addButton(capture,button("停止共享",()->stopService(new Intent(this,CaptureService.class))));local.addView(capture);
        local.addView(button("开启远程触控权限",()->showChoice("远程触控权限","开启系统无障碍服务后，已获准的控制者可以点击、拖动和输入文本。仅观看无需开启；可随时撤销。", "前往系统设置",()->startActivity(new Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS)))));
        local.addView(text("每次共享均需系统授权；接收中始终显示通知。未激活请联系网页管理员。",11,MUTED));
        LinearLayout target=card(root,"连接远程设备");
        code=new EditText(this);code.setSingleLine(true);code.setTextSize(20);code.setHint("9 位设备码");code.setInputType(InputType.TYPE_CLASS_NUMBER);code.setFilters(new InputFilter[]{new InputFilter.LengthFilter(9)});target.addView(code,new LinearLayout.LayoutParams(-1,dp(48)));
        pin=new EditText(this);pin.setSingleLine(true);pin.setTextSize(16);pin.setHint("PIN 可留空，等待对方同意");pin.setInputType(InputType.TYPE_CLASS_NUMBER|InputType.TYPE_NUMBER_VARIATION_PASSWORD);pin.setTransformationMethod(PasswordTransformationMethod.getInstance());pin.setFilters(new InputFilter[]{new InputFilter.LengthFilter(6)});target.addView(pin,new LinearLayout.LayoutParams(-1,dp(44)));
        viewOnly=new CheckBox(this);viewOnly.setText("仅观看，不发送操作");viewOnly.setTextColor(MUTED);target.addView(viewOnly);
        connect=button("连接",this::connect);connect.setTextColor(Color.WHITE);connect.setBackground(surface(BLUE,10));target.addView(connect,new LinearLayout.LayoutParams(-1,dp(44)));
        target.addView(text("空 PIN：对方允许后才能连接，最多等待 60 秒。",11,MUTED));
        LinearLayout saved=card(root,"最近设备");history=column();saved.addView(history);renderHistory();
        LinearLayout limits=card(root,"当前能力");limits.addView(text("支持屏幕共享、点击/拖动、常用按键、中文文本。系统声音、麦克风、摄像头和文件传输暂不可用。",12,MUTED));
        root.addView(text("设计者 郁从根 · 17739798184\n低延迟高响应 · 实际流畅度取决于手机和网络",10,0x8875839a));
    }
    private void beginCapture() {
        if(app.sharing){showError("正在共享","如需重新授权，请先停止共享。无需重复开启。");return;}
        if(Build.VERSION.SDK_INT>=33&&checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS)!=PackageManager.PERMISSION_GRANTED){requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS},33);return;}
        requestProjection();
    }
    private void requestProjection() {
        MediaProjectionManager m=(MediaProjectionManager)getSystemService(MEDIA_PROJECTION_SERVICE);
        Intent request=Build.VERSION.SDK_INT>=34?m.createScreenCaptureIntent(MediaProjectionConfig.createConfigForDefaultDisplay()):m.createScreenCaptureIntent();
        startActivityForResult(request,40);
    }
    @Override public void onRequestPermissionsResult(int request,String[] permissions,int[] results){super.onRequestPermissionsResult(request,permissions,results);if(request==33){if(results.length>0&&results[0]==PackageManager.PERMISSION_GRANTED)requestProjection();else showError("需要共享通知","请允许通知后再共享，便于及时看到连接请求并随时停止共享。");}}
    @Override protected void onActivityResult(int request,int result,Intent data){super.onActivityResult(request,result,data);if(request==40){if(result==RESULT_OK&&data!=null){Intent s=new Intent(this,CaptureService.class).putExtra("result",result).putExtra("projection",data);startForegroundService(s);}else showError("共享未开启","系统授权已取消，没有采集或发送屏幕。");}}
    private void connect(){
        if(connecting)return;String id=code.getText().toString().trim(),secret=pin.getText().toString();boolean control=!viewOnly.isChecked();
        if(!id.matches("[1-9][0-9]{8}")||(!secret.isEmpty()&&!secret.matches("[0-9]{6}"))){showError("检查连接信息","请输入 9 位设备码；PIN 填 6 位数字或留空。");return;}
        connecting=true;connect.setEnabled(false);
        waitingDialog=dialog("正在连接",secret.isEmpty()?"正在检测连接，随后等待对方允许（最多 60 秒）。未获准前不会获取画面或操作。":"正在验证设备和 PIN…", "取消连接",()->{Engine e=app.existingEngine();if(e!=null)e.cancelConnect();},null,null);waitingDialog.setCancelable(false);waitingDialog.show();
        worker.execute(()->{Session session=null;Exception failure=null;try{session=app.engine().connect(id,secret,control);}catch(Exception ex){failure=ex;}Session value=session;Exception error=failure;ui.post(()->{connecting=false;if(waitingDialog!=null){waitingDialog.dismiss();waitingDialog=null;}if(destroyed){if(value!=null)value.close();return;}if(connect!=null)connect.setEnabled(true);if(error!=null){showError("连接未完成",error.getMessage());return;}app.session=value;remember(id);showRemote();});});
    }
    private void showRemote(){
        LinearLayout root=column();root.setBackgroundColor(0xff0c1420);LinearLayout tools=row();tools.setPadding(dp(8),dp(4),dp(8),dp(4));
        remoteStatus=text("正在同步画面…",12,Color.WHITE);tools.addView(remoteStatus,new LinearLayout.LayoutParams(0,-2,1));tools.addView(button("结束",()->{app.endSession();dashboard();}));root.addView(tools);
        LinearLayout keys=row();addButton(keys,button("返回",()->sendKey("Escape","Escape")));addButton(keys,button("主页",()->sendKey("Home","Home")));addButton(keys,button("文字",this::sendText));root.addView(keys);
        remote=new RemoteView(this,app.session,message->showError("操作提示",message));root.addView(remote,new LinearLayout.LayoutParams(-1,0,1));content(root);remote.start();
    }
    private void sendKey(String key,String code){try{if(app.session==null||remote==null)return;JSONObject down=new JSONObject().put("type","key_down").put("key",key).put("code",code),up=new JSONObject().put("type","key_up").put("key",key).put("code",code);remote.send(new JSONArray().put(down).put(up));}catch(Exception ex){showError("操作未发送",ex.getMessage());}}
    private void sendText(){EditText input=new EditText(this);input.setHint("输入要发送的文字");input.setFilters(new InputFilter[]{new InputFilter.LengthFilter(1024)});LinearLayout box=column();box.setPadding(dp(20),dp(16),dp(20),dp(16));box.addView(text("发送文字",18,INK));box.addView(input);Dialog d=new Dialog(this);d.setContentView(box);box.addView(button("发送",()->{try{if(remote!=null)remote.send(new JSONArray().put(new JSONObject().put("type","text").put("text",input.getText().toString())));d.dismiss();}catch(Exception ex){showError("发送失败",ex.getMessage());}}));styleDialog(d);d.show();}
    private void update(){
        JSONObject s=app.state();if(identity!=null&&remote==null){String id=s.optString("deviceCode","");identity.setText(getString(R.string.device_identity,id.isEmpty()?"正在登记…":id,s.optString("pin","------")));state.setText(getString(R.string.device_state,s.optString("message"),s.optBoolean("sharing")?"屏幕共享中":"屏幕未共享",s.optBoolean("accessibility")?"触控权限已开启":"仅观看权限"));}
        if(remote!=null&&app.session!=null){try{JSONObject session=new JSONObject(app.session.statusJSON());remoteStatus.setText(getString(R.string.remote_state,session.optLong("rttMs"),session.optBoolean("control")?"控制":"观看",session.optString("message")));remote.setReady(session.optBoolean("ready"));if(session.optBoolean("closed")){String message=session.optString("message");app.endSession();dashboard();showError("连接已结束",message);}}catch(Exception ignored){}}
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
    private Dialog dialog(String title,String message,String left,Runnable cancel,String right,Runnable accept){Dialog d=new Dialog(this);d.requestWindowFeature(Window.FEATURE_NO_TITLE);LinearLayout box=column();box.setPadding(dp(22),dp(20),dp(22),dp(18));box.setBackground(surface(Color.WHITE,18));TextView h=text(title,19,INK);h.setTypeface(null,Typeface.BOLD);box.addView(h);TextView body=text(message==null?"请重试":message,14,MUTED);body.setId(R.id.dialog_message);box.addView(body);LinearLayout actions=row();addButton(actions,button(left,()->{d.dismiss();if(cancel!=null)cancel.run();}));if(right!=null)addButton(actions,button(right,()->{d.dismiss();if(accept!=null)accept.run();}));box.addView(actions);d.setContentView(box);styleDialog(d);return d;}
    private void styleDialog(Dialog d){Window w=d.getWindow();if(w!=null){w.setBackgroundDrawableResource(android.R.color.transparent);w.setLayout(Math.min(getResources().getDisplayMetrics().widthPixels-dp(40),dp(420)),-2);w.addFlags(WindowManager.LayoutParams.FLAG_DIM_BEHIND);WindowManager.LayoutParams p=w.getAttributes();p.dimAmount=.35f;w.setAttributes(p);}}
    private void showError(String title,String message){if(destroyed||isFinishing())return;dialog(title,message,"知道了",null,null,null).show();}
    private void showChoice(String title,String message,String action,Runnable run){dialog(title,message,"取消",null,action,run).show();}
    private JSONArray saved(){try{return new JSONArray(getPreferences(MODE_PRIVATE).getString("devices","[]"));}catch(Exception ignored){return new JSONArray();}}
    private void remember(String id){JSONArray a=saved(),b=new JSONArray();b.put(id);for(int i=0;i<a.length()&&b.length()<12;i++){String old=a.optString(i);if(!old.equals(id))b.put(old);}getPreferences(MODE_PRIVATE).edit().putString("devices",b.toString()).apply();}
    private void renderHistory(){history.removeAllViews();JSONArray a=saved();if(a.length()==0)history.addView(text("连接成功后记录设备码，不保存 PIN。",12,MUTED));for(int i=0;i<a.length();i++){String id=a.optString(i);history.addView(button(id+"  · 点击填入",()->{code.setText(id);pin.setText("");}));}if(a.length()>0)history.addView(button("清空历史",()->{getPreferences(MODE_PRIVATE).edit().remove("devices").apply();renderHistory();}));}
    private void exit(){stopService(new Intent(this,CaptureService.class));app.closeEngine();finishAndRemoveTask();}
    @Override public void onBackPressed(){if(remote!=null){app.endSession();dashboard();}else exit();}
    @Override protected void onResume(){super.onResume();resumed=true;app.uiVisible=true;ui.post(refresh);}
    @Override protected void onPause(){super.onPause();resumed=false;app.uiVisible=false;ui.removeCallbacks(refresh);if(app.session!=null)app.session.releaseInput();if(approvalDialog!=null){approvalDialog.dismiss();approvalDialog=null;approvalID="";}}
    @Override protected void onDestroy(){destroyed=true;ui.removeCallbacksAndMessages(null);if(remote!=null)remote.stop();if(waitingDialog!=null)waitingDialog.dismiss();if(approvalDialog!=null)approvalDialog.dismiss();Engine e=app.existingEngine();if(e!=null)e.cancelConnect();worker.shutdownNow();if(isFinishing()&&!app.sharing)app.closeEngine();super.onDestroy();}
}
