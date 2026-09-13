package cn.yucg.yudesk;

import android.app.Application;
import android.os.Build;
import cn.yucg.bridge.core.Core;
import cn.yucg.bridge.core.Engine;
import cn.yucg.bridge.core.Session;
import org.json.JSONObject;

public final class YuDeskApp extends Application {
    private Engine engine;
    volatile Session session;
    volatile String notice = "";
    volatile boolean uiVisible;
    volatile boolean sharing;
    volatile boolean sharingStarting;

    @Override public void onCreate() { super.onCreate();go.Seq.setContext(this); }

    public synchronized Engine engine() throws Exception {
        if (engine == null) {
            engine = Core.newEngine(getFilesDir().getAbsolutePath(), Build.MANUFACTURER + " " + Build.MODEL);
            engine.setAccessibility(RemoteAccessibilityService.available());
        }
        return engine;
    }
    public synchronized Engine existingEngine() { return engine; }
    public JSONObject state() {
        try { Engine value = existingEngine(); return value == null ? new JSONObject() : new JSONObject(value.statusJSON()); }
        catch (Throwable failure) { if (!FailureBoundary.recoverable(failure)) throw (Error) failure; return new JSONObject(); }
    }
    public void endSession() {
        Session value = session; session = null;
        if (value != null) {
            FailureBoundary.runQuietly(value::releaseInput);
            FailureBoundary.runQuietly(value::close);
        }
    }
    public void releaseSessionInput() {
        Session value = session;
        if (value != null) FailureBoundary.runQuietly(value::releaseInput);
    }
    public synchronized void closeEngine() {
        endSession();
        Engine value = engine; engine = null;
        if (value != null) FailureBoundary.runQuietly(value::close);
    }
}
