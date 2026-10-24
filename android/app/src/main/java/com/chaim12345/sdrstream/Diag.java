package com.chaim12345.sdrstream;

import android.app.ActivityManager;
import android.app.ApplicationExitInfo;
import android.content.Context;
import android.content.SharedPreferences;
import android.content.pm.PackageInfo;
import android.os.Build;

import java.io.BufferedReader;
import java.io.File;
import java.io.FileWriter;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.io.RandomAccessFile;
import java.net.HttpURLConnection;
import java.net.URL;
import java.net.URLEncoder;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.List;
import java.util.Locale;
import java.util.TimeZone;

/**
 * Crash capture and diagnostics. Everything lands in plain text files in the
 * app-private files dir so the user can share them from the menu.
 */
final class Diag {
    static final String CRASH_LOG = "crash.log";
    static final String ENGINE_LOG = "engine.log";
    static final String ENGINE_LOG_OLD = "engine.log.1";
    static final String JAVA_LOG = "javalog.txt";
    static final String DIAG_TXT = "diag.txt";
    private static final String PREFS = "sdrstream_diag";
    private static final String KEY_LAST_ERROR = "last_engine_error";
    private static final int ENGINE_TAIL_BYTES = 64 * 1024;
    private static final int JAVA_TAIL_BYTES = 64 * 1024;
    private static final int GENERIC_TAIL_BYTES = 24 * 1024;
    private static final int EXIT_TRACE_BYTES = 4 * 1024;

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

    /** Reads the last GENERIC_TAIL_BYTES of a log file, or null when absent. */
    static String tail(File f) {
        return tail(f, GENERIC_TAIL_BYTES);
    }

    /** Reads the last maxBytes of a log file, or null when absent. */
    static String tail(File f, int maxBytes) {
        if (f == null || !f.exists()) return null;
        try (RandomAccessFile raf = new RandomAccessFile(f, "r")) {
            long len = raf.length();
            long start = Math.max(0, len - maxBytes);
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
        return report(ctx, null);
    }

    /**
     * Full diagnostics snapshot. ringTail is the pre-fetched engine log
     * ring (fetched off the main thread via fetchRingTail); null means the
     * engine was unreachable.
     */
    static String report(Context ctx, String ringTail) {
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

        sb.append("--- process exit history ---\n");
        sb.append(exitHistory(ctx));

        String engineLog = tail(new File(files, ENGINE_LOG), ENGINE_TAIL_BYTES);
        sb.append("--- engine.log (tail) ---\n");
        sb.append(engineLog == null ? "(no engine.log yet)\n" : engineLog + "\n");
        String engineLogOld = tail(new File(files, ENGINE_LOG_OLD), GENERIC_TAIL_BYTES);
        if (engineLogOld != null) {
            sb.append("--- engine.log.1 (previous rotation, tail) ---\n");
            sb.append(engineLogOld).append("\n");
        }
        String javaLog = tail(new File(files, JAVA_LOG), JAVA_TAIL_BYTES);
        sb.append("--- javalog.txt (tail) ---\n");
        sb.append(javaLog == null ? "(no javalog.txt yet)\n" : javaLog + "\n");
        sb.append("--- engine ring (/api/logs/tail, redacted) ---\n");
        sb.append(ringTail == null ? "(engine not reachable - ring unavailable)\n" : ringTail + "\n");
        String crashLog = tail(new File(files, CRASH_LOG));
        sb.append("--- crash.log ---\n");
        sb.append(crashLog == null ? "(no crashes recorded)\n" : crashLog + "\n");
        return sb.toString();
    }

    /**
     * Recent process exit reasons (Android 11+): catches silent kills, OOM
     * (REASON_SIGNALED/SIGKILL) and native crashes the Java crash handler
     * cannot see - high value for the Go engine. No permission required.
     */
    private static String exitHistory(Context ctx) {
        if (Build.VERSION.SDK_INT < 30) {
            return "(needs Android 11+)\n";
        }
        try {
            ActivityManager am = (ActivityManager) ctx.getSystemService(Context.ACTIVITY_SERVICE);
            List<ApplicationExitInfo> exits =
                    am.getHistoricalProcessExitReasons(ctx.getPackageName(), 0, 5);
            if (exits == null || exits.isEmpty()) {
                return "(no recorded exits)\n";
            }
            StringBuilder sb = new StringBuilder();
            for (ApplicationExitInfo info : exits) {
                sb.append(isoAt(info.getTimestamp())).append(' ')
                        .append(info.getProcessName()).append(": ")
                        .append(exitReasonName(info.getReason()))
                        .append(" status=").append(info.getStatus());
                if (info.getDescription() != null) {
                    sb.append(" (").append(info.getDescription()).append(')');
                }
                sb.append('\n');
                if (info.getReason() == ApplicationExitInfo.REASON_CRASH_NATIVE) {
                    String trace = readExitTrace(info);
                    if (trace != null) {
                        sb.append("  native trace head:\n").append(trace).append('\n');
                    }
                }
            }
            return sb.toString();
        } catch (Throwable t) {
            return "(unavailable: " + t + ")\n";
        }
    }

    /** First EXIT_TRACE_BYTES of a native-crash tombstone, redacted. */
    private static String readExitTrace(ApplicationExitInfo info) {
        try (BufferedReader r = new BufferedReader(
                new InputStreamReader(info.getTraceInputStream()))) {
            StringBuilder sb = new StringBuilder();
            String line;
            while (sb.length() < EXIT_TRACE_BYTES && (line = r.readLine()) != null) {
                sb.append("    ").append(DiagRedact.redact(line)).append('\n');
            }
            return sb.length() == 0 ? null : sb.toString();
        } catch (Throwable t) {
            return null;
        }
    }

    static String exitReasonName(int reason) {
        switch (reason) {
            case 1: return "EXIT_SELF";
            case 2: return "SIGNALED (kill/OOM)";
            case 3: return "LOW_MEMORY";
            case 4: return "CRASH (java)";
            case 5: return "CRASH_NATIVE";
            case 6: return "ANR";
            case 7: return "INITIALIZATION_FAILURE";
            case 8: return "PERMISSION_CHANGE";
            case 9: return "EXCESSIVE_RESOURCE_USAGE";
            case 10: return "USER_REQUESTED";
            case 11: return "USER_STOPPED";
            case 12: return "DEPENDENCY_DIED";
            case 13: return "OTHER";
            case 14: return "FREEZER";
            case 15: return "PACKAGE_STATE_CHANGE";
            case 16: return "PACKAGE_UPDATED";
            default: return "reason " + reason;
        }
    }

    private static String isoAt(long epochMs) {
        SimpleDateFormat fmt = new SimpleDateFormat("yyyy-MM-dd HH:mm:ss'Z'", Locale.US);
        fmt.setTimeZone(TimeZone.getTimeZone("UTC"));
        return fmt.format(new Date(epochMs));
    }

    /**
     * Fetches the engine's redacted log ring over the loopback UI: logs in
     * through /login with the on-device UI password, then reads
     * /api/logs/tail. Returns null when the engine is unreachable. Runs a
     * subprocess-free HTTP exchange - caller must be off the main thread.
     */
    static String fetchRingTail(Context ctx) {
        HttpURLConnection conn = null;
        try {
            String base = "http://127.0.0.1:" + EngineService.PORT;
            HttpURLConnection login = (HttpURLConnection) new URL(base + "/login").openConnection();
            login.setRequestMethod("POST");
            login.setDoOutput(true);
            login.setConnectTimeout(800);
            login.setReadTimeout(800);
            login.setInstanceFollowRedirects(false);
            String body = "password=" + URLEncoder.encode(Prefs.getUiPassword(ctx), "UTF-8");
            OutputStream os = login.getOutputStream();
            os.write(body.getBytes("UTF-8"));
            os.close();
            login.getResponseCode();
            String cookie = null;
            List<String> cookies = login.getHeaderFields().get("Set-Cookie");
            if (cookies != null) {
                for (String c : cookies) {
                    if (c.startsWith("sdrstream_session=")) {
                        cookie = c.split(";", 2)[0];
                        break;
                    }
                }
            }
            login.disconnect();
            if (cookie == null) return null;

            conn = (HttpURLConnection) new URL(base + "/api/logs/tail?lines=500").openConnection();
            conn.setConnectTimeout(800);
            conn.setReadTimeout(800);
            conn.setRequestProperty("Cookie", cookie);
            if (conn.getResponseCode() != 200) return null;
            BufferedReader r = new BufferedReader(new InputStreamReader(conn.getInputStream()));
            StringBuilder raw = new StringBuilder();
            String line;
            while ((line = r.readLine()) != null) raw.append(line);
            org.json.JSONArray lines = new org.json.JSONObject(raw.toString()).getJSONArray("lines");
            StringBuilder sb = new StringBuilder();
            for (int i = 0; i < lines.length(); i++) {
                sb.append(lines.getString(i)).append('\n');
            }
            return sb.toString();
        } catch (Throwable t) {
            return null;
        } finally {
            if (conn != null) conn.disconnect();
        }
    }

    /** Writes the diagnostics snapshot to diag.txt for sharing. */
    static File writeReportFile(Context ctx) {
        return writeReportFile(ctx, null);
    }

    /** Writes the diagnostics snapshot (with engine ring) to diag.txt. */
    static File writeReportFile(Context ctx, String ringTail) {
        File out = new File(ctx.getFilesDir(), DIAG_TXT);
        try {
            FileWriter w = new FileWriter(out, false);
            w.write(report(ctx, ringTail));
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
