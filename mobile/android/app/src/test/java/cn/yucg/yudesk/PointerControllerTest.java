package cn.yucg.yudesk;

import org.junit.Test;
import static org.junit.Assert.*;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;

public class PointerControllerTest {
    static final class Wire implements PointerController.Sink {
        final List<PointerController.Event> events=new ArrayList<>();
        int releases;
        @Override public void send(PointerController.Event... batch){events.addAll(Arrays.asList(batch));}
        @Override public void release(){releases++;}
        List<String> types(){List<String> types=new ArrayList<>();for(PointerController.Event e:events)types.add(e.type);return types;}
    }
    private PointerController pointer(Wire wire){PointerController p=new PointerController(wire,8);p.geometry(new ScreenMapping(1000,800,2000,1000));return p;}
    @Test public void tapMovesBeforeClickAndReleases() throws Exception {Wire w=new Wire();PointerController p=pointer(w);p.down(20,20,1);p.up(20,20,80);assertEquals(Arrays.asList("move","down","up"),w.types());assertEquals(1,w.events.get(1).button);assertEquals(1000,w.events.get(0).x);}
    @Test public void trackpadMoveDoesNotHoldButton() throws Exception {Wire w=new Wire();PointerController p=pointer(w);p.down(100,100,0);p.move(200,120,16);p.up(220,130,20);assertEquals(Arrays.asList("move","move"),w.types());assertEquals(1240,p.x());assertEquals(560,p.y());}
    @Test public void dragIsOneGestureAndFinalPositionPrecedesRelease() throws Exception {Wire w=new Wire();PointerController p=pointer(w);p.drag(true);p.down(100,100,0);p.move(200,100,1);p.up(220,100,2);assertEquals(Arrays.asList("move","down","move","up"),w.types());assertEquals(1240,w.events.get(3).x);assertFalse(p.dragging());assertFalse(p.active());}
    @Test public void directClickPositionsMouseBeforeDown() throws Exception {Wire w=new Wire();PointerController p=pointer(w);p.mode(false);p.down(250,300,0);p.up(250,300,1);assertEquals(Arrays.asList("move","down","move","up"),w.types());assertEquals(500,w.events.get(0).x);assertEquals(300,w.events.get(0).y);}
    @Test public void directLetterboxIsInert() throws Exception {Wire w=new Wire();PointerController p=pointer(w);p.mode(false);p.down(100,10,0);p.up(100,10,50);assertTrue(w.events.isEmpty());}
    @Test public void cancelAndModeChangeCannotLeaveHeldButton() throws Exception {Wire w=new Wire();PointerController p=pointer(w);p.drag(true);p.down(0,0,0);int releases=w.releases;p.cancel();assertEquals(releases+1,w.releases);assertFalse(p.active());assertFalse(p.dragging());int count=w.events.size();p.up(100,100,50);assertEquals(count,w.events.size());p.mode(false);assertFalse(p.relative());}
    @Test public void rightClickAndScrollUseCorrectProtocol() throws Exception {Wire w=new Wire();PointerController p=pointer(w);p.click(3);assertEquals(3,w.events.get(1).button);p.scroll(-12);assertEquals(3,w.events.size());p.scroll(-12);assertEquals("wheel",w.events.get(4).type);assertEquals(120,w.events.get(4).deltaY);assertEquals("move",w.events.get(3).type);}
    @Test public void rotationPreservesSourcePositionAndScaling() throws Exception {Wire w=new Wire();PointerController p=pointer(w);p.down(100,100,0);p.up(200,100,30);int before=p.x();p.cancel();p.geometry(new ScreenMapping(1800,900,2000,1000));assertEquals(before,p.x());p.down(100,100,40);p.up(190,100,80);assertEquals(before+100,p.x());}
    @Test public void resolutionChangeReleasesAndRemapsWithinBounds() throws Exception {Wire w=new Wire();PointerController p=pointer(w);p.drag(true);p.down(0,0,0);p.geometry(new ScreenMapping(500,1000,1000,2000));assertFalse(p.active());assertEquals(500,p.x());assertEquals(1000,p.y());p.down(1,1,1);p.up(100000,100000,100);assertEquals(999,p.x());assertEquals(1999,p.y());}
    @Test public void moveFloodIsBoundedWithoutLosingFinalPosition() throws Exception {Wire w=new Wire();PointerController p=pointer(w);p.down(0,0,0);for(int i=0;i<10000;i++)p.move(i/10f,20,i/10);p.up(999.9f,20,1000);assertTrue(w.events.size()<=65);assertEquals(1999,p.x());assertFalse(w.types().contains("down"));}
}
