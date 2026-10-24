package com.chaim12345.sdrstream;

import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import org.junit.Test;

public class LogDrainTest {
    /** QA's vector: a net/http error embeds the full URL with its query token. */
    @Test
    public void httpErrorUrlTokenNeverReachesEngineLog() {
        String out = LogDrain.clean("Get \"https://h/x?token=SECRET\": dial tcp 10.0.0.1:443: connect: refused");
        assertFalse("SECRET leaked through the drain: " + out, out.contains("SECRET"));
        assertTrue(out.contains("https://h/x?[redacted]"));
    }

    @Test
    public void panicStyleLineRedacted() {
        String out = LogDrain.clean("panic: feed https://user:pw@host/live?lat=32.0853 died");
        assertFalse(out.contains("user:pw"));
        assertFalse(out.contains("32.0853"));
    }

    @Test
    public void oversizedLineTruncated() {
        StringBuilder big = new StringBuilder();
        for (int i = 0; i < 9000; i++) big.append('x');
        String out = LogDrain.clean(big.toString());
        assertTrue(out.length() < 9000);
        assertTrue(out.endsWith("...[truncated]"));
    }
}
