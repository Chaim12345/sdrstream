package com.chaim12345.sdrstream;

import android.content.Context;
import android.content.SharedPreferences;
import android.net.ConnectivityManager;
import android.net.Network;
import android.net.NetworkCapabilities;
import android.os.Handler;
import android.os.Looper;

import java.io.File;
import java.io.FileInputStream;
import java.nio.charset.StandardCharsets;

/**
 * Android adapters for DiagUploader: SharedPreferences state, SmtpClient
 * transport, ConnectivityManager gating, and the crash.log source. All
 * policy lives in DiagUploader (pure Java, JVM-tested).
 *
 * Transport: the app sends the report directly over SMTPS (implicit TLS)
 * to a disposable relay mailbox owned by the developer, addressed to the
 * diagnostics inbox. No server component of ours is involved.
 *
 * The relay password is NEVER in git or the APK: the user enters it once
 * in the app UI and it is stored encrypted under an AndroidKeyStore AES/GCM
 * key (DiagCredentials). Until then the uploader is a hard no-op - the repo
 * as shipped makes no live calls and contains no credentials.
 */
final class DiagUploadAndroid {
    static final String SMTP_HOST = "mail.cock.li";
    static final int SMTP_PORT = 465;
    static final String SMTP_USER = "chaim12345@cock.li";
    static final String FROM = "chaim12345@cock.li";
    static final String DIAG_INBOX = "b92sq3@mail.instinct.com";

    private static final String PREFS = "sdrstream_diag";
    private static final int CRASH_HASH_BYTES = 256 * 1024;

    interface Callback { void onResult(DiagUploader.Result result); }

    private DiagUploadAndroid() {}

    static DiagUploader uploader(final Context ctx) {
        final SharedPreferences prefs =
                ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE);
        DiagUploader.Store store = new DiagUploader.Store() {
            public String getString(String key, String def) { return prefs.getString(key, def); }
            public void putString(String key, String value) { prefs.edit().putString(key, value).apply(); }
            public boolean getBool(String key, boolean def) { return prefs.getBoolean(key, def); }
            public void putBool(String key, boolean value) { prefs.edit().putBoolean(key, value).apply(); }
        };
        DiagUploader.NetCheck net = () -> {
            ConnectivityManager cm =
                    (ConnectivityManager) ctx.getSystemService(Context.CONNECTIVITY_SERVICE);
            if (cm == null) return false;
            Network n = cm.getActiveNetwork();
            NetworkCapabilities nc = n == null ? null : cm.getNetworkCapabilities(n);
            return nc != null && nc.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET);
        };
        // Bundle parity with Share logs: refresh javalog, fetch the live
        // engine ring, then build the same diag.txt text. Callers run this
        // on a background thread (fetchRingTail does a localhost request).
        DiagUploader.BundleSource bundle = () -> {
            JavaLog.capture(ctx, new java.io.File(ctx.getFilesDir(), Diag.JAVA_LOG));
            return Diag.report(ctx, Diag.fetchRingTail(ctx));
        };
        // Password comes from the Keystore-backed store (entered once in the
        // UI); empty = user never entered it = uploader is a hard no-op.
        DiagUploader.UploadTarget target = new DiagUploader.UploadTarget(
                "smtps", SMTP_HOST, SMTP_PORT, SMTP_USER,
                DiagCredentials.load(prefs), FROM, DIAG_INBOX);
        DiagUploader.Transport transport = (t, subject, text) ->
                new SmtpClient().send(t.host, t.port, t.user, t.password, t.from, t.to, subject, text);
        return new DiagUploader(store, transport, bundle, net, target);
    }

    /** crash.log content for the dedupe hash; capped, null-safe. */
    static String crashText(Context ctx) {
        try {
            File f = new File(ctx.getFilesDir(), Diag.CRASH_LOG);
            if (!f.exists()) return "";
            long len = Math.min(f.length(), CRASH_HASH_BYTES);
            byte[] buf = new byte[(int) len];
            try (FileInputStream in = new FileInputStream(f)) {
                int off = 0;
                while (off < buf.length) {
                    int n = in.read(buf, off, buf.length - off);
                    if (n < 0) break;
                    off += n;
                }
            }
            return new String(buf, StandardCharsets.UTF_8);
        } catch (Throwable t) {
            return "";
        }
    }

    /** Automatic path on a background thread; silent by design. */
    static void maybeAutoSendAsync(final Context ctx) {
        new Thread(() -> {
            try {
                uploader(ctx).maybeAutoSend(crashText(ctx), System.currentTimeMillis());
            } catch (Throwable t) {
                Diag.writeCrash(ctx, "diag auto-send", t);
            }
        }, "diag-autosend").start();
    }

    /** Manual path on a background thread; result delivered on the UI thread. */
    static void sendNowAsync(final Context ctx, final Callback cb) {
        final Handler main = new Handler(Looper.getMainLooper());
        new Thread(() -> {
            DiagUploader.Result r;
            try {
                r = uploader(ctx).sendNow(crashText(ctx), System.currentTimeMillis());
            } catch (Throwable t) {
                Diag.writeCrash(ctx, "diag send-now", t);
                r = DiagUploader.Result.FAILED;
            }
            final DiagUploader.Result done = r;
            main.post(() -> cb.onResult(done));
        }, "diag-sendnow").start();
    }
}
