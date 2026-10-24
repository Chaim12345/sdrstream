package com.chaim12345.sdrstream;

import java.io.IOException;
import java.io.Writer;
import java.util.ArrayDeque;

/**
 * Keeps only the newest lines within a byte budget: logcat -d can dump
 * megabytes, but a shareable javalog.txt should carry only the recent tail.
 * Bytes are accounted as UTF-8 length of line plus newline.
 */
final class BoundedTail {
    private final int maxBytes;
    private final ArrayDeque<String> lines = new ArrayDeque<>();
    private int bytes;

    BoundedTail(int maxBytes) {
        this.maxBytes = maxBytes;
    }

    void add(String line) {
        int cost = utf8(line) + 1;
        lines.addLast(line);
        bytes += cost;
        while (bytes > maxBytes && lines.size() > 1) {
            bytes -= utf8(lines.pollFirst()) + 1;
        }
    }

    int lineCount() {
        return lines.size();
    }

    void writeTo(Writer w) throws IOException {
        for (String line : lines) {
            w.write(line);
            w.write('\n');
        }
    }

    private static int utf8(String s) {
        int n = 0;
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            if (c < 0x80) n++;
            else if (c < 0x800) n += 2;
            else n += 3; // surrogate pairs land within the budget slop
        }
        return n;
    }
}
