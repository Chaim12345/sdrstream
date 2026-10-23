package com.chaim12345.sdrstream;

import android.app.Activity;
import android.app.AlertDialog;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.content.pm.PackageInfo;
import android.content.pm.PackageInstaller;
import android.content.pm.PackageManager;
import android.content.pm.Signature;
import android.content.pm.SigningInfo;
import android.net.ConnectivityManager;
import android.net.Network;
import android.net.NetworkCapabilities;
import android.net.Uri;
import android.os.Build;
import android.os.Handler;
import android.os.Looper;
import android.provider.Settings;
import android.util.Log;
import android.widget.ProgressBar;
import android.widget.Toast;

import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.util.Locale;

/**
 * In-app self-update: check a signed-metadata feed, download the APK,
 * verify it (checksum from the feed, package name, strictly newer version,
 * and the SAME signer as the installed app), then commit a PackageInstaller
 * session. On Android 12+ the session asks for no user action; the system
 * may still show its confirm dialog (handled via STATUS_PENDING_USER_ACTION).
 *
 * Guarantees: never a downgrade, never a different signer, never a wipe
 * (PackageInstaller updates keep app data), and any failure or cancel
 * leaves the installed app untouched and deletes partial downloads.
 */
final class Updater {
    static final String ACTION_INSTALL_RESULT =
            "com.chaim12345.sdrstream.action.UPDATE_INSTALL_RESULT";

    private static final String TAG = "sdrstream-update";
    private static final long AUTOCHECK_INTERVAL_MS = 6L * 3600_000L;
    private static final int MAX_MANIFEST_BYTES = 64 * 1024;
    private static final int CONNECT_TIMEOUT_MS = 10_000;
    private static final int READ_TIMEOUT_MS = 15_000;
    private static final long MAX_APK_BYTES = 200L * 1024 * 1024;
    private static final int MAX_REDIRECTS = 2;

    static final int CHECK_UP_TO_DATE = 0;
    static final int CHECK_AVAILABLE = 1;
    static final int CHECK_ERROR = 2;
    static final int CHECK_NOT_CONFIGURED = 3;

    interface CheckListener {
        void onCheck(int status, UpdateManifest manifest, String message);
    }

    interface DownloadListener {
        void onProgress(int percent);
        void onDone(File apk);
        void onError(String message);
    }

    private static final Handler MAIN = new Handler(Looper.getMainLooper());

    private Updater() {}

    /** Manual check from the menu: every outcome is shown to the user. */
    static void manualCheck(final Activity activity) {
        final AlertDialog progress = new AlertDialog.Builder(activity)
                .setTitle("Updates")
                .setMessage("Checking for updates...")
                .setCancelable(false)
                .show();
        check(activity, (status, manifest, message) -> {
            progress.dismiss();
            switch (status) {
                case CHECK_NOT_CONFIGURED:
                    infoDialog(activity, "Updates",
                            "This build has no update source configured yet, so it cannot "
                                    + "check for updates automatically.");
                    break;
                case CHECK_UP_TO_DATE:
                    infoDialog(activity, "Updates",
                            "sdrstream is up to date (build " + installedVersionCode(activity) + ").");
                    break;
                case CHECK_AVAILABLE:
                    offerUpdate(activity, manifest);
                    break;
                default:
                    infoDialog(activity, "Update check failed", message);
            }
        });
    }

    /**
     * Silent background check at most once per 6h, only when a feed is
     * configured. Nothing is shown unless an update is actually available.
     */
    static void autoCheckIfDue(final Activity activity) {
        String feed = Prefs.getUpdateFeedUrl(activity);
        if (feed.isEmpty()) return;
        long now = System.currentTimeMillis();
        if (now - Prefs.getUpdateLastCheck(activity) < AUTOCHECK_INTERVAL_MS) return;
        Prefs.setUpdateLastCheck(activity, now);
        check(activity, (status, manifest, message) -> {
            if (status == CHECK_AVAILABLE) offerUpdate(activity, manifest);
        });
    }

    static void check(final Context ctx, final CheckListener listener) {
        final String feed = Prefs.getUpdateFeedUrl(ctx);
        if (feed.isEmpty()) {
            MAIN.post(() -> listener.onCheck(CHECK_NOT_CONFIGURED, null, null));
            return;
        }
        // Plain HTTP is accepted only for the exact host the user typed as
        // the update source, and only when that host is loopback or a CGNAT
        // literal with an active VPN transport right now. A CGNAT address is
        // not assumed to be Tailscale by range alone; the VPN check is what
        // makes it a known tailnet test host. HTTPS feeds need no such check.
        final String feedHost = UpdateManifest.hostOf(feed.toLowerCase(Locale.US));
        final boolean feedIsPlainHttp = feed.toLowerCase(Locale.US).startsWith("http://");
        final String plainHttpHost;
        if (feedIsPlainHttp && feedHost != null && !feedHost.startsWith("127.")
                && !feedHost.equals("localhost")) {
            if (!UpdateManifest.isCgnatLiteral(feedHost) || !isOnVpn(ctx)) {
                MAIN.post(() -> listener.onCheck(CHECK_ERROR, null,
                        "Plain-HTTP update sources are only for this device's own tailnet "
                                + "while the VPN is connected. Use HTTPS, or connect the VPN."));
                return;
            }
            plainHttpHost = feedHost;
        } else {
            plainHttpHost = feedIsPlainHttp ? feedHost : null; // loopback
        }
        new Thread(() -> {
            try {
                String body = httpGet(feed, MAX_MANIFEST_BYTES);
                final UpdateManifest m = UpdateManifest.parse(body, ctx.getPackageName(),
                        plainHttpHost);
                if (m.minSdk > Build.VERSION.SDK_INT) {
                    postCheck(listener, CHECK_ERROR, null,
                            "The available update needs a newer Android version.");
                    return;
                }
                long installed = installedVersionCode(ctx);
                postCheck(listener, m.versionCode > installed ? CHECK_AVAILABLE : CHECK_UP_TO_DATE,
                        m, null);
            } catch (Exception e) {
                Log.w(TAG, "update check failed", e);
                String msg = e instanceof UpdateException
                        ? e.getMessage()
                        : "Could not reach the update source.";
                postCheck(listener, CHECK_ERROR, null, msg);
            }
        }, "update-check").start();
    }

    private static void postCheck(final CheckListener l, final int status,
                                  final UpdateManifest m, final String message) {
        MAIN.post(() -> l.onCheck(status, m, message));
    }

    private static void offerUpdate(final Activity activity, final UpdateManifest m) {
        String versionLine = m.versionName.isEmpty()
                ? "build " + m.versionCode
                : m.versionName + " (build " + m.versionCode + ")";
        String body = "New version: " + versionLine + "\nInstalled: build "
                + installedVersionCode(activity)
                + (m.notes.isEmpty() ? "" : "\n\n" + m.notes)
                + "\n\nThe download is verified against this app's signing key "
                + "before installing. Your settings and data are kept.";
        new AlertDialog.Builder(activity)
                .setTitle("Update available")
                .setMessage(body)
                .setPositiveButton("Update", (d, w) -> startDownloadWithPermission(activity, m))
                .setNegativeButton("Later", null)
                .show();
    }

    private static void startDownloadWithPermission(final Activity activity, final UpdateManifest m) {
        if (!activity.getPackageManager().canRequestPackageInstalls()) {
            new AlertDialog.Builder(activity)
                    .setTitle("Allow app updates")
                    .setMessage("Android asks for one-time permission before sdrstream can "
                            + "install its own updates. On the next screen, allow installs "
                            + "from sdrstream, then run the update again.")
                    .setPositiveButton("Open settings", (d, w) -> activity.startActivity(
                            new Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,
                                    Uri.parse("package:" + activity.getPackageName()))))
                    .setNegativeButton("Cancel", null)
                    .show();
            return;
        }
        final ProgressBar bar = new ProgressBar(activity, null,
                android.R.attr.progressBarStyleHorizontal);
        bar.setMax(100);
        bar.setIndeterminate(true);
        int pad = (int) (20 * activity.getResources().getDisplayMetrics().density);
        bar.setPadding(pad, pad, pad, 0);
        final AlertDialog dialog = new AlertDialog.Builder(activity)
                .setTitle("Downloading update")
                .setView(bar)
                .setNegativeButton("Cancel", null)
                .setCancelable(false)
                .show();
        final boolean[] cancelled = {false};
        dialog.getButton(AlertDialog.BUTTON_NEGATIVE).setOnClickListener(v -> {
            cancelled[0] = true;
            dialog.dismiss();
        });
        download(activity, m, cancelled, new DownloadListener() {
            @Override public void onProgress(final int percent) {
                MAIN.post(() -> {
                    bar.setIndeterminate(percent < 0);
                    if (percent >= 0) bar.setProgress(percent);
                });
            }

            @Override public void onDone(final File apk) {
                MAIN.post(() -> {
                    dialog.dismiss();
                    if (cancelled[0]) { apk.delete(); return; }
                    verifyAndInstall(activity, m, apk);
                });
            }

            @Override public void onError(final String message) {
                MAIN.post(() -> {
                    dialog.dismiss();
                    if (!cancelled[0]) infoDialog(activity, "Update download failed", message);
                });
            }
        });
    }

    static void download(final Context ctx, final UpdateManifest m, final boolean[] cancelled,
                         final DownloadListener listener) {
        new Thread(() -> {
            File dir = new File(ctx.getFilesDir(), "updates");
            // Keep at most the file we are about to write: stale partials and
            // older downloaded APKs are deleted, never installed.
            cleanupDownloads(ctx);
            if (!dir.exists() && !dir.mkdirs()) {
                listener.onError("Could not prepare the download folder.");
                return;
            }
            File partial = new File(dir, "update-" + m.versionCode + ".apk.partial");
            File target = new File(dir, "update-" + m.versionCode + ".apk");
            HttpURLConnection conn = null;
            try {
                conn = openFollowingRedirects(m.url);
                long total = conn.getContentLengthLong();
                if (total > MAX_APK_BYTES) {
                    throw new UpdateException("The update is larger than the allowed maximum.");
                }
                try (InputStream in = conn.getInputStream();
                     OutputStream out = new FileOutputStream(partial)) {
                    byte[] buf = new byte[65536];
                    long read = 0;
                    int n;
                    int lastPercent = -1;
                    while ((n = in.read(buf)) >= 0) {
                        if (cancelled[0]) {
                            listener.onError("cancelled");
                            return;
                        }
                        read += n;
                        if (read > MAX_APK_BYTES) {
                            throw new UpdateException("The update is larger than the allowed maximum.");
                        }
                        out.write(buf, 0, n);
                        int percent = total > 0 ? (int) (read * 100 / total) : -1;
                        if (percent != lastPercent) {
                            lastPercent = percent;
                            listener.onProgress(percent);
                        }
                    }
                }
                String actual = UpdateVerifier.sha256Hex(partial);
                if (!m.sha256.equalsIgnoreCase(actual)) {
                    throw new UpdateException("The download did not match the expected checksum.");
                }
                if (!partial.renameTo(target)) {
                    throw new IOException("Could not finalize the download.");
                }
                listener.onDone(target);
            } catch (Exception e) {
                Log.w(TAG, "download failed", e);
                partial.delete();
                listener.onError(e instanceof UpdateException
                        ? e.getMessage()
                        : "The download could not be completed.");
            } finally {
                if (conn != null) conn.disconnect();
            }
        }, "update-download").start();
    }

    /** Last gate before the system installer: identity, freshness, signer. */
    private static void verifyAndInstall(final Activity activity, final UpdateManifest m,
                                         final File apk) {
        try {
            verifyArchiveMatchesSelf(activity, apk, m.versionCode);
        } catch (UpdateException e) {
            apk.delete();
            infoDialog(activity, "Update refused", e.getMessage());
            return;
        }
        Toast.makeText(activity, "Installing update...", Toast.LENGTH_SHORT).show();
        commitSession(activity, apk);
    }

    static void verifyArchiveMatchesSelf(Context ctx, File apk, long expectVersionCode)
            throws UpdateException {
        PackageManager pm = ctx.getPackageManager();
        int flags = Build.VERSION.SDK_INT >= 28
                ? PackageManager.GET_SIGNING_CERTIFICATES
                : PackageManager.GET_SIGNATURES;
        PackageInfo archive = pm.getPackageArchiveInfo(apk.getAbsolutePath(), flags);
        if (archive == null) throw new UpdateException("The download is not a valid app package.");
        if (!ctx.getPackageName().equals(archive.packageName)) {
            throw new UpdateException("The downloaded update is for a different app.");
        }
        long archiveVersion = versionCodeOf(archive);
        if (archiveVersion <= installedVersionCode(ctx)) {
            // Never a downgrade or same-version replace, even with our key.
            throw new UpdateException("The update is not newer than the installed version.");
        }
        if (expectVersionCode >= 0 && archiveVersion != expectVersionCode) {
            throw new UpdateException("The download does not match the version the feed offered.");
        }
        try {
            byte[][] mine = signerBytes(pm.getPackageInfo(ctx.getPackageName(), flags));
            byte[][] theirs = signerBytes(archive);
            if (!UpdateVerifier.sameSigners(mine, theirs)) {
                throw new UpdateException("The update is signed by a different key.");
            }
        } catch (PackageManager.NameNotFoundException e) {
            throw new UpdateException("Could not read the installed app's signature.", e);
        }
    }

    @SuppressWarnings("deprecation")
    private static byte[][] signerBytes(PackageInfo pi) {
        Signature[] sigs;
        if (Build.VERSION.SDK_INT >= 28) {
            SigningInfo si = pi.signingInfo;
            if (si == null) return new byte[0][];
            sigs = si.hasMultipleSigners()
                    ? si.getApkContentsSigners()
                    : si.getSigningCertificateHistory();
        } else {
            sigs = pi.signatures;
        }
        if (sigs == null) return new byte[0][];
        byte[][] out = new byte[sigs.length][];
        for (int i = 0; i < sigs.length; i++) out[i] = sigs[i].toByteArray();
        return out;
    }

    private static void commitSession(final Activity activity, final File apk) {
        PackageInstaller installer = activity.getPackageManager().getPackageInstaller();
        PackageInstaller.SessionParams params = new PackageInstaller.SessionParams(
                PackageInstaller.SessionParams.MODE_FULL_INSTALL);
        params.setSize(apk.length());
        if (Build.VERSION.SDK_INT >= 31) {
            // Self-update: docs allow no user action when the incoming app
            // meets the target-API floor; the system can still demand the
            // confirm UI, which UpdateInstallReceiver then launches.
            params.setRequireUserAction(PackageInstaller.SessionParams.USER_ACTION_NOT_REQUIRED);
        }
        PackageInstaller.Session session = null;
        try {
            int sessionId = installer.createSession(params);
            session = installer.openSession(sessionId);
            try (OutputStream out = session.openWrite("base.apk", 0, apk.length());
                 InputStream in = new FileInputStream(apk)) {
                byte[] buf = new byte[65536];
                int n;
                while ((n = in.read(buf)) >= 0) out.write(buf, 0, n);
                session.fsync(out);
            }
            Intent result = new Intent(activity, UpdateInstallReceiver.class)
                    .setAction(ACTION_INSTALL_RESULT);
            int flags = PendingIntent.FLAG_UPDATE_CURRENT;
            if (Build.VERSION.SDK_INT >= 31) flags |= PendingIntent.FLAG_MUTABLE;
            session.commit(PendingIntent.getBroadcast(activity, 0, result, flags)
                    .getIntentSender());
            session.close();
            session = null;
        } catch (Exception e) {
            Log.w(TAG, "install session failed", e);
            apk.delete();
            infoDialog(activity, "Update failed",
                    "The system could not start the install. The current version is unchanged.");
        } finally {
            if (session != null) {
                try { session.close(); } catch (Exception ignored) {}
            }
        }
    }

    static void cleanupDownloads(Context ctx) {
        File dir = new File(ctx.getFilesDir(), "updates");
        File[] files = dir.listFiles();
        if (files == null) return;
        for (File f : files) {
            if (f.getName().startsWith("update-")) f.delete();
        }
    }

    static long installedVersionCode(Context ctx) {
        try {
            PackageInfo pi = ctx.getPackageManager().getPackageInfo(ctx.getPackageName(), 0);
            return versionCodeOf(pi);
        } catch (PackageManager.NameNotFoundException e) {
            return -1;
        }
    }

    @SuppressWarnings("deprecation")
    private static long versionCodeOf(PackageInfo pi) {
        return Build.VERSION.SDK_INT >= 28 ? pi.getLongVersionCode() : pi.versionCode;
    }

    private static String httpGet(String url, int maxBytes) throws IOException {
        HttpURLConnection conn = openFollowingRedirects(url);
        try (InputStream in = conn.getInputStream()) {
            StringBuilder sb = new StringBuilder();
            byte[] buf = new byte[8192];
            int total = 0;
            int n;
            while ((n = in.read(buf)) >= 0) {
                total += n;
                if (total > maxBytes) throw new IOException("response too large");
                sb.append(new String(buf, 0, n, "UTF-8"));
            }
            return sb.toString();
        } finally {
            conn.disconnect();
        }
    }

    /**
     * Opens a connection, following at most MAX_REDIRECTS redirects by hand.
     * Automatic redirect following is off: every hop must be an absolute
     * HTTPS URL, so a feed or CDN can never bounce the updater to plain HTTP
     * or to an internal address (SSRF guard).
     */
    private static HttpURLConnection openFollowingRedirects(String url) throws IOException {
        String current = url;
        for (int hop = 0; ; hop++) {
            HttpURLConnection conn = (HttpURLConnection) new URL(current).openConnection();
            conn.setConnectTimeout(CONNECT_TIMEOUT_MS);
            conn.setReadTimeout(READ_TIMEOUT_MS);
            conn.setInstanceFollowRedirects(false);
            conn.setRequestProperty("User-Agent", "sdrstream-updater");
            int code = conn.getResponseCode();
            if (code < 300 || code >= 400) return conn;
            String location = conn.getHeaderField("Location");
            conn.disconnect();
            if (location == null || hop >= MAX_REDIRECTS) {
                throw new IOException("too many redirects");
            }
            java.net.URI resolved;
            try {
                resolved = new java.net.URI(current).resolve(location);
            } catch (java.net.URISyntaxException e) {
                throw new IOException("bad redirect target", e);
            }
            if (!"https".equalsIgnoreCase(resolved.getScheme())) {
                throw new IOException("redirect to a non-HTTPS address refused");
            }
            current = resolved.toString();
        }
    }

    /** True when the active network is a VPN transport (Tailscale counts). */
    private static boolean isOnVpn(Context ctx) {
        ConnectivityManager cm = (ConnectivityManager) ctx.getSystemService(Context.CONNECTIVITY_SERVICE);
        if (cm == null) return false;
        for (Network network : cm.getAllNetworks()) {
            NetworkCapabilities caps = cm.getNetworkCapabilities(network);
            if (caps != null && caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN)) {
                return true;
            }
        }
        return false;
    }

    private static void infoDialog(Activity activity, String title, String message) {
        if (activity.isFinishing()) return;
        new AlertDialog.Builder(activity)
                .setTitle(title)
                .setMessage(message)
                .setPositiveButton("OK", null)
                .show();
    }

    /** Human-readable current version for dialogs. */
    static String installedVersionLabel(Context ctx) {
        try {
            PackageInfo pi = ctx.getPackageManager().getPackageInfo(ctx.getPackageName(), 0);
            return String.format(Locale.US, "%s (build %d)", pi.versionName, versionCodeOf(pi));
        } catch (PackageManager.NameNotFoundException e) {
            return "unknown";
        }
    }
}
