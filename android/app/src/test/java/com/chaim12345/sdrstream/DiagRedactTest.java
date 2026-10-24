package com.chaim12345.sdrstream;

import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import org.junit.Test;

/** Mirrors go/internal/web/logring_redaction_test.go: same vectors, same verdicts. */
public class DiagRedactTest {
    @Test
    public void stripsSecretsKeepsDiagnostics() {
        String[] in = {
            "feed: https://user:secret@feeds.example.com/live?lat=32.0853&lon=34.7818&key=abc123 connected",
            "login attempt password=hunter2 from 127.0.0.1",
            "Cookie: sdrstream_session=deadbeef; other=1",
            "position lat=32.0853, lon=34.7818",
            "decoded CQ CQ DE TEST grid KO87 145.8000 MHz",
        };
        StringBuilder out = new StringBuilder();
        for (String l : in) out.append(DiagRedact.redact(l)).append('\n');
        String s = out.toString();
        for (String banned : new String[]{"secret@", "abc123", "hunter2", "deadbeef", "32.0853", "34.7818", "user:secret"}) {
            assertFalse("redaction leaked " + banned + " in:\n" + s, s.contains(banned));
        }
        for (String kept : new String[]{"feeds.example.com", "145.8000 MHz", "decoded CQ CQ DE TEST"}) {
            assertTrue("redaction ate legitimate content " + kept + " in:\n" + s, s.contains(kept));
        }
    }

    @Test
    public void keepsUrlHostButStripsQuery() {
        String out = DiagRedact.redact("feed https://feeds.example.com/live?key=abc123 ok");
        assertTrue(out.contains("https://feeds.example.com/live?[redacted]"));
        assertFalse(out.contains("abc123"));
    }
}
