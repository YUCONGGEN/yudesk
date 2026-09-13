package cn.yucg.yudesk;

import static org.junit.Assert.*;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.CountDownLatch;
import org.junit.Test;

public final class ProjectionLaunchCoordinatorTest {
    @Test public void grantRunsOnlyAfterMatchingForegroundOwnerIsReady() {
        ProjectionLaunchCoordinator coordinator = new ProjectionLaunchCoordinator();
        AtomicInteger ready = new AtomicInteger();
        AtomicInteger failed = new AtomicInteger();
        long request = coordinator.prepare(7, callback(ready, failed));

        assertNull(coordinator.ready(request, 8));
        assertEquals(1, coordinator.pendingCount());
        Runnable dispatch = coordinator.ready(request, 7);
        assertNotNull(dispatch);
        dispatch.run();
        assertEquals(1, ready.get());
        assertEquals(0, failed.get());
        assertNull(coordinator.ready(request, 7));
        assertEquals(0, coordinator.pendingCount());
    }

    @Test public void cancellationAndOwnerCleanupAreOneShot() {
        ProjectionLaunchCoordinator coordinator = new ProjectionLaunchCoordinator();
        AtomicInteger ready = new AtomicInteger();
        AtomicInteger failed = new AtomicInteger();
        long first = coordinator.prepare(11, callback(ready, failed));
        coordinator.prepare(11, callback(ready, failed));
        coordinator.prepare(12, callback(ready, failed));

        Runnable cancelled = coordinator.cancel(first, "start failed");
        assertNotNull(cancelled);
        cancelled.run();
        assertNull(coordinator.cancel(first, "again"));

        List<Runnable> callbacks = new ArrayList<>();
        assertEquals(1, coordinator.cancelOwner(11, "closed", callbacks));
        callbacks.forEach(Runnable::run);
        assertEquals(2, failed.get());
        assertEquals(1, coordinator.pendingCount());
        assertEquals(0, ready.get());
    }

    @Test public void serviceReadyAndTimeoutRaceStillDeliverOnce() throws Exception {
        for (int attempt = 0; attempt < 100; attempt++) {
            ProjectionLaunchCoordinator coordinator = new ProjectionLaunchCoordinator();
            AtomicInteger ready = new AtomicInteger();
            AtomicInteger failed = new AtomicInteger();
            long request = coordinator.prepare(21, callback(ready, failed));
            CountDownLatch start = new CountDownLatch(1);
            Thread service = new Thread(() -> { await(start); Runnable next = coordinator.ready(request, 21); if (next != null) next.run(); });
            Thread timeout = new Thread(() -> { await(start); Runnable next = coordinator.cancel(request, "timeout"); if (next != null) next.run(); });
            service.start(); timeout.start(); start.countDown(); service.join(); timeout.join();
            assertEquals(1, ready.get() + failed.get());
            assertEquals(0, coordinator.pendingCount());
        }
    }

    private static ProjectionLaunchCoordinator.Callback callback(AtomicInteger ready, AtomicInteger failed) {
        return new ProjectionLaunchCoordinator.Callback() {
            @Override public void ready() { ready.incrementAndGet(); }
            @Override public void failed(String reason) { failed.incrementAndGet(); }
        };
    }

    private static void await(CountDownLatch latch) {
        try { latch.await(); } catch (InterruptedException interrupted) { Thread.currentThread().interrupt(); }
    }
}
