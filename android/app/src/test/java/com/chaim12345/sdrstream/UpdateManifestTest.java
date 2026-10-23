package com.chaim12345.sdrstream;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import org.junit.Test;

public class UpdateManifestTest {
    private static final String PKG = "com.chaim12345.sdrstream";

    private static String valid() {
        return "{"
                + "\"package\":\"" + PKG + "\","
                + "\"versionCode\":1034,"
                + "\"versionName\":\"0.1.3\","
                + "\"url\":\"https://releases.example.com/app.apk\","
                + "\"sha256\":\"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\","
                + "\"notes\":\"engine fixes\","
                + "\"minSdk\":26"
                + "}";
    }

    @Test
    public void parsesValidManifest() throws Exception {
        UpdateManifest m = UpdateManifest.parse(valid(), PKG, null);
        assertEquals(PKG, m.pkg);
        assertEquals(1034, m.versionCode);
        assertEquals("0.1.3", m.versionName);
        assertEquals("https://releases.example.com/app.apk", m.url);
        assertEquals("engine fixes", m.notes);
        assertEquals(26, m.minSdk);
    }

    @Test
    public void toleratesUnknownFields() throws Exception {
        UpdateManifest m = UpdateManifest.parse(
                valid().replace("\"minSdk\":26", "\"minSdk\":26,\"futureField\":{\"x\":1}"), PKG, null);
        assertEquals(1034, m.versionCode);
    }

    @Test
    public void rejectsOtherPackage() {
        expectInvalid(valid().replace(PKG, "com.example.other"), PKG, null);
    }

    @Test
    public void rejectsMissingVersion() {
        expectInvalid("{\"package\":\"" + PKG + "\",\"url\":\"https://x/y.apk\","
                + "\"sha256\":\"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"}", PKG, null);
    }

    @Test
    public void rejectsHttpDownloadFromPublicHost() {
        expectInvalid(valid().replace("https://releases.example.com", "http://releases.example.com"), PKG, "100.99.176.4");
    }

    @Test
    public void allowsHttpToLoopbackAlways() throws Exception {
        for (String host : new String[]{"127.0.0.1", "127.0.1.1", "localhost"}) {
            UpdateManifest m = UpdateManifest.parse(
                    valid().replace("https://releases.example.com", "http://" + host + ":8080"), PKG, null);
            assertTrue(m.url.startsWith("http://" + host));
        }
    }

    @Test
    public void allowsHttpOnlyToTheExactConfiguredHost() throws Exception {
        // The caller vetted 100.99.176.4 (user-configured + VPN check).
        UpdateManifest m = UpdateManifest.parse(
                valid().replace("https://releases.example.com", "http://100.99.176.4:8080"),
                PKG, "100.99.176.4");
        assertTrue(m.url.startsWith("http://100.99.176.4"));
    }

    @Test
    public void rejectsHttpToEveryOtherHostEvenPrivateRanges() {
        // CGNAT/RFC1918 ranges alone are never trusted: only the exact
        // caller-vetted host qualifies.
        for (String host : new String[]{"100.64.0.1", "100.127.255.254", "10.0.0.5",
                "192.168.1.20", "172.16.0.3", "100.99.176.5", "example.com",
                "100.99.176.4.evil.com"}) {
            expectInvalid(valid().replace("https://releases.example.com", "http://" + host),
                    PKG, "100.99.176.4");
        }
        // No vetted host at all: even the CGNAT literal is refused.
        expectInvalid(valid().replace("https://releases.example.com", "http://100.99.176.4"),
                PKG, null);
    }

    @Test
    public void rejectsBadChecksum() {
        expectInvalid(valid().replace(
                "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
                "0123456789abcdef"), PKG, null); // too short
        expectInvalid(valid().replace(
                "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
                "zz3456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), PKG, null); // non-hex
    }

    @Test
    public void rejectsGarbageJson() {
        expectInvalid("not json at all", PKG, null);
        expectInvalid("", PKG, null);
    }

    @Test
    public void acceptsUppercaseShaAndNormalizes() throws Exception {
        UpdateManifest m = UpdateManifest.parse(valid().replace(
                "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
                "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"), PKG, null);
        assertEquals("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", m.sha256);
    }

    @Test
    public void hex64() {
        assertTrue(UpdateManifest.isHex64("a".repeat(64)));
        assertFalse(UpdateManifest.isHex64("a".repeat(63)));
        assertFalse(UpdateManifest.isHex64("a".repeat(65)));
        assertFalse(UpdateManifest.isHex64("g".repeat(64)));
        assertFalse(UpdateManifest.isHex64(null));
    }

    @Test
    public void cgnatLiteralDetection() {
        assertTrue(UpdateManifest.isCgnatLiteral("100.64.0.1"));
        assertTrue(UpdateManifest.isCgnatLiteral("100.127.255.254"));
        assertFalse(UpdateManifest.isCgnatLiteral("100.63.255.255"));
        assertFalse(UpdateManifest.isCgnatLiteral("100.128.0.1"));
        assertFalse(UpdateManifest.isCgnatLiteral("example.com"));
        assertFalse(UpdateManifest.isCgnatLiteral(null));
    }

    private static void expectInvalid(String json, String pkg, String plainHttpHost) {
        try {
            UpdateManifest.parse(json, pkg, plainHttpHost);
            fail("expected UpdateException for: " + json);
        } catch (UpdateException expected) {
            assertTrue(expected.getMessage() != null && !expected.getMessage().isEmpty());
        }
    }
}
