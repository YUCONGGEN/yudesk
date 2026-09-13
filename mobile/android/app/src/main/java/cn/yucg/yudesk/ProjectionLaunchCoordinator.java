package cn.yucg.yudesk;

import java.util.HashMap;
import java.util.Map;

/**
 * One-shot hand-off between an Activity result and the media-projection
 * foreground service. It is Android-free so ordering and stale callbacks can
 * be covered by local unit tests.
 */
final class ProjectionLaunchCoordinator {
    interface Callback {
        void ready();
        void failed(String reason);
    }

    private static final class Pending {
        final long owner;
        final Callback callback;
        Pending(long owner, Callback callback) { this.owner = owner; this.callback = callback; }
    }

    private final Map<Long, Pending> pending = new HashMap<>();
    private long nextRequest = 1;

    synchronized long prepare(long owner, Callback callback) {
        if (owner <= 0) throw new IllegalArgumentException("owner");
        if (callback == null) throw new IllegalArgumentException("callback");
        long request = nextRequest++;
        if (request <= 0) { nextRequest = 2; request = 1; }
        pending.put(request, new Pending(owner, callback));
        return request;
    }

    synchronized Runnable ready(long request, long owner) {
        Pending value = pending.get(request);
        if (value == null || value.owner != owner) return null;
        pending.remove(request);
        return value.callback::ready;
    }

    synchronized Runnable cancel(long request, String reason) {
        Pending value = pending.remove(request);
        return value == null ? null : () -> value.callback.failed(reason);
    }

    synchronized int cancelOwner(long owner, String reason, java.util.List<Runnable> callbacks) {
        int count = 0;
        for (Long request : new java.util.ArrayList<>(pending.keySet())) {
            Pending value = pending.get(request);
            if (value != null && value.owner == owner) {
                pending.remove(request);
                callbacks.add(() -> value.callback.failed(reason));
                count++;
            }
        }
        return count;
    }

    synchronized int pendingCount() { return pending.size(); }
}
