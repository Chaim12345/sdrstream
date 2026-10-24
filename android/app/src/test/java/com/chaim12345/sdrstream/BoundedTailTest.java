package com.chaim12345.sdrstream;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import java.io.StringWriter;
import org.junit.Test;

public class BoundedTailTest {
    @Test
    public void keepsNewestLinesWithinBudget() {
        BoundedTail tail = new BoundedTail(30);
        for (int i = 0; i < 10; i++) {
            tail.add("line-number-" + i); // 14 bytes each incl. newline
        }
        StringWriter w = new StringWriter();
        try {
            tail.writeTo(w);
        } catch (Exception e) {
            throw new AssertionError(e);
        }
        String out = w.toString();
        assertTrue(out.length() <= 30);
        assertTrue("newest line kept", out.contains("line-number-9"));
        assertFalse("oldest lines dropped", out.contains("line-number-0"));
    }

    @Test
    public void singleOversizedLineStillKept() {
        BoundedTail tail = new BoundedTail(5);
        tail.add("a much too long line");
        assertEquals(1, tail.lineCount());
    }
}
