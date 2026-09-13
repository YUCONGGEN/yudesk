package cn.yucg.yudesk;

/** Keeps recoverable JNI/network failures from escaping Android component threads. */
final class FailureBoundary {
    private FailureBoundary() {}

    static boolean recoverable(Throwable failure) {
        // Network/JNI bridge failures are normally Exceptions. A missing ABI
        // is also presented as a recoverable startup error, but programmer
        // assertions and other linkage/VM Errors must never be hidden.
        return failure instanceof Exception || failure instanceof UnsatisfiedLinkError;
    }

    static String message(Throwable failure, String fallback) {
        if (failure == null) return fallback;
        String value = failure.getMessage();
        return value == null || value.trim().isEmpty() ? fallback : value.trim();
    }

    static void runQuietly(Runnable action) {
        try {
            action.run();
        } catch (Throwable failure) {
            if (!recoverable(failure)) throw (Error) failure;
        }
    }
}
