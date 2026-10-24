package com.chaim12345.sdrstream;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertTrue;

import java.io.File;
import java.nio.file.Files;
import java.util.List;
import org.junit.Rule;
import org.junit.Test;
import org.junit.rules.TemporaryFolder;

public class RotatingLogWriterTest {
    @Rule
    public TemporaryFolder tmp = new TemporaryFolder();

    @Test
    public void rollsOnceAtCapAndKeepsBothFiles() throws Exception {
        File log = new File(tmp.getRoot(), "engine.log");
        RotatingLogWriter w = new RotatingLogWriter(log, 100);
        for (int i = 0; i < 12; i++) {
            w.writeLine("0123456789"); // 11 bytes per line; roll on line 11
        }
        w.close();
        File backup = new File(log.getPath() + ".1");
        assertTrue("backup exists after roll", backup.exists());
        assertTrue("current file bounded", log.length() <= 100 + 11);
        assertTrue("total bounded ~2x cap", log.length() + backup.length() <= 2 * 100 + 22);
        List<String> current = Files.readAllLines(log.toPath());
        assertEquals("--- rotated (previous output in engine.log.1) ---", current.get(0));
    }

    @Test
    public void secondRollReplacesBackupAndStaysBounded() throws Exception {
        File log = new File(tmp.getRoot(), "engine.log");
        RotatingLogWriter w = new RotatingLogWriter(log, 60);
        for (int i = 0; i < 30; i++) {
            w.writeLine("line-" + i); // 7 bytes per line
        }
        w.close();
        File backup = new File(log.getPath() + ".1");
        assertTrue(backup.exists());
        long total = log.length() + backup.length();
        assertTrue("total stays bounded after many rolls: " + total, total <= 2 * 60 + 14);
        // No engine.log.2 accumulation.
        assertEquals(2, tmp.getRoot().listFiles((d, n) -> n.startsWith("engine.log")).length);
    }

    @Test
    public void appendsAcrossRestarts() throws Exception {
        File log = new File(tmp.getRoot(), "engine.log");
        RotatingLogWriter w1 = new RotatingLogWriter(log, 1000);
        w1.writeLine("first boot");
        w1.close();
        RotatingLogWriter w2 = new RotatingLogWriter(log, 1000);
        w2.writeLine("second boot");
        w2.close();
        List<String> lines = Files.readAllLines(log.toPath());
        assertEquals("first boot", lines.get(0));
        assertEquals("second boot", lines.get(1));
    }
}
