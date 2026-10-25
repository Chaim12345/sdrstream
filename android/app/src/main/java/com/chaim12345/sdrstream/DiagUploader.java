package com.chaim12345.sdrstream;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.Locale;
import java.util.SimpleTimeZone;

/**
 * Automatic diagnostics upload, opt-in only. Pure-Java core so the policy
 * (dedupe, daily cap, retry) runs under plain JUnit; Android adapters
 * (SharedPreferences, SmtpClient, ConnectivityManager) live in
 * DiagUploadAndroid.
 *
 * Transport: direct SMTPS (implicit TLS) to a mail relay account. There is
 * no server we control in the path - the app authenticates to the relay
 * with build-time injected credentials and sends one plain-text message
 * per report. TLS is mandatory: any target that is not "smtps" is treated
 * exactly like an unconfigured one (NO_ENDPOINT, never a send).
 *
 * Policy:
 * - Never sends unless the user opted in (or taps send-now, which is itself
 *   explicit consent for that one send).
 * - The same crash (by SHA-256 of crash.log) is sent once, ever.
 * - At most one automatic send per UTC day; a second distinct crash waits
 *   for the next day (pending).
 * - Offline/failure leaves a pending marker; the next launch retries.
 *   Some mobile carriers block outbound port 465 - that surfaces as a
 *   normal send failure and follows this same pending-retry path.
 * - The bundle arrives already redacted from Diag.report() and is sent
 *   byte-exact - the uploader never edits log content.
 */
final class DiagUploader {

    interface Store {
        String getString(String key, String def);
        void putString(String key, String value);
        boolean getBool(String key, boolean def);
        void putBool(String key, boolean value);
    }

    /** Where a report goes. All fields required; password may be empty (disabled). */
    static final class UploadTarget {
        /** Must be "smtps" (implicit TLS) or the uploader refuses to send. */
        final String scheme;
        final String host;
        final int port;
        final String user;
        final String password;
        final String from;
        final String to;

        UploadTarget(String scheme, String host, int port, String user,
                     String password, String from, String to) {
            this.scheme = scheme == null ? "" : scheme;
            this.host = host == null ? "" : host;
            this.port = port;
            this.user = user == null ? "" : user;
            this.password = password == null ? "" : password;
            this.from = from == null ? "" : from;
            this.to = to == null ? "" : to;
        }

        /** Configured = TLS-only scheme plus all addressing plus a password. */
        boolean configured() {
            return "smtps".equals(scheme) && !host.isEmpty() && port > 0
                    && !user.isEmpty() && !password.isEmpty()
                    && !from.isEmpty() && !to.isEmpty();
        }
    }

    /** Sends one message; throws on any failure (auth, carrier block, server error). */
    interface Transport { void send(UploadTarget target, String subject, String text) throws Exception; }

    /** Builds the redacted diagnostics bundle (Diag.report in the app). */
    interface BundleSource { String build(); }

    interface NetCheck { boolean online(); }

    enum Result { DISABLED, NO_ENDPOINT, OFFLINE, DUPLICATE, DAILY_CAP, NOTHING_NEW, SENT, FAILED }

    static final String KEY_OPT_IN = "upload_opt_in";
    static final String KEY_ASKED = "upload_asked";
    static final String KEY_LAST_HASH = "upload_last_hash";
    static final String KEY_LAST_DAY = "upload_last_day";
    /** Empty = no pending retry; otherwise why: "offline", "failed", "cap". */
    static final String KEY_PENDING = "upload_pending_reason";

    private final Store store;
    private final Transport transport;
    private final BundleSource bundle;
    private final NetCheck net;
    private final UploadTarget target;

    DiagUploader(Store store, Transport transport, BundleSource bundle, NetCheck net,
                 UploadTarget target) {
        this.store = store;
        this.transport = transport;
        this.bundle = bundle;
        this.net = net;
        this.target = target;
    }

    boolean optedIn() { return store.getBool(KEY_OPT_IN, false); }

    void setOptIn(boolean on) {
        store.putBool(KEY_OPT_IN, on);
        store.putBool(KEY_ASKED, true);
    }

    boolean asked() { return store.getBool(KEY_ASKED, false); }

    void markAsked() { store.putBool(KEY_ASKED, true); }

    /**
     * Automatic path: called on app launch (and after a crash is recorded).
     * Sends at most once per day, never the same crash twice.
     */
    Result maybeAutoSend(String crashText, long nowMillis) {
        if (!optedIn()) return Result.DISABLED;
        boolean hasCrash = crashText != null && !crashText.isEmpty();
        boolean pending = !store.getString(KEY_PENDING, "").isEmpty();
        if (!hasCrash && !pending) return Result.NOTHING_NEW;
        return attempt(crashText == null ? "" : crashText, nowMillis, false);
    }

    /** Manual path: the user tapped "send now" - bypasses dedupe and the cap. */
    Result sendNow(String crashText, long nowMillis) {
        return attempt(crashText == null ? "" : crashText, nowMillis, true);
    }

    private Result attempt(String crashText, long nowMillis, boolean bypassLimits) {
        // Diagnostics must never travel plaintext or to an unconfigured
        // relay: anything that is not a complete smtps target is NO_ENDPOINT.
        if (!target.configured()) return Result.NO_ENDPOINT;
        String hash = sha256Hex(crashText);
        boolean pending = !store.getString(KEY_PENDING, "").isEmpty();
        if (!bypassLimits) {
            if (!pending && hash.equals(store.getString(KEY_LAST_HASH, ""))) return Result.DUPLICATE;
            if (dayOf(nowMillis).equals(store.getString(KEY_LAST_DAY, ""))) {
                store.putString(KEY_PENDING, "cap");
                return Result.DAILY_CAP;
            }
        }
        if (!net.online()) {
            store.putString(KEY_PENDING, "offline");
            return Result.OFFLINE;
        }
        try {
            transport.send(target, subject(nowMillis, hash), bundle.build());
        } catch (Exception e) {
            store.putString(KEY_PENDING, "failed");
            return Result.FAILED;
        }
        store.putString(KEY_LAST_HASH, hash);
        store.putString(KEY_LAST_DAY, dayOf(nowMillis));
        store.putString(KEY_PENDING, "");
        return Result.SENT;
    }

    private static String subject(long nowMillis, String hash) {
        return "sdrstream diag " + dayOf(nowMillis)
                + (hash.isEmpty() ? "" : " " + hash.substring(0, Math.min(12, hash.length())));
    }

    static String dayOf(long nowMillis) {
        SimpleDateFormat f = new SimpleDateFormat("yyyy-MM-dd", Locale.US);
        f.setTimeZone(new SimpleTimeZone(0, "UTC"));
        return f.format(new Date(nowMillis));
    }

    static String sha256Hex(String s) {
        try {
            MessageDigest md = MessageDigest.getInstance("SHA-256");
            byte[] out = md.digest(s.getBytes(StandardCharsets.UTF_8));
            StringBuilder sb = new StringBuilder(out.length * 2);
            for (byte b : out) {
                sb.append(Character.forDigit((b >> 4) & 0xF, 16)).append(Character.forDigit(b & 0xF, 16));
            }
            return sb.toString();
        } catch (Exception e) {
            return "";
        }
    }
}
