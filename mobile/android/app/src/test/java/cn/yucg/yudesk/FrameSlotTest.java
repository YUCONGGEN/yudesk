package cn.yucg.yudesk;

import org.junit.Test;
import java.util.ArrayList;
import java.util.List;
import java.util.HashSet;
import java.util.Set;
import java.util.concurrent.ConcurrentLinkedQueue;
import static org.junit.Assert.*;

public class FrameSlotTest {
    @Test(timeout=5000) public void concurrentDecodeAndUIHaveExclusiveBitmapOwnership() throws Exception {
        ConcurrentLinkedQueue<Integer> recycled=new ConcurrentLinkedQueue<>();
        ConcurrentLinkedQueue<Boolean> callbacks=new ConcurrentLinkedQueue<>();
        FrameSlot<Integer> slot=new FrameSlot<>(recycled::add);Set<Integer> published=new HashSet<>();
        Thread decoder=new Thread(()->{for(int i=0;i<10000;i++)if(slot.offer(i))callbacks.add(true);});decoder.start();
        while(decoder.isAlive()||!callbacks.isEmpty()){
            if(callbacks.poll()!=null){if(slot.publishPending())published.add(slot.current());}else Thread.yield();
        }
        decoder.join();assertEquals(Integer.valueOf(9999),slot.current());slot.close();
        Set<Integer> discarded=new HashSet<>(recycled);assertEquals(recycled.size(),discarded.size());
        assertEquals(10000,published.size()+discarded.size());
        for(Integer value:published)assertFalse(discarded.contains(value));
    }
    @Test public void blockedUIKeepsOnlyNewestDecodeAndOneCallback() {
        List<Integer> recycled=new ArrayList<>();FrameSlot<Integer> slot=new FrameSlot<>(recycled::add);
        slot.publish(-1);int callbacks=0;
        for(int i=0;i<10000;i++)if(slot.offer(i))callbacks++;
        assertEquals(1,callbacks);assertEquals(9999,recycled.size());assertEquals(Integer.valueOf(-1),slot.current());
        assertTrue(slot.publishPending());assertEquals(Integer.valueOf(9999),slot.current());assertFalse(slot.publishPending());
        assertTrue(slot.offer(10000));assertTrue(slot.publishPending());slot.close();
        assertFalse(recycled.contains(-1));assertFalse(recycled.contains(9999));assertFalse(recycled.contains(10000));
    }
    @Test public void stopBeforeUICallbackDiscardsPendingExactlyOnce() {
        List<Object> recycled=new ArrayList<>();FrameSlot<Object> slot=new FrameSlot<>(recycled::add);
        Object published=new Object(),pending=new Object(),late=new Object();slot.publish(published);
        assertTrue(slot.offer(pending));slot.close();slot.close();assertFalse(slot.publishPending());assertFalse(slot.offer(late));
        assertEquals(2,recycled.size());assertSame(pending,recycled.get(0));assertSame(late,recycled.get(1));assertNull(slot.current());
    }
    @Test public void replacementNeverRecyclesPublishedFrames() {
        List<Object> recycled=new ArrayList<>();FrameSlot<Object> slot=new FrameSlot<>(recycled::add);
        Object previous=null;
        for(int i=0;i<10000;i++){Object next=new Object();assertTrue(slot.publish(next));assertSame(next,slot.current());assertNotSame(previous,slot.current());previous=next;}
        assertTrue(recycled.isEmpty());slot.close();assertNull(slot.current());assertTrue(recycled.isEmpty());
    }
    @Test public void closeDiscardsOnlyFramesThatNeverReachedTheRenderer() {
        List<Object> recycled=new ArrayList<>();FrameSlot<Object> slot=new FrameSlot<>(recycled::add);
        Object published=new Object(),waiting=new Object(),postFailure=new Object();
        slot.publish(published);slot.close();slot.close();assertFalse(slot.publish(waiting));slot.discardUnpublished(postFailure);
        assertEquals(2,recycled.size());assertSame(waiting,recycled.get(0));assertSame(postFailure,recycled.get(1));assertFalse(recycled.contains(published));assertNull(slot.current());
    }
}
