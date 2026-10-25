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
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;

/**
 * Android adapters for DiagUploader: SharedPreferences state,
 * HttpURLConnection transport, ConnectivityManager gating, and the crash.log
 * source. All policy lives in DiagUploader (pure Java, JVM-tested).
 *
 * ENDPOINT and CLIENT_ID are empty in the repo on purpose: the uploader is a
 * hard no-op until the diag-relay Worker is deployed and the owner fills both
 * in a release commit. The repo as shipped makes no live calls.
 */
final class DiagUploadAndroid {
    static final String ENDPOINT = "";
    static final String CLIENT_ID = "";

    private static final String PREFS = "sdrstream_diag";
    private static final int CRASH_HASH_BYTES = 256 * 1024;
    private static final int CONNECT_TIMEOUT_MS = 15000;
    private static final int READ_TIMEOUT_MS = 15000;

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
        return new DiagUploader(store, DiagUploadAndroid::post,
                () -> Diag.report(ctx), net, ENDPOINT, CLIENT_ID);
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

    private static int post(String url, String clientId, String bodyJson) throws Exception {
        HttpURLConnection conn = (HttpURLConnection) new URL(url).openConnection();
        try {
            conn.setRequestMethod("POST");
            conn.setConnectTimeout(CONNECT_TIMEOUT_MS);
            conn.setReadTimeout(READ_TIMEOUT_MS);
            conn.setDoOutput(true);
            conn.setRequestProperty("Content-Type", "application/json");
            conn.setRequestProperty("X-Sdrstream-Client", clientId);
            byte[] body = bodyJson.getBytes(StandardCharsets.UTF_8);
            conn.setFixedLengthStreamingMode(body.length);
            try (OutputStream out = conn.getOutputStream()) {
                out.write(body);
            }
            return conn.getResponseCode();
        } finally {
            conn.disconnect();
        }
    }
}
