package com.chaim12345.sdrstream;

import android.content.Context;
import android.content.SharedPreferences;

import java.security.SecureRandom;

/** App settings: the web UI password, generated once and kept on-device. */
final class Prefs {
    private static final String NAME = "sdrstream";
    private static final String KEY_PASSWORD = "ui_password";
    private static final String KEY_TAILNET_ADDR = "tailnet_address";

    private Prefs() {}

    static SharedPreferences prefs(Context ctx) {
        return ctx.getSharedPreferences(NAME, Context.MODE_PRIVATE);
    }

    /** Returns the UI password, generating and storing one on first run. */
    static synchronized String getUiPassword(Context ctx) {
        SharedPreferences p = prefs(ctx);
        String pw = p.getString(KEY_PASSWORD, null);
        if (pw == null || pw.isEmpty()) {
            pw = generate();
            p.edit().putString(KEY_PASSWORD, pw).apply();
        }
        return pw;
    }

    static void setUiPassword(Context ctx, String pw) {
        prefs(ctx).edit().putString(KEY_PASSWORD, pw).apply();
    }

    /**
     * The user's own Tailscale IPv4 for remote UI access, typed by the user
     * (or accepted from a detected suggestion) in the app menu. Empty means
     * OFF: the engine binds loopback only. Never auto-detected into a bind.
     */
    static String getTailnetAddress(Context ctx) {
        String v = prefs(ctx).getString(KEY_TAILNET_ADDR, "");
        return v == null ? "" : v.trim();
    }

    static void setTailnetAddress(Context ctx, String addr) {
        prefs(ctx).edit().putString(KEY_TAILNET_ADDR, addr == null ? "" : addr.trim()).apply();
    }

    private static String generate() {
        byte[] buf = new byte[18];
        new SecureRandom().nextBytes(buf);
        StringBuilder sb = new StringBuilder(buf.length * 2);
        for (byte b : buf) sb.append(Character.forDigit((b >> 4) & 0xF, 16)).append(Character.forDigit(b & 0xF, 16));
        return sb.toString();
    }
}
