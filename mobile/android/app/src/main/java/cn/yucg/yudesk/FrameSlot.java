package cn.yucg.yudesk;

import java.util.function.Consumer;

/**
 * One published frame and one replaceable pending frame. Android's RenderThread can
 * retain a published Bitmap beyond the Java onDraw call, so it must never be
 * recycled manually, including when replaced or the window closes. The GC and
 * graphics framework release that storage once all render references expire.
 */
final class FrameSlot<T> {
    private final Consumer<T> recycleUnpublished;
    private T current,pending;
    private boolean closed,scheduled;
    FrameSlot(Consumer<T> discard) { recycleUnpublished=discard; }
    synchronized T current() { return current; }
    synchronized boolean publish(T value) {
        if(closed){discardUnpublished(value);return false;}
        current=value;
        return true;
    }
    // Decoder thread: true schedules the sole UI callback. Replaced pending
    // bitmaps have never reached Canvas and may be recycled immediately.
    synchronized boolean offer(T value) {
        if(closed){discardUnpublished(value);return false;}
        discardUnpublished(pending);pending=value;
        if(scheduled)return false;
        scheduled=true;return true;
    }
    synchronized boolean publishPending() {
        scheduled=false;
        if(closed||pending==null)return false;
        T value=pending;pending=null;
        return publish(value);
    }
    void discardUnpublished(T value) { if(value!=null)recycleUnpublished.accept(value); }
    synchronized void close() { closed=true;current=null;discardUnpublished(pending);pending=null; }
}
