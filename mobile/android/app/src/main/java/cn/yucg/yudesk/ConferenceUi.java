package cn.yucg.yudesk;

import android.app.Activity;
import android.content.Context;
import android.content.res.ColorStateList;
import android.graphics.Color;
import android.graphics.Canvas;
import android.graphics.Paint;
import android.graphics.Path;
import android.graphics.Typeface;
import android.graphics.drawable.GradientDrawable;
import android.graphics.drawable.RippleDrawable;
import android.text.Editable;
import android.text.InputType;
import android.text.TextUtils;
import android.text.TextWatcher;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.widget.FrameLayout;
import android.widget.HorizontalScrollView;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.TextView;
import org.webrtc.EglBase;
import org.webrtc.RendererCommon;
import org.webrtc.SurfaceViewRenderer;
import org.webrtc.VideoTrack;
import java.util.LinkedHashMap;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Locale;

/** Native light meeting UI aligned with the WeLink white, gray and blue palette. */
final class ConferenceUi {
    interface Actions {
        void microphone();
        void speaker();
        void camera();
        void switchCamera();
        void share();
        void record();
        void leave();
        void member(Member member);
    }

    static final class Member {
        final String id, name;
        final long joinedAt;
        final boolean self, host, microphone, camera, screen, recording;
        Member(String id, String name, long joinedAt, boolean self, boolean host, boolean microphone, boolean camera, boolean screen, boolean recording) {
            this.id=id;this.name=name;this.joinedAt=joinedAt;this.self=self;this.host=host;this.microphone=microphone;this.camera=camera;this.screen=screen;this.recording=recording;
        }
    }

    final FrameLayout root;
    private final Activity activity;
    private final Actions actions;
    private final EglBase.Context eglContext;
    private final TileGrid grid;
    private final LinearLayout memberList, memberPanel, topBar;
    private final HorizontalScrollView toolbarBar;
    private final TextView connection, count, recordingBadge, sharingBadge, shareFocusBadge;
    private final EditText memberSearch;
    private final Map<String, Tile> tiles = new LinkedHashMap<>();
    private final List<Member> memberValues = new ArrayList<>();
    private final Tool mic, speaker, camera, switchCamera, share, members, record, leave;
    private boolean memberPanelOpen, selfIsHost, followShare;
    private String focusedID="";

    ConferenceUi(Activity activity, String code, EglBase.Context eglContext, Actions actions) {
        this.activity=activity;this.actions=actions;this.eglContext=eglContext;
        root=new FrameLayout(activity);root.setBackgroundColor(0xfff5f6f7);

        grid=new TileGrid(activity);grid.setPadding(dp(8),dp(58),dp(8),dp(82));
        root.addView(grid,new FrameLayout.LayoutParams(-1,-1));

        topBar=row();topBar.setPadding(dp(18),dp(8),dp(12),dp(8));topBar.setBackground(fade(0xffffffff,0xffffffff));
        LinearLayout heading=column();TextView title=text("YuDesk 会议",15,0xff22262b);title.setTypeface(Typeface.DEFAULT_BOLD);heading.addView(title,new LinearLayout.LayoutParams(-1,dp(22)));TextView roomNumber=text(DashboardUi.groupCode(code),12,0xff8995a4);heading.addView(roomNumber,new LinearLayout.LayoutParams(-1,dp(20)));topBar.addView(heading,new LinearLayout.LayoutParams(0,dp(42),1));
        sharingBadge=text("",11,0xff23885c);sharingBadge.setSingleLine(true);sharingBadge.setEllipsize(TextUtils.TruncateAt.END);sharingBadge.setMaxWidth(dp(190));sharingBadge.setPadding(dp(9),0,dp(9),0);sharingBadge.setBackground(round(0xffeffbf5,14));sharingBadge.setVisibility(View.GONE);topBar.addView(sharingBadge,new LinearLayout.LayoutParams(-2,dp(30)));
        recordingBadge=text("● 录制中",12,0xffff7272);recordingBadge.setVisibility(View.GONE);recordingBadge.setPadding(dp(10),0,dp(10),0);topBar.addView(recordingBadge,new LinearLayout.LayoutParams(-2,dp(42)));
        connection=text("正在连接…",12,0xff8995a4);connection.setGravity(Gravity.END|Gravity.CENTER_VERTICAL);topBar.addView(connection,new LinearLayout.LayoutParams(-2,dp(42)));
        FrameLayout.LayoutParams topParams=new FrameLayout.LayoutParams(-1,dp(58),Gravity.TOP);root.addView(topBar,topParams);

        toolbarBar=new HorizontalScrollView(activity);toolbarBar.setHorizontalScrollBarEnabled(false);toolbarBar.setFillViewport(true);toolbarBar.setBackgroundColor(0xffffffff);
        LinearLayout toolbar=row();toolbar.setGravity(Gravity.CENTER);toolbar.setPadding(dp(8),dp(4),dp(8),dp(4));
        mic=tool("麦克风",this.actions::microphone);speaker=tool("扬声器",this.actions::speaker);camera=tool("摄像头",this.actions::camera);switchCamera=tool("切换镜头",this.actions::switchCamera);share=tool("共享屏幕",this.actions::share);members=tool("成员",()->setMembersOpen(!memberPanelOpen));record=tool("录制",this.actions::record);leave=tool("离开",this.actions::leave);leave.label.setTextColor(0xffff7272);
        for(Tool tool:new Tool[]{mic,speaker,camera,switchCamera,share,members,record,leave})toolbar.addView(tool.root,new LinearLayout.LayoutParams(dp(72),dp(70)));
        toolbarBar.addView(toolbar,new HorizontalScrollView.LayoutParams(-1,-1));
        FrameLayout.LayoutParams toolbarParams=new FrameLayout.LayoutParams(-1,dp(78),Gravity.BOTTOM);root.addView(toolbarBar,toolbarParams);

        memberPanel=column();memberPanel.setPadding(dp(14),dp(14),dp(14),dp(14));memberPanel.setBackgroundColor(0xffffffff);memberPanel.setVisibility(View.GONE);
        LinearLayout memberHeader=row();TextView memberTitle=text("参会成员",16,0xff22262b);memberTitle.setTypeface(Typeface.DEFAULT_BOLD);memberHeader.addView(memberTitle,new LinearLayout.LayoutParams(0,dp(42),1));count=text("0 人",12,0xff8995a4);memberHeader.addView(count,new LinearLayout.LayoutParams(-2,dp(42)));memberPanel.addView(memberHeader);
        memberSearch=new EditText(activity);memberSearch.setSingleLine(true);memberSearch.setTextSize(12);memberSearch.setHint("搜索姓名");memberSearch.setHintTextColor(0xff9aa5b2);memberSearch.setTextColor(0xff27384c);memberSearch.setInputType(InputType.TYPE_CLASS_TEXT);memberSearch.setPadding(dp(11),0,dp(11),0);memberSearch.setBackground(round(0xfff2f5f8,9));memberPanel.addView(memberSearch,new LinearLayout.LayoutParams(-1,dp(38)));memberSearch.addTextChangedListener(new TextWatcher(){@Override public void beforeTextChanged(CharSequence s,int start,int count,int after){}@Override public void onTextChanged(CharSequence s,int start,int before,int count){renderMemberRows();}@Override public void afterTextChanged(Editable value){}});
        memberList=column();memberPanel.addView(memberList,new LinearLayout.LayoutParams(-1,0,1));
        FrameLayout.LayoutParams panelParams=new FrameLayout.LayoutParams(dp(310),-1,Gravity.END);panelParams.topMargin=dp(58);panelParams.bottomMargin=dp(78);root.addView(memberPanel,panelParams);

        shareFocusBadge=text("",11,Color.WHITE);shareFocusBadge.setSingleLine(true);shareFocusBadge.setGravity(Gravity.CENTER);shareFocusBadge.setPadding(dp(13),0,dp(13),0);shareFocusBadge.setBackground(round(0xb3162434,16));shareFocusBadge.setVisibility(View.GONE);shareFocusBadge.setOnClickListener(v->clearShareFocus());FrameLayout.LayoutParams focusParams=new FrameLayout.LayoutParams(-2,dp(34),Gravity.TOP|Gravity.CENTER_HORIZONTAL);focusParams.topMargin=dp(10);root.addView(shareFocusBadge,focusParams);
    }

    void setConnection(String value, boolean warning) { connection.setText(value);connection.setTextColor(warning?0xffdc9443:0xff8995a4); }

    void rename(String oldID,String newID) {
        Tile tile=tiles.remove(oldID);if(tile==null)return;tile.id=newID;tiles.put(newID,tile);
    }

    void upsert(Member member) {
        Tile tile=tiles.get(member.id);
        if(tile==null){tile=new Tile(member.id,member.name);tiles.put(member.id,tile);grid.addView(tile.root);}
        tile.name=member.name;tile.self=member.self;tile.host=member.host;tile.microphone=member.microphone;tile.camera=member.camera;tile.screen=member.screen;tile.recording=member.recording;if(!member.microphone)tile.voice.setLevel(0);tile.render();grid.requestLayout();
    }

    void attachVideo(String id, VideoTrack track, boolean mirror) {
        Tile tile=tiles.get(id);if(tile==null)return;
        if(tile.track==track)return;
        if(tile.track!=null)tile.track.removeSink(tile.renderer);
        tile.track=track;tile.renderer.setMirror(mirror);if(track!=null)track.addSink(tile.renderer);tile.render();
    }

    void setVoiceLevel(String id,int level) { Tile tile=tiles.get(id);if(tile!=null)tile.voice.setLevel(level); }

    void remove(String id) {
        Tile tile=tiles.remove(id);if(tile==null)return;if(tile.track!=null)tile.track.removeSink(tile.renderer);tile.renderer.release();grid.removeView(tile.root);if(id.equals(focusedID))clearShareFocus();grid.requestLayout();
    }

    void renderMembers(List<Member> values, boolean selfIsHost) {
        this.selfIsHost=selfIsHost;memberValues.clear();memberValues.addAll(values);renderMemberRows();
        boolean anyRecording=false;Member sharer=null;
        for(Member member:values){anyRecording|=member.recording;if(member.screen)sharer=member;}
        recordingBadge.setVisibility(anyRecording?View.VISIBLE:View.GONE);
        sharingBadge.setVisibility(sharer==null?View.GONE:View.VISIBLE);sharingBadge.setText(sharer==null?"":sharer.name+(sharer.self?"（我）":"")+" 正在共享");
        if(followShare){if(sharer==null)clearShareFocus();else if(!sharer.id.equals(focusedID))focusShare(sharer.id,false);}
    }

    private void renderMemberRows(){
        if(memberList==null)return;memberList.removeAllViews();String query=normalize(memberSearch.getText().toString());int shown=0;
        for(Member member:memberValues){
            if(!query.isEmpty()&&!normalize(member.name+member.id).contains(query))continue;shown++;
            LinearLayout row=row();row.setPadding(dp(4),dp(6),0,dp(6));
            TextView avatar=text(initial(member.name),14,0xff0099ff);avatar.setGravity(Gravity.CENTER);avatar.setTypeface(Typeface.DEFAULT_BOLD);avatar.setBackground(round(0xffe3f3ff,18));row.addView(avatar,new LinearLayout.LayoutParams(dp(36),dp(36)));
            LinearLayout labels=column();labels.setPadding(dp(10),0,dp(6),0);TextView name=text(member.name+(member.self?"（我）":""),13,0xff22262b);name.setSingleLine(true);labels.addView(name);String state=(member.host?"主持人 · ":"")+(member.microphone?"麦克风开启":"已静音")+(member.camera?" · 视频":"")+(member.screen?" · 共享":"");TextView detail=text(state,11,0xff8995a4);detail.setSingleLine(true);labels.addView(detail);row.addView(labels,new LinearLayout.LayoutParams(0,dp(42),1));
            if(selfIsHost){TextView menu=text("•••",14,0xff0099ff);menu.setGravity(Gravity.CENTER);menu.setContentDescription("管理 "+member.name);menu.setBackground(ripple(0x182f83ff,10));menu.setOnClickListener(v->actions.member(member));row.addView(menu,new LinearLayout.LayoutParams(dp(38),dp(38)));row.setOnLongClickListener(v->{actions.member(member);return true;});}
            memberList.addView(row,new LinearLayout.LayoutParams(-1,dp(54)));
        }
        count.setText(query.isEmpty()?String.format(Locale.getDefault(),"%d 人",memberValues.size()):String.format(Locale.getDefault(),"%d / %d 人",shown,memberValues.size()));
    }

    void setControls(boolean microphone,boolean speakerEnabled,boolean cameraEnabled,boolean screen,boolean recording,boolean host,boolean sharePending) {
        mic.setActive(microphone,microphone?"静音":"解除静音");speaker.setActive(speakerEnabled,speakerEnabled?"关闭声音":"播放声音");camera.setActive(cameraEnabled,cameraEnabled?"关闭视频":"开启视频");switchCamera.root.setVisibility(cameraEnabled?View.VISIBLE:View.GONE);share.root.setVisibility(host?View.VISIBLE:View.GONE);record.root.setVisibility(host?View.VISIBLE:View.GONE);share.setActive(screen,sharePending?"选择屏幕…":screen?"停止共享":"共享屏幕");share.setEnabled(!sharePending);record.setActive(recording,recording?"停止录制":"录制");leave.label.setText(host?"结束会议":"离开会议");
    }

    void setMembersOpen(boolean open){memberPanelOpen=open;memberPanel.setVisibility(open?View.VISIBLE:View.GONE);members.setActive(open,"成员");}

    boolean handleBack(){if(!focusedID.isEmpty()){clearShareFocus();return true;}if(memberPanelOpen){setMembersOpen(false);return true;}return false;}

    private void focusShare(String id,boolean chosen){Tile tile=tiles.get(id);if(tile==null||!tile.screen)return;if(chosen)followShare=true;focusedID=id;for(Map.Entry<String,Tile> entry:tiles.entrySet())entry.getValue().root.setVisibility(entry.getKey().equals(id)?View.VISIBLE:View.GONE);topBar.setVisibility(View.GONE);toolbarBar.setVisibility(View.GONE);memberPanel.setVisibility(View.GONE);memberPanelOpen=false;members.setActive(false,"成员");grid.setPadding(0,0,0,0);shareFocusBadge.setText(tile.name+" 正在共享 · 点击返回会议");shareFocusBadge.setVisibility(View.VISIBLE);grid.requestLayout();}
    private void clearShareFocus(){focusedID="";followShare=false;for(Tile tile:tiles.values())tile.root.setVisibility(View.VISIBLE);topBar.setVisibility(View.VISIBLE);toolbarBar.setVisibility(View.VISIBLE);grid.setPadding(dp(8),dp(58),dp(8),dp(82));shareFocusBadge.setVisibility(View.GONE);grid.requestLayout();}

    void release(){for(Tile tile:tiles.values()){if(tile.track!=null)tile.track.removeSink(tile.renderer);tile.renderer.release();}tiles.clear();grid.removeAllViews();}

    private final class Tile {
        String id,name;boolean self,host,microphone,camera,screen,recording;VideoTrack track;
        final FrameLayout root=new FrameLayout(activity);final SurfaceViewRenderer renderer=new SurfaceViewRenderer(activity);final TextView avatar,nameLabel,state;final VoiceFlow voice=new VoiceFlow(activity);
        Tile(String id,String name){this.id=id;this.name=name;root.setBackground(round(0xffffffff,14));renderer.init(eglContext,null);renderer.setEnableHardwareScaler(true);renderer.setScalingType(RendererCommon.ScalingType.SCALE_ASPECT_FIT);root.addView(renderer,new FrameLayout.LayoutParams(-1,-1));avatar=text(initial(name),30,0xff0099ff);avatar.setGravity(Gravity.CENTER);avatar.setTypeface(Typeface.DEFAULT_BOLD);avatar.setBackground(round(0xffe3f3ff,42));FrameLayout.LayoutParams avatarParams=new FrameLayout.LayoutParams(dp(84),dp(84),Gravity.CENTER);root.addView(avatar,avatarParams);LinearLayout badge=row();badge.setPadding(dp(10),0,dp(10),0);badge.setBackground(round(0xecffffff,8));nameLabel=text(name,12,0xff22262b);nameLabel.setSingleLine(true);badge.addView(nameLabel,new LinearLayout.LayoutParams(-2,dp(30)));state=text("",11,0xff0099ff);state.setPadding(dp(8),0,0,0);badge.addView(state,new LinearLayout.LayoutParams(-2,dp(30)));FrameLayout.LayoutParams badgeParams=new FrameLayout.LayoutParams(-2,dp(30),Gravity.START|Gravity.BOTTOM);badgeParams.setMargins(dp(10),0,0,dp(10));root.addView(badge,badgeParams);FrameLayout.LayoutParams voiceParams=new FrameLayout.LayoutParams(dp(38),dp(26),Gravity.START|Gravity.TOP);voiceParams.setMargins(dp(10),dp(10),0,0);root.addView(voice,voiceParams);root.setOnClickListener(v->{if(screen){if(id.equals(focusedID))clearShareFocus();else focusShare(id,true);}});}
        void render(){nameLabel.setText(memberLabel(name,self,host));avatar.setText(initial(name));boolean video=track!=null&&(camera||screen);renderer.setVisibility(video?View.VISIBLE:View.INVISIBLE);avatar.setVisibility(video?View.GONE:View.VISIBLE);state.setText(screen?"共享屏幕 · 点击放大":microphone?"麦克风开启":"已静音");root.setClickable(screen);root.setContentDescription(screen?"放大查看 "+name+" 的共享屏幕":name);}
    }

    private static final class VoiceFlow extends View {
        private final Paint paint=new Paint(Paint.ANTI_ALIAS_FLAG);private int level;
        VoiceFlow(Context context){super(context);setContentDescription("声音流");}
        void setLevel(int value){value=Math.max(0,Math.min(100,value));if(level==value)return;level=value;setContentDescription(level>=7?"正在说话":"声音流");invalidate();}
        @Override protected void onDraw(Canvas canvas){super.onDraw(canvas);float radius=getHeight()/2f;paint.setStyle(Paint.Style.FILL);paint.setColor(level>=7?0xffe8f9f0:0xeef7f9fc);canvas.drawRoundRect(0,0,getWidth(),getHeight(),radius,radius,paint);int[] shape={42,72,100,58};float center=getHeight()/2f,barWidth=Math.max(2,getWidth()/16f),gap=barWidth*1.4f,total=barWidth*4+gap*3,start=(getWidth()-total)/2f;paint.setColor(level>=7?0xff27ad70:0xff9aa6b5);for(int i=0;i<4;i++){float height=Math.max(3,(3+level*shape[i]/1000f)*getResources().getDisplayMetrics().density),left=start+i*(barWidth+gap);canvas.drawRoundRect(left,center-height/2,left+barWidth,center+height/2,barWidth/2,barWidth/2,paint);}}
    }

    private static final class TileGrid extends ViewGroup {
        TileGrid(Context context){super(context);setClipToPadding(false);}
        @Override protected void onMeasure(int widthMeasureSpec,int heightMeasureSpec){int width=MeasureSpec.getSize(widthMeasureSpec),height=MeasureSpec.getSize(heightMeasureSpec);int innerW=Math.max(0,width-getPaddingLeft()-getPaddingRight()),innerH=Math.max(0,height-getPaddingTop()-getPaddingBottom()),count=getChildCount(),columns=count<=1?1:count<=4?2:count<=6?3:4,rows=Math.max(1,(count+columns-1)/columns),gap=dp(getContext(),8),cellW=Math.max(0,(innerW-gap*(columns-1))/columns),cellH=Math.max(0,(innerH-gap*(rows-1))/rows);for(int i=0;i<count;i++)getChildAt(i).measure(MeasureSpec.makeMeasureSpec(cellW,MeasureSpec.EXACTLY),MeasureSpec.makeMeasureSpec(cellH,MeasureSpec.EXACTLY));setMeasuredDimension(width,height);}
        @Override protected void onLayout(boolean changed,int left,int top,int right,int bottom){int count=getChildCount(),columns=count<=1?1:count<=4?2:count<=6?3:4,rows=Math.max(1,(count+columns-1)/columns),gap=dp(getContext(),8),innerW=getWidth()-getPaddingLeft()-getPaddingRight(),innerH=getHeight()-getPaddingTop()-getPaddingBottom(),cellW=(innerW-gap*(columns-1))/columns,cellH=(innerH-gap*(rows-1))/rows;for(int i=0;i<count;i++){int col=i%columns,row=i/columns,x=getPaddingLeft()+col*(cellW+gap),y=getPaddingTop()+row*(cellH+gap);getChildAt(i).layout(x,y,x+cellW,y+cellH);}}
    }

    private final class Tool {final LinearLayout root;final MeetingIcon glyph;final TextView label;Tool(String title,Runnable action){root=column();root.setGravity(Gravity.CENTER);root.setBackground(ripple(Color.TRANSPARENT,10));glyph=new MeetingIcon(activity,title);label=text(title,11,0xff657382);label.setGravity(Gravity.CENTER);root.addView(glyph,new LinearLayout.LayoutParams(dp(24),dp(24)));root.addView(label,new LinearLayout.LayoutParams(-1,dp(30)));root.setOnClickListener(v->action.run());root.setContentDescription(title);}void setActive(boolean active,String title){glyph.active=active;glyph.invalidate();label.setText(title);label.setTextColor(active?0xff0099ff:0xff657382);root.setContentDescription(title);}void setEnabled(boolean enabled){root.setEnabled(enabled);root.setAlpha(enabled?1f:.55f);}}
    private static final class MeetingIcon extends View {
        final String kind;final Paint pen=new Paint(Paint.ANTI_ALIAS_FLAG);boolean active;
        MeetingIcon(Context context,String kind){super(context);this.kind=kind;}
        @Override protected void onDraw(Canvas canvas){super.onDraw(canvas);canvas.save();canvas.scale(getWidth()/24f,getHeight()/24f);pen.setStyle(Paint.Style.STROKE);pen.setStrokeWidth(1.6f);pen.setStrokeCap(Paint.Cap.ROUND);pen.setStrokeJoin(Paint.Join.ROUND);pen.setColor(kind.equals("离开")?0xffdc535a:active?0xff0099ff:0xff718095);
            switch(kind){
                case "麦克风":canvas.drawRoundRect(9,2,15,14,3,3,pen);canvas.drawArc(6,5,18,18,0,180,false,pen);canvas.drawLine(6,9,6,12,pen);canvas.drawLine(18,9,18,12,pen);canvas.drawLine(12,18,12,22,pen);canvas.drawLine(8,22,16,22,pen);break;
                case "扬声器":path(canvas,new float[]{3,9,8,9,14,4,14,20,8,15,3,15},true);canvas.drawArc(15,8,21,16,-70,140,false,pen);break;
                case "摄像头":canvas.drawRoundRect(2,5,16,19,2,2,pen);path(canvas,new float[]{16,9,22,6,22,18,16,15},true);break;
                case "切换镜头":canvas.drawRoundRect(2,6,22,20,2,2,pen);path(canvas,new float[]{7,6,9,3,15,3,17,6},false);canvas.drawArc(8,9,16,17,35,260,false,pen);path(canvas,new float[]{15,9,16,12,13,12},false);break;
                case "共享屏幕":canvas.drawRoundRect(2,3,22,18,2,2,pen);canvas.drawLine(8,22,16,22,pen);canvas.drawLine(12,18,12,22,pen);path(canvas,new float[]{8,10,12,6,16,10},false);canvas.drawLine(12,6,12,14,pen);break;
                case "成员":canvas.drawCircle(9,7,3,pen);canvas.drawArc(2,13,16,25,180,180,false,pen);canvas.drawArc(14,4,21,11,260,170,false,pen);canvas.drawArc(14,13,24,25,260,100,false,pen);break;
                case "录制":canvas.drawCircle(12,12,9,pen);pen.setStyle(Paint.Style.FILL);canvas.drawCircle(12,12,4,pen);break;
                case "离开":path(canvas,new float[]{10,3,3,3,3,21,10,21},false);canvas.drawLine(9,12,22,12,pen);path(canvas,new float[]{17,7,22,12,17,17},false);break;
                default:canvas.drawCircle(12,12,8,pen);
            }canvas.restore();
        }
        private void path(Canvas canvas,float[] points,boolean close){Path path=new Path();path.moveTo(points[0],points[1]);for(int i=2;i<points.length;i+=2)path.lineTo(points[i],points[i+1]);if(close)path.close();canvas.drawPath(path,pen);}
    }
    private Tool tool(String title,Runnable action){return new Tool(title,action);}
    private LinearLayout row(){LinearLayout v=new LinearLayout(activity);v.setOrientation(LinearLayout.HORIZONTAL);v.setGravity(Gravity.CENTER_VERTICAL);return v;}
    private LinearLayout column(){LinearLayout v=new LinearLayout(activity);v.setOrientation(LinearLayout.VERTICAL);return v;}
    private TextView text(String value,int size,int color){TextView v=new TextView(activity);v.setText(value);v.setTextSize(size);v.setTextColor(color);v.setIncludeFontPadding(false);v.setGravity(Gravity.CENTER_VERTICAL);return v;}
    private String memberLabel(String name,boolean self,boolean host){return name+(self?"（我）":"")+(host?" · 主持人":"");}
    private String initial(String name){String value=name==null?"":name.trim();if(value.isEmpty())return "参";int end=value.offsetByCodePoints(0,1);return value.substring(0,end).toUpperCase(Locale.getDefault());}
    private String normalize(String value){return value==null?"":value.toLowerCase(Locale.getDefault()).replaceAll("\\s+","");}
    private GradientDrawable round(int color,int radius){GradientDrawable d=new GradientDrawable();d.setColor(color);d.setCornerRadius(dp(radius));return d;}
    private RippleDrawable ripple(int color,int radius){return new RippleDrawable(ColorStateList.valueOf(0x220099ff),round(color,radius),null);}
    private GradientDrawable fade(int start,int end){GradientDrawable d=new GradientDrawable(GradientDrawable.Orientation.TOP_BOTTOM,new int[]{start,end});return d;}
    private int dp(int value){return dp(activity,value);}private static int dp(Context context,int value){return Math.round(value*context.getResources().getDisplayMetrics().density);}
}
