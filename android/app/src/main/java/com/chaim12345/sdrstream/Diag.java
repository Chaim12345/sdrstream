package com.chaim12345.sdrstream;

import android.content.Context;
import android.content.SharedPreferences;
import android.content.pm.PackageInfo;
import android.os.Build;

import java.io.File;
import java.io.FileWriter;
import java.io.RandomAccessFile;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.Locale;
import java.util.TimeZone;

/**
 * Crash capture and diagnostics. Everything lands in plain text files in the
 * app-private files dir so the user can share them from the menu.
 */
final class Diag {
    static final String CRASH_LOG = "crash.log";
    static final String ENGINE_LOG = "engine.log";
    static final String DIAG_TXT = "diag.txt";
    private static final String PREFS = "sdrstream_diag";
    private static final String KEY_LAST_ERROR = "last_engine_error";
    private static final int TAIL_BYTES = 24 * 1024;

    private Diag() {}

    private static SharedPreferences prefs(Context ctx) {
        return ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE);
    }

    /** Routes every uncaught exception (any thread) into crash.log. */
    static void installCrashHandler(final Context ctx) {
        final Thread.UncaughtExceptionHandler previous =
                Thread.getDefaultUncaughtExceptionHandler();
        Thread.setDefaultUncaughtExceptionHandler((thread, error) -> {
            try {
                writeCrash(ctx, "uncaught exception on thread " + thread.getName(), error);
            } catch (Throwable ignored) {
            }
            if (previous != null) previous.uncaughtException(thread, error);
        });
    }

    static synchronized void writeCrash(Context ctx, String where, Throwable error) {
        try {
            FileWriter out = new FileWriter(new File(ctx.getFilesDir(), CRASH_LOG), true);
            out.write("=== " + isoNow() + " - " + where + " ===\n");
            out.write(deviceInfo(ctx) + "\n");
            out.write(android.util.Log.getStackTraceString(error));
            out.write("\n");
            out.close();
        } catch (Throwable ignored) {
        }
    }

    /** One-line status the UI can surface; overwritten on each new error. */
    static void recordEngineError(Context ctx, String message) {
        prefs(ctx).edit().putString(KEY_LAST_ERROR, isoNow() + "  " + message).apply();
    }

    static String lastEngineError(Context ctx) {
        return prefs(ctx).getString(KEY_LAST_ERROR, null);
    }

    static void clearEngineError(Context ctx) {
        prefs(ctx).edit().remove(KEY_LAST_ERROR).apply();
    }

    static boolean hasCrashLog(Context ctx) {
        return new File(ctx.getFilesDir(), CRASH_LOG).exists();
    }

    static String deviceInfo(Context ctx) {
        String version = "?";
        try {
            PackageInfo pi = ctx.getPackageManager().getPackageInfo(ctx.getPackageName(), 0);
            version = pi.versionName + " (" + pi.getLongVersionCode() + ")";
        } catch (Exception ignored) {
        }
        return "app " + version
                + ", " + Build.MANUFACTURER + " " + Build.MODEL
                + ", Android " + Build.VERSION.RELEASE + " (SDK " + Build.VERSION.SDK_INT + ")"
                + ", abis " + String.join(",", Build.SUPPORTED_ABIS);
    }

    /** Reads the last TAIL_BYTES of a log file, or null when absent. */
    static String tail(File f) {
        if (f == null || !f.exists()) return null;
        try (RandomAccessFile raf = new RandomAccessFile(f, "r")) {
            long len = raf.length();
            long start = Math.max(0, len - TAIL_BYTES);
            raf.seek(start);
            byte[] buf = new byte[(int) (len - start)];
            raf.readFully(buf);
            String text = new String(buf, java.nio.charset.StandardCharsets.UTF_8);
            return (start > 0 ? "[...truncated...]\n" : "") + text;
        } catch (Throwable t) {
            return "[unreadable: " + t + "]";
        }
    }

    /** Full diagnostics snapshot: device, engine binary state, logs. */
    static String report(Context ctx) {
        StringBuilder sb = new StringBuilder();
        sb.append("sdrstream diagnostics - ").append(isoNow()).append('\n');
        sb.append(deviceInfo(ctx)).append('\n');
        String err = lastEngineError(ctx);
        if (err != null) sb.append("last engine error: ").append(err).append('\n');
        sb.append('\n');

        File engine = new File(ctx.getApplicationInfo().nativeLibraryDir, "libsdrstreamd.so");
        sb.append("engine binary: ").append(engine.getAbsolutePath()).append('\n');
        sb.append("  exists=").append(engine.exists())
                .append(" canRead=").append(engine.canRead())
                .append(" canExecute=").append(engine.canExecute())
                .append(" size=").append(engine.length()).append('\n');
        File libDir = new File(ctx.getApplicationInfo().nativeLibraryDir);
        String[] libs = libDir.list();
        if (libs != null) {
            for (String lib : libs) sb.append("  lib: ").append(lib).append('\n');
        }
        File files = ctx.getFilesDir();
        String[] names = files.list();
        if (names != null) {
            for (String name : names) {
                File f = new File(files, name);
                sb.append("  file: ").append(name)
                        .append(" (").append(f.isDirectory() ? "dir" : f.length() + " bytes")
                        .append(")\n");
            }
        }
        sb.append('\n');

        String engineLog = tail(new File(files, ENGINE_LOG));
        sb.append("--- engine.log (tail) ---\n");
        sb.append(engineLog == null ? "(no engine.log yet)\n" : engineLog + "\n");
        String crashLog = tail(new File(files, CRASH_LOG));
        sb.append("--- crash.log ---\n");
        sb.append(crashLog == null ? "(no crashes recorded)\n" : crashLog + "\n");
        return sb.toString();
    }

    /** Writes the diagnostics snapshot to diag.txt for sharing. */
    static File writeReportFile(Context ctx) {
        File out = new File(ctx.getFilesDir(), DIAG_TXT);
        try {
            FileWriter w = new FileWriter(out, false);
            w.write(report(ctx));
            w.close();
        } catch (Throwable ignored) {
        }
        return out;
    }

    static String isoNow() {
        SimpleDateFormat fmt = new SimpleDateFormat("yyyy-MM-dd HH:mm:ss.SSS'Z'", Locale.US);
        fmt.setTimeZone(TimeZone.getTimeZone("UTC"));
        return fmt.format(new Date());
    }
}
