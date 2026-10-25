package com.chaim12345.sdrstream;

/**
 * Per-line processing for the engine log drain. The Go engine redacts its
 * log-package output at the source, but direct fmt.Fprint writes and
 * panics bypass that layer (QA review), so every line is redacted again
 * here before it reaches engine.log. Pure and unit-tested.
 */
final class LogDrain {
    static final int MAX_LINE = 8192;

    private LogDrain() {}

    /** Truncates oversized lines and redacts credentials/URLs/coordinates. */
    static String clean(String line) {
        if (line.length() > MAX_LINE) {
            line = line.substring(0, MAX_LINE) + "...[truncated]";
        }
        return DiagRedact.redact(line);
    }

    /**
     * True when the drain thread ended because the engine process was
     * closed under it (restart/stop): the read is interrupted by close()
     * on Android (InterruptedIOException). That is a normal shutdown, not
     * a crash - it must never land in crash.log (device evidence
     * 2026-10-25: a routine engine restart produced the only "crash"
     * entry and made the diagnostics look like the engine had crashed).
     */
    static boolean isBenignClose(Throwable t) {
        return t instanceof java.io.InterruptedIOException;
    }
}
