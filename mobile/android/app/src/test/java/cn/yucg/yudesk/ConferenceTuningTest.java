package cn.yucg.yudesk;

import org.junit.Test;
import static org.junit.Assert.*;

public class ConferenceTuningTest {
    @Test public void cameraBudgetScalesWithMeshSizeAndDelay(){
        assertEquals(1_500_000,ConferenceTuning.videoBitrate(false,1,20,0));
        assertEquals(1_000_000,ConferenceTuning.videoBitrate(false,4,20,0));
        assertEquals(700_000,ConferenceTuning.videoBitrate(false,4,220,0));
    }
    @Test public void availableBandwidthCapsVideoButKeepsInteractiveFloor(){
        assertEquals(656_000,ConferenceTuning.videoBitrate(false,1,20,800_000));
        assertEquals(250_000,ConferenceTuning.videoBitrate(true,1,350,200_000));
    }
    @Test public void updateHasHysteresisButModeChangeIsImmediate(){
        assertFalse(ConferenceTuning.needsUpdate(1_000_000,1_090_000,false,false));
        assertTrue(ConferenceTuning.needsUpdate(1_000_000,800_000,false,false));
        assertTrue(ConferenceTuning.needsUpdate(1_000_000,1_000_000,false,true));
    }
}
