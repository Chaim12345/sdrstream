package com.chaim12345.sdrstream;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Intent;
import android.os.IBinder;
import android.os.PowerManager;

import java.io.File;
import java.io.IOException;
import java.util.Map;

/**
 * Runs the sdrstream Go engine (the same binary that runs on servers,
 * cross-compiled GOOS=android/arm64 and shipped inside the APK as
 * jniLibs/arm64-v8a/libsdrstreamd.so) as a foreground service.
 *
 * Android 10+ blocks exec() of files in app-private data dirs, but native
 * libraries extracted to nativeLibraryDir stay executable - that is why the
 * binary is packaged under the lib*.so name.
 */
public class EngineService extends Service {
    public static final String ACTION_START = "com.chaim12345.sdrstream.START";
    public static final String ACTION_STOP = "com.chaim12345.sdrstream.STOP";
    static final int PORT = 8080;
    private static final String CHANNEL_ID = "engine";
    private static final int NOTIF_ID = 1;

    private Process process;
    private PowerManager.WakeLock wakeLock;

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        if (intent != null && ACTION_STOP.equals(intent.getAction())) {
            stopEngine();
            stopSelf();
            return START_NOT_STICKY;
        }
        startForeground(NOTIF_ID, buildNotification("starting engine"));
        startEngine();
        return START_STICKY;
    }

    private void startEngine() {
        if (process != null) return;
        String enginePath = getApplicationInfo().nativeLibraryDir + "/libsdrstreamd.so";
        File engine = new File(enginePath);
        if (!engine.canExecute()) {
            updateNotification("engine binary missing - reinstall the app");
            return;
        }
        File files = getFilesDir();
        File state = new File(files, "state");
        //noinspection ResultOfMethodCallIgnored
        state.mkdirs();

        ProcessBuilder pb = new ProcessBuilder(enginePath, "web",
                "--host", "127.0.0.1", "--port", String.valueOf(PORT),
                "--scan-open-directory", "--scan-kiwi-directory");
        Map<String, String> env = pb.environment();
        env.put("HOME", files.getAbsolutePath());
        env.put("SDRSTREAM_STATE_DIR", state.getAbsolutePath());
        String password = Prefs.getUiPassword(this);
        if (password != null && !password.isEmpty()) {
            env.put("SDRSTREAM_UI_PASSWORD", password);
        }
        pb.redirectErrorStream(true);
        pb.redirectOutput(new File(files, "engine.log"));
        try {
            process = pb.start();
        } catch (IOException e) {
            updateNotification("failed to start engine: " + e.getMessage());
            return;
        }
        PowerManager pm = (PowerManager) getSystemService(POWER_SERVICE);
        wakeLock = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "sdrstream:engine");
        wakeLock.acquire();
        updateNotification("monitoring - UI on 127.0.0.1:" + PORT);
        // Restart the engine if it dies.
        new Thread(() -> {
            try {
                process.waitFor();
            } catch (InterruptedException ignored) {
            }
            process = null;
            if (wakeLock != null && wakeLock.isHeld()) wakeLock.release();
            updateNotification("engine stopped - tap to restart");
        }, "engine-watchdog").start();
    }

    private void stopEngine() {
        if (process != null) {
            process.destroy();
            process = null;
        }
        if (wakeLock != null && wakeLock.isHeld()) wakeLock.release();
    }

    private Notification buildNotification(String text) {
        NotificationManager nm = getSystemService(NotificationManager.class);
        nm.createNotificationChannel(new NotificationChannel(
                CHANNEL_ID, "sdrstream engine", NotificationManager.IMPORTANCE_LOW));
        PendingIntent open = PendingIntent.getActivity(this, 0,
                new Intent(this, MainActivity.class), PendingIntent.FLAG_IMMUTABLE);
        return new Notification.Builder(this, CHANNEL_ID)
                .setContentTitle("sdrstream")
                .setContentText(text)
                .setSmallIcon(android.R.drawable.stat_sys_download_done)
                .setContentIntent(open)
                .setOngoing(true)
                .build();
    }

    private void updateNotification(String text) {
        NotificationManager nm = getSystemService(NotificationManager.class);
        nm.notify(NOTIF_ID, buildNotification(text));
    }

    @Override
    public void onDestroy() {
        stopEngine();
        super.onDestroy();
    }

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }
}
