package cn.yucg.yudesk;
import org.junit.Test;
import static org.junit.Assert.*;
public class ScreenMappingTest {
    @Test public void fullFrameHasNoLetterboxOrCroppedEdges() {
        ScreenMapping m = new ScreenMapping(1080,1920,1920,1080);
		assertEquals(0,m.left,0);assertEquals(0,m.top,0);assertEquals(1080,m.width,0);assertEquals(1920,m.height,0);assertTrue(m.contains(10,10));assertTrue(m.contains(540,960));
        assertEquals(960,m.x(540));assertEquals(540,m.y(960));
        assertEquals(1919,m.x(9999));assertEquals(0,m.y(-1));
    }
    @Test public void portraitFit() {
        ScreenMapping m = new ScreenMapping(400,800,1080,2160);
        assertEquals(0,m.left,0);assertEquals(0,m.top,0);
        assertTrue(m.contains(399,799));assertFalse(m.contains(400,800));
    }
}
