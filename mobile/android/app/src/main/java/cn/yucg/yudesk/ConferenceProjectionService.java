package cn.yucg.yudesk;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.os.Build;
import android.os.IBinder;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.atomic.AtomicLong;

/**
 * Owns the foreground-service state required before WebRTC opens a
 * MediaProjection grant. Activity results are released only after
 * startForeground() has returned successfully; fixed timing delays are never
 * used.
 */
public final class ConferenceProjectionService extends Service {
    static final String ACTION_STOP = "cn.yucg.yudesk.CONFERENCE_SHARE_STOP";
    private static final String CHANNEL = "yudesk-conference-sharing";
    private static final String EXTRA_OWNER = "owner";
    private static final String EXTRA_REQUEST = "request";
    private static final String EXTRA_RECORDING = "recording";
    private static final ProjectionLaunchCoordinator LAUNCHES = new ProjectionLaunchCoordinator();
    private static final AtomicLong NEXT_OWNER = new AtomicLong(1);
    private static final Map<Long, Runnable> STOP_CALLBACKS = new HashMap<>();

    private long activeOwner;
    private boolean stopNotified;

    static synchronized long registerOwner(Runnable onStopped) {
        long owner = NEXT_OWNER.getAndIncrement();
        if (owner <= 0) {
            NEXT_OWNER.set(2);
            owner = 1;
        }
        STOP_CALLBACKS.put(owner, onStopped);
        return owner;
    }

    static void unregisterOwner(long owner) {
        List<Runnable> cancelled = new ArrayList<>();
        synchronized (ConferenceProjectionService.class) {
            STOP_CALLBACKS.remove(owner);
        }
        LAUNCHES.cancelOwner(owner, "会议已结束", cancelled);
        for (Runnable callback : cancelled) callback.run();
    }

    static long prepareLaunch(long owner, ProjectionLaunchCoordinator.Callback callback) {
        synchronized (ConferenceProjectionService.class) {
            if (!STOP_CALLBACKS.containsKey(owner)) throw new IllegalStateException("会议共享会话已结束");
        }
        return LAUNCHES.prepare(owner, callback);
    }

    static Intent launchIntent(MainActivity activity, long owner, long request, boolean recording) {
        return new Intent(activity, ConferenceProjectionService.class)
                .putExtra(EXTRA_OWNER, owner)
                .putExtra(EXTRA_REQUEST, request)
                .putExtra(EXTRA_RECORDING, recording);
    }

    static void cancelLaunch(long request, String reason) {
        dispatch(LAUNCHES.cancel(request, reason));
    }

    private static void dispatch(Runnable callback) {
        if (callback != null) FailureBoundary.runQuietly(callback);
    }

    @Override public void onCreate() {
        super.onCreate();
        NotificationManager manager = getSystemService(NotificationManager.class);
        manager.createNotificationChannel(new NotificationChannel(CHANNEL, "会议屏幕共享", NotificationManager.IMPORTANCE_LOW));
    }

    @Override public int onStartCommand(Intent intent, int flags, int startId) {
        if (intent == null) {
            stopSelfResult(startId);
            return START_NOT_STICKY;
        }
        if (ACTION_STOP.equals(intent.getAction())) {
            notifyOwnerStopped();
            stopSelfResult(startId);
            return START_NOT_STICKY;
        }

        long owner = intent.getLongExtra(EXTRA_OWNER, 0);
        long request = intent.getLongExtra(EXTRA_REQUEST, 0);
        boolean recording = intent.getBooleanExtra(EXTRA_RECORDING, false);
        if (owner <= 0 || request <= 0) {
            dispatch(LAUNCHES.cancel(request, "屏幕共享请求无效"));
            stopSelfResult(startId);
            return START_NOT_STICKY;
        }
        synchronized (ConferenceProjectionService.class) {
            if (!STOP_CALLBACKS.containsKey(owner)) {
                dispatch(LAUNCHES.cancel(request, "会议共享会话已结束"));
                stopSelfResult(startId);
                return START_NOT_STICKY;
            }
        }

        try {
            boolean alreadyActive = activeOwner > 0;
            Notification notification = notification(recording);
            if (Build.VERSION.SDK_INT >= 29) Api29.startForeground(this, notification);
            else startForeground(17, notification);
            activeOwner = owner;
            stopNotified = false;
            Runnable ready = LAUNCHES.ready(request, owner);
            dispatch(ready);
            if (ready == null && !alreadyActive) stopSelfResult(startId);
        } catch (Throwable failure) {
            if (!FailureBoundary.recoverable(failure)) throw (Error) failure;
            dispatch(LAUNCHES.cancel(request, "前台共享服务启动失败：" + FailureBoundary.message(failure, "系统拒绝启动")));
            stopSelfResult(startId);
        }
        return START_NOT_STICKY;
    }

    private Notification notification(boolean recording) {
        PendingIntent open = PendingIntent.getActivity(this, 31,
                new Intent(this, MainActivity.class).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_SINGLE_TOP),
                PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE);
        PendingIntent stop = PendingIntent.getService(this, 32,
                new Intent(this, ConferenceProjectionService.class).setAction(ACTION_STOP),
                PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE);
        return new Notification.Builder(this, CHANNEL)
                .setSmallIcon(R.drawable.ic_notification)
                .setContentTitle(recording ? "YuDesk · 正在录制会议" : "YuDesk · 正在共享会议屏幕")
                .setContentText(recording ? "点击返回会议，或立即停止录制。" : "点击返回会议，或立即停止共享。")
                .setContentIntent(open)
                .setOngoing(true)
                .addAction(new Notification.Action.Builder(null, recording ? "停止录制" : "停止共享", stop).build())
                .build();
    }

    private void notifyOwnerStopped() {
        if (stopNotified || activeOwner <= 0) return;
        stopNotified = true;
        Runnable callback;
        synchronized (ConferenceProjectionService.class) {
            callback = STOP_CALLBACKS.get(activeOwner);
        }
        dispatch(callback);
    }

    @Override public void onTaskRemoved(Intent rootIntent) {
        notifyOwnerStopped();
        stopSelf();
    }

    @Override public void onDestroy() {
        notifyOwnerStopped();
        stopForeground(STOP_FOREGROUND_REMOVE);
        super.onDestroy();
    }

    @Override public IBinder onBind(Intent intent) { return null; }

    @android.annotation.TargetApi(29)
    @android.annotation.SuppressLint("UseRequiresApi")
    private static final class Api29 {
        static void startForeground(Service service, Notification notification) {
            service.startForeground(17, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PROJECTION);
        }
    }
}
