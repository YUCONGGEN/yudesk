package cn.yucg.yudesk;

/** Capture-thread-only trailing sample: keep the existing interval, retain the final update. */
final class CapturePacer {
    private final long interval;
    private long next;
    private boolean scheduled;
    CapturePacer(long intervalMillis){interval=intervalMillis;}
    // A negative delay means a callback already owns the next sample.
    long schedule(long now){if(scheduled)return -1;scheduled=true;return Math.max(0,next-now);}
    void begin(){scheduled=false;}
    void captured(long now){next=now+interval;}
    void reset(){scheduled=false;next=0;}
}
