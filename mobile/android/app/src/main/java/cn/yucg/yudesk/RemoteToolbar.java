package cn.yucg.yudesk;

import android.content.Context;
import android.content.res.ColorStateList;
import android.graphics.Color;
import android.graphics.drawable.GradientDrawable;
import android.graphics.drawable.RippleDrawable;
import android.text.TextUtils;
import android.view.Gravity;
import android.widget.Button;
import android.widget.LinearLayout;
import android.widget.TextView;
import org.json.JSONObject;

/** Two slim, predictable rows; no scrolling toolbar or oversized native buttons. */
final class RemoteToolbar {
    final LinearLayout top,bottom;
    private final Context context;
    private final TextView status;
    private final Button rotate,mode,left,right,drag,keyboard;
    private final RemoteView remote;
    RemoteToolbar(Context context,RemoteView remote,Runnable rotation,Runnable tools,Runnable end,Runnable text){
        this.context=context;this.remote=remote;
        top=row();bottom=row();
        status=new TextView(context);status.setTextColor(0xffb9cce3);status.setTextSize(12);status.setSingleLine(true);status.setEllipsize(TextUtils.TruncateAt.END);status.setText("正在同步画面…");top.addView(status,new LinearLayout.LayoutParams(0,-2,1));
        rotate=button("横屏",rotation);top.addView(rotate,new LinearLayout.LayoutParams(dp(52),dp(44)));
        top.addView(button("工具",tools),new LinearLayout.LayoutParams(dp(52),dp(44)));
        Button finish=button("结束",end);finish.setTextColor(0xffffaab0);top.addView(finish,new LinearLayout.LayoutParams(dp(52),dp(44)));
        mode=button("鼠标",()->remote.mouseMode(!remote.mouseMode()));
        left=button("左键",()->remote.click(1));right=button("右键",()->remote.click(3));drag=button("拖动",remote::toggleDrag);keyboard=button("文字",text);
        for(Button b:new Button[]{mode,left,right,drag,keyboard}){LinearLayout.LayoutParams p=new LinearLayout.LayoutParams(0,dp(48),1);p.setMargins(dp(2),0,dp(2),0);bottom.addView(b,p);}
        remote.onInputChanged(this::syncInput);syncInput();
    }
    private int dp(int value){return Math.round(value*context.getResources().getDisplayMetrics().density);}
    private LinearLayout row(){LinearLayout row=new LinearLayout(context);row.setGravity(Gravity.CENTER_VERTICAL);row.setPadding(dp(8),0,dp(8),0);row.setBackgroundColor(0xff111e30);return row;}
    private Button button(String title,Runnable action){Button b=new Button(context);b.setText(title);b.setAllCaps(false);b.setTextSize(12);b.setSingleLine(true);b.setMinWidth(0);b.setMinimumWidth(0);b.setMinHeight(0);b.setMinimumHeight(0);b.setPadding(dp(3),0,dp(3),0);b.setTextColor(0xffe6efff);b.setStateListAnimator(null);b.setElevation(0);GradientDrawable shape=new GradientDrawable();shape.setColor(0xff192b42);shape.setCornerRadius(dp(8));b.setBackground(new RippleDrawable(ColorStateList.valueOf(0x443887ed),shape,null));b.setOnClickListener(v->action.run());return b;}
    void orientation(boolean landscape){rotate.setText(landscape?"竖屏":"横屏");rotate.setContentDescription(landscape?"切换竖屏":"切换横屏");}
    void update(JSONObject session){boolean control=session.optBoolean("control"),ready=session.optBoolean("ready")&&!session.optBoolean("closed");long rtt=session.optLong("rttMs");String message=session.optString("message");String label=message.isEmpty()||message.equals("已连接")?(control?"远程控制":"仅观看"):message;String route="p2p".equals(session.optString("transport"))?"P2P 直连":"中转";status.setText(context.getString(R.string.remote_status,label,route+" · "+(rtt>0?rtt+" ms":"测量中")));status.setContentDescription(message+"；"+route+"；网络往返延迟 "+rtt+" 毫秒，不是画面延迟；"+session.optString("transportReason"));for(Button b:new Button[]{mode,left,right,drag,keyboard}){b.setEnabled(control&&ready);b.setAlpha(control&&ready?1:.4f);}syncInput();}
    void syncInput(){mode.setText(remote.mouseMode()?"鼠标":"触屏");mode.setContentDescription(remote.mouseMode()?"当前为鼠标模式，点击切换触屏":"当前为触屏模式，点击切换鼠标");drag.setText(remote.dragging()?"拖动中":"拖动");drag.setTextColor(remote.dragging()?0xff64c9ff:Color.WHITE);drag.setEnabled(left.isEnabled()&&remote.mouseMode());drag.setAlpha(drag.isEnabled()?1:.4f);}
}
