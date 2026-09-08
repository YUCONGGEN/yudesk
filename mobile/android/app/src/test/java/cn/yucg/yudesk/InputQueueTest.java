package cn.yucg.yudesk;

import org.junit.Test;
import static org.junit.Assert.*;

public class InputQueueTest {
    private static final class Event {
        final String type;final int button,x,y,width;
        Event(String t,int b,int x,int y,int w){type=t;button=b;this.x=x;this.y=y;width=w;}
    }
    private Event event(String type,int button,int point){return new Event(type,button,point%2==0?20:180,point*4,256);}
    private InputQueue<Event> queue(int capacity){return new InputQueue<>(capacity,e->e.type,e->e.button,(a,b)->a.width==b.width);}
    @Test public void hoverFloodKeepsLatestBeforeClickEdges(){
        InputQueue<Event> q=queue(64);Event latest=null,down=event("down",1,0),up=event("up",1,0);
        for(int i=0;i<10000;i++){latest=event("move",0,i);assertTrue(q.offer(latest));}
        assertTrue(q.offer(down));assertTrue(q.offer(up));assertEquals(3,q.size());
        assertSame(latest,q.removeFirst());assertSame(down,q.removeFirst());assertSame(up,q.removeFirst());
    }
    @Test public void dequeuedDownStillProtectsEveryZigzagVertex(){
        InputQueue<Event> q=queue(64);Event down=event("down",1,0);assertTrue(q.offer(down));assertSame(down,q.removeFirst());
        Event[] path=new Event[48];for(int i=0;i<path.length;i++){path[i]=event("move",0,i);assertTrue(q.offer(path[i]));}
        Event up=event("up",1,48);assertTrue(q.offer(up));assertEquals(49,q.size());
        for(Event point:path)assertSame(point,q.removeFirst());assertSame(up,q.removeFirst());
        assertTrue(q.offer(event("move",0,1)));assertTrue(q.offer(event("move",0,2)));assertEquals(1,q.size());
    }
    @Test public void partialButtonReleaseDoesNotEnableHover(){
        InputQueue<Event> q=queue(64);q.offer(event("down",1,0));q.offer(event("down",2,0));q.offer(event("up",1,0));
        while(!q.isEmpty())q.removeFirst();q.offer(event("move",0,1));q.offer(event("move",0,2));assertEquals(2,q.size());
        q.clear();q.offer(event("move",0,1));q.offer(event("move",0,2));assertEquals(1,q.size());
    }
    @Test public void fullDragFailsWithoutChangingStateOrEvictingVertices(){
        InputQueue<Event> q=queue(3);Event down=event("down",1,0);q.offer(down);q.removeFirst();
        Event a=event("move",0,1),b=event("move",0,2),c=event("move",0,3),d=event("move",0,4);
        assertTrue(q.offer(a));assertTrue(q.offer(b));assertTrue(q.offer(c));assertFalse(q.offer(event("up",1,4)));
        assertSame(a,q.removeFirst());assertTrue(q.offer(d));assertEquals(3,q.size());
        assertSame(b,q.removeFirst());assertSame(c,q.removeFirst());assertSame(d,q.removeFirst());
    }
    @Test public void criticalEventsAndCoordinateChangesFenceHover(){
        InputQueue<Event> q=queue(4);Event a=event("move",0,1),key=event("key_down",0,0),b=event("move",0,2),c=new Event("move",0,1,1,512);
        assertTrue(q.offer(a));assertTrue(q.offer(key));assertTrue(q.offer(b));assertTrue(q.offer(c));assertFalse(q.offer(event("key_up",0,0)));
        assertSame(a,q.removeFirst());assertSame(key,q.removeFirst());assertSame(b,q.removeFirst());assertSame(c,q.removeFirst());
    }
}
