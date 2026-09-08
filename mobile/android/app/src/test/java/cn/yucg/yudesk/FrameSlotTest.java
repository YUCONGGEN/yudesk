package cn.yucg.yudesk;

import org.junit.Test;
import java.util.ArrayList;
import java.util.List;
import static org.junit.Assert.*;

public class FrameSlotTest {
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
