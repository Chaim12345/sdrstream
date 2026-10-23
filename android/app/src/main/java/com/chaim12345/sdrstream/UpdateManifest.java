package com.chaim12345.sdrstream;

import org.json.JSONException;
import org.json.JSONObject;

import java.util.Locale;

/**
 * Parsed and validated update-feed manifest. Pure JVM logic (unit-tested).
 *
 * Feed format (served over HTTPS; plain HTTP only to private test hosts):
 * {
 *   "package": "com.chaim12345.sdrstream",
 *   "versionCode": 1034,
 *   "versionName": "0.1.3",        // optional, display only
 *   "url": "https://.../app.apk",
 *   "sha256": "<64 hex>",           // SHA-256 of the APK file
 *   "notes": "what changed",        // optional, display only
 *   "minSdk": 26                    // optional
 * }
 *
 * Trust model: the feed only names a candidate. Before install, the APK's
 * own SHA-256 must match sha256, its package must match ours, its version
 * must be strictly newer, and its signer must equal ours (checked against
 * the installed app's signing info, not the feed). A compromised feed can
 * therefore offer garbage, never a foreign or downgraded app.
 */
final class UpdateManifest {
    final String pkg;
    final int versionCode;
    final String versionName;
    final String url;
    final String sha256;
    final String notes;
    final int minSdk;

    private UpdateManifest(String pkg, int versionCode, String versionName,
                           String url, String sha256, String notes, int minSdk) {
        this.pkg = pkg;
        this.versionCode = versionCode;
        this.versionName = versionName;
        this.url = url;
        this.sha256 = sha256;
        this.notes = notes;
        this.minSdk = minSdk;
    }

    static UpdateManifest parse(String json, String expectedPackage) throws UpdateException {
        final JSONObject o;
        try {
            o = new JSONObject(json);
        } catch (JSONException e) {
            throw new UpdateException("The update feed returned unreadable data.", e);
        }
        String pkg = o.optString("package", "");
        if (!expectedPackage.equals(pkg)) {
            throw new UpdateException("The update feed is for a different app.");
        }
        int versionCode = o.optInt("versionCode", -1);
        if (versionCode < 1) {
            throw new UpdateException("The update feed has no valid version.");
        }
        String url = o.optString("url", "");
        if (!isAllowedUrl(url)) {
            throw new UpdateException("The update feed points at an untrusted download address.");
        }
        String sha256 = o.optString("sha256", "").toLowerCase(Locale.US);
        if (!isHex64(sha256)) {
            throw new UpdateException("The update feed has no valid checksum.");
        }
        int minSdk = o.optInt("minSdk", 0);
        return new UpdateManifest(pkg, versionCode, o.optString("versionName", ""),
                url, sha256, o.optString("notes", ""), minSdk);
    }

    static boolean isHex64(String s) {
        if (s == null || s.length() != 64) return false;
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            boolean ok = (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F');
            if (!ok) return false;
        }
        return true;
    }

    /**
     * HTTPS is required, except to hosts that cannot leave the local network:
     * loopback, RFC1918 and the Tailscale CGNAT range 100.64.0.0/10, so a QA
     * feed on the device owner's own tailnet/LAN can be plain HTTP. The
     * checksum and signer checks still apply to whatever is downloaded.
     */
    static boolean isAllowedUrl(String url) {
        if (url == null) return false;
        String lower = url.toLowerCase(Locale.US);
        if (lower.startsWith("https://")) return true;
        if (!lower.startsWith("http://")) return false;
        String rest = lower.substring("http://".length());
        int slash = rest.indexOf('/');
        String host = slash >= 0 ? rest.substring(0, slash) : rest;
        int colon = host.lastIndexOf(':');
        if (colon >= 0) host = host.substring(0, colon);
        return isPrivateTestHost(host);
    }

    static boolean isPrivateTestHost(String host) {
        if (host == null || host.isEmpty()) return false;
        if (host.equals("localhost")) return true;
        String[] parts = host.split("\\.");
        if (parts.length != 4) return false;
        int[] o = new int[4];
        try {
            for (int i = 0; i < 4; i++) {
                o[i] = Integer.parseInt(parts[i]);
                if (o[i] < 0 || o[i] > 255) return false;
            }
        } catch (NumberFormatException e) {
            return false; // hostname, not a literal: require DNS-proof HTTPS
        }
        if (o[0] == 127) return true;                       // loopback
        if (o[0] == 10) return true;                        // RFC1918
        if (o[0] == 192 && o[1] == 168) return true;        // RFC1918
        if (o[0] == 172 && o[1] >= 16 && o[1] <= 31) return true; // RFC1918
        if (o[0] == 100 && o[1] >= 64 && o[1] <= 127) return true; // Tailscale CGNAT
        return false;
    }
}
