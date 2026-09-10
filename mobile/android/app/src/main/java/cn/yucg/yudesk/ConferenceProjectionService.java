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

/**
 * Foreground ownership required by Android before WebRTC consumes a
 * MediaProjection grant. The actual projection remains in ConferenceController
 * so one consent token is never opened twice.
 */
public final class ConferenceProjectionService extends Service {
    static final String ACTION_STOP = "cn.yucg.yudesk.CONFERENCE_SHARE_STOP";
    private static final String CHANNEL = "yudesk-conference-sharing";
    static volatile Runnable onStopped;

    @Override public void onCreate() {
        super.onCreate();
        NotificationManager manager = getSystemService(NotificationManager.class);
        manager.createNotificationChannel(new NotificationChannel(CHANNEL, "会议屏幕共享", NotificationManager.IMPORTANCE_LOW));
    }

    @Override public int onStartCommand(Intent intent, int flags, int startId) {
        if (intent == null || ACTION_STOP.equals(intent.getAction())) {
            stopSelf();
            return START_NOT_STICKY;
        }
        PendingIntent open = PendingIntent.getActivity(this, 31,
                new Intent(this, MainActivity.class).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
                PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE);
        PendingIntent stop = PendingIntent.getService(this, 32,
                new Intent(this, ConferenceProjectionService.class).setAction(ACTION_STOP),
                PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE);
        boolean recording=intent.getBooleanExtra("recording",false);
        Notification notification = new Notification.Builder(this, CHANNEL)
                .setSmallIcon(R.drawable.ic_notification)
                .setContentTitle(recording?"YuDesk · 正在录制会议":"YuDesk · 正在共享会议屏幕")
                .setContentText(recording?"点击返回会议，或立即停止录制。":"点击返回会议，或立即停止共享。")
                .setContentIntent(open)
                .setOngoing(true)
                .addAction(new Notification.Action.Builder(null, recording?"停止录制":"停止共享", stop).build())
                .build();
        if (Build.VERSION.SDK_INT >= 29) {
            startForeground(17, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PROJECTION);
        } else {
            startForeground(17, notification);
        }
        return START_NOT_STICKY;
    }

    @Override public void onTaskRemoved(Intent rootIntent) { stopSelf(); }

    @Override public void onDestroy() {
        stopForeground(STOP_FOREGROUND_REMOVE);
        Runnable callback=onStopped;if(callback!=null)callback.run();
        super.onDestroy();
    }

    @Override public IBinder onBind(Intent intent) { return null; }
}
