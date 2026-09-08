package cn.yucg.yudesk;

import org.junit.Test;
import static org.junit.Assert.*;

public class CapturePacerTest {
    @Test public void finalUpdateInsideIntervalGetsOneTrailingSample(){
        CapturePacer p=new CapturePacer(33);
        assertEquals(0,p.schedule(100));p.begin();p.captured(100);
        // The display changes once, then stays still. No further callback is
        // needed to capture its final image at the existing 33 ms deadline.
        assertEquals(25,p.schedule(108));
        for(int i=0;i<10000;i++)assertEquals(-1,p.schedule(109));
        p.begin();p.captured(133);assertEquals(0,p.schedule(200));
    }
    @Test public void slowCaptureDoesNotAddAnotherIntervalAndResizeResets(){
        CapturePacer p=new CapturePacer(33);p.schedule(100);p.begin();p.captured(100);
        assertEquals(0,p.schedule(150));p.reset();assertEquals(0,p.schedule(151));
    }
}
