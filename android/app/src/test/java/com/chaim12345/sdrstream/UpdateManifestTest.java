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
        UpdateManifest m = UpdateManifest.parse(valid(), PKG);
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
                valid().replace("\"minSdk\":26", "\"minSdk\":26,\"futureField\":{\"x\":1}"), PKG);
        assertEquals(1034, m.versionCode);
    }

    @Test
    public void rejectsOtherPackage() {
        expectInvalid(valid().replace(PKG, "com.example.other"), PKG);
    }

    @Test
    public void rejectsMissingVersion() {
        expectInvalid("{\"package\":\"" + PKG + "\",\"url\":\"https://x/y.apk\","
                + "\"sha256\":\"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"}", PKG);
    }

    @Test
    public void rejectsHttpDownloadFromPublicHost() {
        expectInvalid(valid().replace("https://releases.example.com", "http://releases.example.com"), PKG);
    }

    @Test
    public void allowsHttpToTailnetAndLanHosts() throws Exception {
        for (String host : new String[]{"100.99.176.4", "100.64.0.1", "100.127.255.254",
                "127.0.0.1", "10.0.0.5", "192.168.1.20", "172.16.0.3", "172.31.255.255"}) {
            UpdateManifest m = UpdateManifest.parse(
                    valid().replace("https://releases.example.com", "http://" + host + ":8080"), PKG);
            assertTrue(m.url.startsWith("http://" + host));
        }
    }

    @Test
    public void rejectsHttpToNonPrivateLookalikes() {
        for (String host : new String[]{"100.63.0.1", "100.128.0.1", "172.15.0.1", "172.32.0.1",
                "11.0.0.5", "192.167.1.1", "example.com", "100.99.176.4.evil.com"}) {
            expectInvalid(valid().replace("https://releases.example.com", "http://" + host), PKG);
        }
    }

    @Test
    public void rejectsBadChecksum() {
        expectInvalid(valid().replace(
                "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
                "0123456789abcdef"), PKG); // too short
        expectInvalid(valid().replace(
                "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
                "zz3456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), PKG); // non-hex
    }

    @Test
    public void rejectsGarbageJson() {
        expectInvalid("not json at all", PKG);
        expectInvalid("", PKG);
    }

    @Test
    public void acceptsUppercaseShaAndNormalizes() throws Exception {
        UpdateManifest m = UpdateManifest.parse(valid().replace(
                "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
                "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"), PKG);
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

    private static void expectInvalid(String json, String pkg) {
        try {
            UpdateManifest.parse(json, pkg);
            fail("expected UpdateException for: " + json);
        } catch (UpdateException expected) {
            assertTrue(expected.getMessage() != null && !expected.getMessage().isEmpty());
        }
    }
}
