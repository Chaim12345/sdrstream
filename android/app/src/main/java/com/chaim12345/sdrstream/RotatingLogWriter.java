package com.chaim12345.sdrstream;

import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;

/**
 * Bounded engine.log: appends lines up to maxBytes, then rolls once to
 * "<file>.1" (replacing any older backup) and continues in a fresh file.
 * Total on-disk size stays under ~2x maxBytes. The engine's log pipe used
 * to append without bound (EngineService.redirectOutput), so a long-running
 * engine could fill the app-private storage; the drain thread now writes
 * through this instead.
 */
final class RotatingLogWriter {
    private final File file;
    private final File backup;
    private final long maxBytes;
    private OutputStream out;
    private long size;

    RotatingLogWriter(File file, long maxBytes) {
        this.file = file;
        this.backup = new File(file.getPath() + ".1");
        this.maxBytes = maxBytes;
    }

    synchronized void writeLine(String line) throws IOException {
        byte[] b = (line + "\n").getBytes(StandardCharsets.UTF_8);
        if (out == null) {
            open();
        }
        if (size >= maxBytes) {
            roll();
        }
        out.write(b);
        size += b.length;
    }

    synchronized void flush() throws IOException {
        if (out != null) out.flush();
    }

    synchronized void close() {
        if (out != null) {
            try {
                out.close();
            } catch (IOException ignored) {
            }
            out = null;
        }
    }

    private void open() throws IOException {
        out = new FileOutputStream(file, true);
        size = file.exists() ? file.length() : 0;
    }

    private void roll() throws IOException {
        out.close();
        out = null;
        if (backup.exists() && !backup.delete()) {
            throw new IOException("cannot replace " + backup);
        }
        if (file.exists() && !file.renameTo(backup)) {
            throw new IOException("cannot roll " + file);
        }
        out = new FileOutputStream(file, false);
        size = 0;
        String marker = "--- rotated (previous output in " + backup.getName() + ") ---\n";
        byte[] m = marker.getBytes(StandardCharsets.UTF_8);
        out.write(m);
        size += m.length;
    }
}
