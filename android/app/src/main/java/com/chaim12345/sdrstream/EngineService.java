package com.chaim12345.sdrstream;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.os.Build;
import android.os.IBinder;
import android.os.PowerManager;

import java.io.File;
import java.io.IOException;
import java.util.Map;

/**
 * Runs the sdrstream Go engine (the same binary that runs on servers,
 * cross-compiled GOOS=android/arm64 and shipped inside the APK as
 * jniLibs/<abi>/libsdrstreamd.so) as a foreground service.
 *
 * Android 10+ blocks exec() of files in app-private data dirs, but native
 * libraries extracted to nativeLibraryDir stay executable - that is why the
 * binary is packaged under the lib*.so name.
 *
 * Every failure path is recorded via Diag and shown in the notification, so
 * a dead engine is never silent.
 */
public class EngineService extends Service {
    public static final String ACTION_START = "com.chaim12345.sdrstream.START";
    public static final String ACTION_STOP = "com.chaim12345.sdrstream.STOP";
    static final int PORT = 8080;
    private static final String CHANNEL_ID = "engine";
    private static final int NOTIF_ID = 1;

    private Process process;
    private PowerManager.WakeLock wakeLock;
    private volatile boolean stopping;

    @Override
    public void onCreate() {
        super.onCreate();
        try {
            createChannel();
        } catch (Throwable t) {
            Diag.writeCrash(this, "create notification channel", t);
        }
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        if (intent != null && ACTION_STOP.equals(intent.getAction())) {
            stopping = true;
            stopEngine();
            stopSelf();
            return START_NOT_STICKY;
        }
        stopping = false;
        try {
            startForegroundNow(buildNotification("starting engine"));
        } catch (Throwable t) {
            // Android 12+ refuses foreground starts from the background
            // (ForegroundServiceStartNotAllowedException), Android 14+ throws
            // on type/permission problems. Record instead of dying silently.
            Diag.writeCrash(this, "EngineService.startForeground", t);
            Diag.recordEngineError(this, "could not go foreground: " + t);
            stopSelf();
            return START_NOT_STICKY;
        }
        startEngine();
        return START_STICKY;
    }

    private void startForegroundNow(Notification notification) {
        if (Build.VERSION.SDK_INT >= 29) {
            startForeground(NOTIF_ID, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC);
        } else {
            startForeground(NOTIF_ID, notification);
        }
    }

    private void startEngine() {
        if (process != null) return;
        String enginePath = getApplicationInfo().nativeLibraryDir + "/libsdrstreamd.so";
        File engine = new File(enginePath);
        if (!engine.canExecute()) {
            String msg = "engine binary not executable: exists=" + engine.exists()
                    + " canRead=" + engine.canRead() + " canExecute=" + engine.canExecute()
                    + " path=" + enginePath;
            Diag.recordEngineError(this, msg);
            updateNotification("engine binary missing - reinstall the app");
            return;
        }
        File files = getFilesDir();
        File state = new File(files, "state");
        //noinspection ResultOfMethodCallIgnored
        state.mkdirs();
        //noinspection ResultOfMethodCallIgnored
        getCacheDir().mkdirs();

        ProcessBuilder pb = new ProcessBuilder(enginePath, "web",
                "--host", "127.0.0.1", "--port", String.valueOf(PORT),
                "--scan-open-directory", "--scan-kiwi-directory");
        // Give the engine a real working directory: without this the child
        // inherits "/" and relative paths (e.g. the default captures dir)
        // resolve outside the sandbox and fail.
        pb.directory(files);
        Map<String, String> env = pb.environment();
        env.put("HOME", files.getAbsolutePath());
        env.put("TMPDIR", getCacheDir().getAbsolutePath());
        env.put("SDRSTREAM_STATE_DIR", state.getAbsolutePath());
        String password = Prefs.getUiPassword(this);
        if (password != null && !password.isEmpty()) {
            env.put("SDRSTREAM_UI_PASSWORD", password);
        }
        pb.redirectErrorStream(true);
        pb.redirectOutput(ProcessBuilder.Redirect.appendTo(new File(files, Diag.ENGINE_LOG)));
        try {
            process = pb.start();
        } catch (IOException | SecurityException e) {
            Diag.writeCrash(this, "exec engine", e);
            Diag.recordEngineError(this, "failed to start engine: " + e);
            updateNotification("failed to start engine - see diagnostics");
            return;
        } catch (Throwable t) {
            Diag.writeCrash(this, "exec engine (unexpected)", t);
            Diag.recordEngineError(this, "failed to start engine: " + t);
            updateNotification("failed to start engine - see diagnostics");
            return;
        }
        Diag.clearEngineError(this);
        try {
            PowerManager pm = (PowerManager) getSystemService(POWER_SERVICE);
            wakeLock = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "sdrstream:engine");
            wakeLock.acquire();
        } catch (Throwable t) {
            Diag.writeCrash(this, "acquire wakelock", t);
        }
        updateNotification("monitoring - UI on 127.0.0.1:" + PORT);
        final Process watched = process;
        new Thread(() -> {
            int code = -1;
            try {
                code = watched.waitFor();
            } catch (InterruptedException ignored) {
            }
            process = null;
            releaseWakeLock();
            if (stopping) return;
            String msg = "engine exited with code " + code + " - see engine.log in diagnostics";
            Diag.recordEngineError(EngineService.this, msg);
            updateNotification("engine stopped (code " + code + ") - reopen the app");
        }, "engine-watchdog").start();
    }

    private void stopEngine() {
        if (process != null) {
            process.destroy();
            process = null;
        }
        releaseWakeLock();
    }

    private void releaseWakeLock() {
        try {
            if (wakeLock != null && wakeLock.isHeld()) wakeLock.release();
        } catch (Throwable ignored) {
        }
        wakeLock = null;
    }

    private void createChannel() {
        NotificationManager nm = getSystemService(NotificationManager.class);
        nm.createNotificationChannel(new NotificationChannel(
                CHANNEL_ID, "sdrstream engine", NotificationManager.IMPORTANCE_LOW));
    }

    private Notification buildNotification(String text) {
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
        try {
            NotificationManager nm = getSystemService(NotificationManager.class);
            nm.notify(NOTIF_ID, buildNotification(text));
        } catch (Throwable t) {
            Diag.writeCrash(this, "update notification", t);
        }
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
