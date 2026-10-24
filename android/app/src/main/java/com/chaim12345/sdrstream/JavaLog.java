package com.chaim12345.sdrstream;

import android.content.Context;
import android.os.Process;

import java.io.BufferedReader;
import java.io.File;
import java.io.FileWriter;
import java.io.IOException;
import java.io.InputStreamReader;
import java.util.ArrayList;
import java.util.List;

/**
 * Captures this app's own logcat lines into javalog.txt so a diagnostics
 * share carries the Java side (warnings, crashes logged by Android) without
 * ADB. Captured at app start and refreshed on every Share. Every line is
 * redacted through DiagRedact first and the file is capped to the newest
 * MAX_BYTES, so it can neither leak secrets nor grow without bound.
 */
final class JavaLog {
    static final int MAX_BYTES = 512 * 1024;

    private JavaLog() {}

    /** Refreshes javalog.txt on a background thread. */
    static void captureAsync(final Context ctx) {
        final File out = new File(ctx.getFilesDir(), Diag.JAVA_LOG);
        new Thread(() -> capture(ctx, out), "javalog-capture").start();
    }

    /** Synchronous capture; call from a worker thread only (execs logcat). */
    static void capture(Context ctx, File out) {
        List<String> cmd = new ArrayList<>();
        cmd.add("logcat");
        cmd.add("-d");
        cmd.add("-v");
        cmd.add("threadtime");
        cmd.add("--pid=" + Process.myPid());
        java.lang.Process proc = null;
        try {
            proc = new ProcessBuilder(cmd).redirectErrorStream(true).start();
            BufferedReader r = new BufferedReader(new InputStreamReader(proc.getInputStream()));
            BoundedTail tail = new BoundedTail(MAX_BYTES);
            String line;
            while ((line = r.readLine()) != null) {
                tail.add(DiagRedact.redact(line));
            }
            File tmp = new File(out.getPath() + ".tmp");
            FileWriter w = new FileWriter(tmp, false);
            tail.writeTo(w);
            w.close();
            if (!tmp.renameTo(out)) {
                // Files-dir same-volume rename should not fail; fall back so
                // a stale javalog.txt is never mistaken for a fresh one.
                tmp.delete();
            }
        } catch (Throwable t) {
            Diag.writeCrash(ctx, "javalog capture", t);
        } finally {
            if (proc != null) proc.destroy();
        }
    }
}
