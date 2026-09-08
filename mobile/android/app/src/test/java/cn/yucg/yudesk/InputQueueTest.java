package cn.yucg.yudesk;

import org.junit.Test;
import static org.junit.Assert.*;

public class InputQueueTest {
    @Test public void longDragKeepsLatestMoveAndBothButtonEdges(){
        InputQueue<String> q=new InputQueue<>(64,v->v.startsWith("move"));
        assertTrue(q.offer("down"));for(int i=0;i<10000;i++)assertTrue(q.offer("move"+i));assertTrue(q.offer("up"));
        assertEquals(3,q.size());assertEquals("down",q.removeFirst());assertEquals("move9999",q.removeFirst());assertEquals("up",q.removeFirst());assertTrue(q.isEmpty());
    }
    @Test public void criticalBoundaryPreventsMoveCoalescingAcrossIt(){
        InputQueue<String> q=new InputQueue<>(4,v->v.startsWith("move"));
        assertTrue(q.offer("move1"));assertTrue(q.offer("key_down"));assertTrue(q.offer("move2"));assertTrue(q.offer("key_up"));assertFalse(q.offer("down"));
        assertEquals("move1",q.removeFirst());assertEquals("key_down",q.removeFirst());assertEquals("move2",q.removeFirst());assertEquals("key_up",q.removeFirst());
        q.clear();assertTrue(q.isEmpty());
    }
}
