package com.chaim12345.sdrstream;

import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import java.io.IOException;
import java.io.InterruptedIOException;

import org.junit.Test;

/** Device-log findings from the S23 (2026-10-25): crash.log mislabel + logcat spam. */
public class DeviceLogFixesTest {

    @Test
    public void drainCloseDuringRestartIsNotACrash() {
        // Exact type from the device crash.log: read interrupted by close()
        // when stopEngine() destroys the process under the drain thread.
        InterruptedIOException e = new InterruptedIOException("read interrupted by close() on another thread");
        assertTrue(LogDrain.isBenignClose(e));
    }

    @Test
    public void realDrainFailuresStillCrash() {
        assertFalse(LogDrain.isBenignClose(new IOException("unexpected fs error")));
        assertFalse(LogDrain.isBenignClose(new RuntimeException("boom")));
        assertFalse(LogDrain.isBenignClose(new IOException("Connection reset by peer")));
    }

    @Test
    public void webViewFrameRateSpamFiltered() {
        assertFalse(JavaLog.keepLine(
                "10-25 08:10:41.123 12345 12345 I chromium: [INFO:CONSOLE] setRequestedFrameRate 60"));
        assertFalse(JavaLog.keepLine("10-25 D/WebView: setRequestedFrameRate called"));
    }

    @Test
    public void realLogLinesKept() {
        assertTrue(JavaLog.keepLine("10-25 08:10:37.100 12345 12345 I sdrstream: engine started"));
        assertTrue(JavaLog.keepLine("10-25 E/AndroidRuntime: FATAL EXCEPTION: main"));
    }
}
