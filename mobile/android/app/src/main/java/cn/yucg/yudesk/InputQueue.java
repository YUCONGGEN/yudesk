package cn.yucg.yudesk;

import java.util.ArrayDeque;
import java.util.function.Predicate;

/** UI-thread-only queue: replace consecutive pending moves, never button/key edges. */
final class InputQueue<T> {
    private final ArrayDeque<T> values=new ArrayDeque<>();
    private final int capacity;
    private final Predicate<T> isMove;
    InputQueue(int limit,Predicate<T> move){if(limit<1)throw new IllegalArgumentException("capacity");capacity=limit;isMove=move;}
    boolean offer(T value){
        T last=values.peekLast();
        if(last!=null&&isMove.test(last)&&isMove.test(value)){values.removeLast();values.addLast(value);return true;}
        if(values.size()>=capacity)return false;
        values.addLast(value);return true;
    }
    boolean isEmpty(){return values.isEmpty();}
    int size(){return values.size();}
    T removeFirst(){return values.removeFirst();}
    void clear(){values.clear();}
}
