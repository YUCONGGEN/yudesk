package cn.yucg.yudesk;

/** UI-independent mouse state machine. Button edges are never throttled. */
final class PointerController {
    static final class Event {
        final String type;
        final int x,y,button,deltaY;
        Event(String type,int x,int y,int button,int deltaY){this.type=type;this.x=x;this.y=y;this.button=button;this.deltaY=deltaY;}
    }
    interface Sink { void send(Event... events) throws Exception; void release(); }
    private final Sink sink;
    private final float slop;
    private ScreenMapping map;
    private float x,y,originX,originY,lastX,lastY,scrollRemainder;
    private long downTime,lastMove;
    private boolean relative=true,drag,active,moved,buttonDown;
    PointerController(Sink sink,float touchSlop){this.sink=sink;slop=touchSlop;}
    void geometry(ScreenMapping next){
        if(map==null){x=(next.sourceWidth-1)/2f;y=(next.sourceHeight-1)/2f;}
        else if(map.sourceWidth!=next.sourceWidth||map.sourceHeight!=next.sourceHeight){
            cancel();x=x/Math.max(1,map.sourceWidth-1)*(next.sourceWidth-1);y=y/Math.max(1,map.sourceHeight-1)*(next.sourceHeight-1);
        }
        map=next;clamp();
    }
    boolean relative(){return relative;}
    boolean dragging(){return drag;}
    boolean active(){return active;}
    int x(){return Math.round(x);}
    int y(){return Math.round(y);}
    void mode(boolean value){cancel();relative=value;}
    void drag(boolean value){cancel();drag=value&&relative;}
    void cancel(){active=false;buttonDown=false;drag=false;scrollRemainder=0;sink.release();}
    private void clamp(){if(map!=null){x=Math.max(0,Math.min(map.sourceWidth-1,x));y=Math.max(0,Math.min(map.sourceHeight-1,y));}}
    private Event event(String type,int button){return new Event(type,x(),y(),button,0);}
    void down(float px,float py,long time)throws Exception{
        if(map==null||(!relative&&!map.contains(px,py)))return;
        active=true;moved=false;originX=lastX=px;originY=lastY=py;downTime=time;lastMove=time;
        if(!relative){x=map.x(px);y=map.y(py);}
        if(!relative||drag){buttonDown=true;sink.send(event("move",0),event("down",1));}
    }
    void move(float px,float py,long time)throws Exception{
        if(!active)return;
        if(relative){
            if(!moved&&Math.hypot(px-originX,py-originY)<=slop)return;
            moved=true;x+=(px-lastX)*map.sourceWidth/Math.max(1,map.width);y+=(py-lastY)*map.sourceHeight/Math.max(1,map.height);clamp();
        }else{x=map.x(px);y=map.y(py);}
        lastX=px;lastY=py;
        if(time-lastMove>=16){lastMove=time;sink.send(event("move",0));}
    }
    void up(float px,float py,long time)throws Exception{
        if(!active)return;
        move(px,py,time);active=false;
        if(buttonDown){buttonDown=false;sink.send(event("move",0),event("up",1));}
        else if(!moved&&time-downTime<=600){click(1);}
        else if(moved){sink.send(event("move",0));}
        // Drag is deliberately one gesture: it cannot leave a held button behind.
        drag=false;
    }
    void click(int button)throws Exception{
        if(map==null)return;
        if(active||buttonDown)cancel();
        sink.send(event("move",0),event("down",button),event("up",button));
    }
    void scroll(float fingerDeltaDp)throws Exception{
        if(map==null)return;
        scrollRemainder-=fingerDeltaDp;
        int steps=(int)(scrollRemainder/24f);
        if(steps==0)return;
        scrollRemainder-=steps*24f;
        int delta=Math.max(-1200,Math.min(1200,steps*120));
        sink.send(event("move",0),new Event("wheel",x(),y(),0,delta));
    }
}
