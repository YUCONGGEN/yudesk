package cn.yucg.yudesk;

import java.util.ArrayDeque;
import java.util.function.Function;
import java.util.function.ToIntFunction;
import java.util.function.BiPredicate;

/** UI-thread-only queue: replace only hover, retaining every admitted drag point. */
final class InputQueue<T> {
    private static final class Entry<T> {
        final T value;final boolean hover;
        Entry(T value,boolean hover){this.value=value;this.hover=hover;}
    }
    private final ArrayDeque<Entry<T>> values=new ArrayDeque<>();
    private final int capacity;
    private final Function<T,String> type;
    private final ToIntFunction<T> button;
    private final BiPredicate<T,T> sameSpace;
    private int buttons;
    InputQueue(int limit,Function<T,String> eventType,ToIntFunction<T> eventButton,BiPredicate<T,T> geometry){
        if(limit<1)throw new IllegalArgumentException("capacity");capacity=limit;type=eventType;button=eventButton;sameSpace=geometry;
    }
    boolean offer(T value){
        String kind=type.apply(value);boolean hover=buttons==0&&kind.equals("move");
        Entry<T> last=values.peekLast(),entry=new Entry<>(value,hover);
        if(last!=null&&last.hover&&hover&&sameSpace.test(last.value,value)){values.removeLast();values.addLast(entry);return true;}
        if(values.size()>=capacity)return false;
        values.addLast(entry);
        int b=button.applyAsInt(value);
        if(b>=1&&b<=3){if(kind.equals("down"))buttons|=1<<b;else if(kind.equals("up"))buttons&=~(1<<b);}
        return true;
    }
    boolean isEmpty(){return values.isEmpty();}
    int size(){return values.size();}
    T removeFirst(){return values.removeFirst().value;}
    void clear(){values.clear();buttons=0;}
}
