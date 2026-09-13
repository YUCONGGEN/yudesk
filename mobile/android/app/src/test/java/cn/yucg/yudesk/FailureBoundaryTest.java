package cn.yucg.yudesk;

import static org.junit.Assert.*;
import org.junit.Test;

public final class FailureBoundaryTest {
    @Test public void bridgeAndLinkageFailuresAreRecoverable() {
        assertTrue(FailureBoundary.recoverable(new RuntimeException("offline")));
        assertTrue(FailureBoundary.recoverable(new UnsatisfiedLinkError("missing ABI")));
        assertFalse(FailureBoundary.recoverable(new AssertionError("programming error")));
        assertFalse(FailureBoundary.recoverable(new OutOfMemoryError("fatal")));
        assertFalse(FailureBoundary.recoverable(new ThreadDeath()));
    }

    @Test public void emptyFailureMessageUsesStableFallback() {
        assertEquals("连接已断开", FailureBoundary.message(new RuntimeException("  "), "连接已断开"));
        assertEquals("offline", FailureBoundary.message(new RuntimeException(" offline "), "连接已断开"));
    }

    @Test public void quietCleanupContainsRuntimeFailureButNotVmFailure() {
        FailureBoundary.runQuietly(() -> { throw new IllegalStateException("already released"); });
        try {
            FailureBoundary.runQuietly(() -> { throw new OutOfMemoryError("fatal"); });
            fail("VM failures must not be hidden");
        } catch (OutOfMemoryError expected) {
            assertEquals("fatal", expected.getMessage());
        }
    }
}
