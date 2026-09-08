package cn.yucg.yudesk;

import java.util.function.Consumer;

/**
 * UI-thread-only ownership of one published frame. Android's RenderThread can
 * retain a published Bitmap beyond the Java onDraw call, so it must never be
 * recycled manually, including when replaced or the window closes. The GC and
 * graphics framework release that storage once all render references expire.
 */
final class FrameSlot<T> {
    private final Consumer<T> recycleUnpublished;
    private T current;
    private boolean closed;
    FrameSlot(Consumer<T> discard) { recycleUnpublished=discard; }
    T current() { return current; }
    boolean publish(T value) {
        if(closed){discardUnpublished(value);return false;}
        current=value;
        return true;
    }
    void discardUnpublished(T value) { if(value!=null)recycleUnpublished.accept(value); }
    void close() { closed=true;current=null; }
}
