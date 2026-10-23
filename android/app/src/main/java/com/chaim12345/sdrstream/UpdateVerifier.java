package com.chaim12345.sdrstream;

import java.io.File;
import java.io.FileInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Comparator;
import java.util.List;

/** Pure verification helpers for the updater (unit-tested). */
final class UpdateVerifier {

    private UpdateVerifier() {}

    /** Streaming SHA-256 of a file as lowercase hex. */
    static String sha256Hex(File f) throws IOException {
        final MessageDigest md;
        try {
            md = MessageDigest.getInstance("SHA-256");
        } catch (NoSuchAlgorithmException e) {
            throw new IOException("SHA-256 unavailable", e);
        }
        byte[] buf = new byte[65536];
        try (InputStream in = new FileInputStream(f)) {
            int n;
            while ((n = in.read(buf)) >= 0) md.update(buf, 0, n);
        }
        StringBuilder sb = new StringBuilder(64);
        for (byte b : md.digest()) sb.append(Character.forDigit((b >> 4) & 0xF, 16))
                .append(Character.forDigit(b & 0xF, 16));
        return sb.toString();
    }

    /**
     * Order-insensitive equality of two signer sets (DER cert bytes). Both
     * sides must be non-empty and contain no duplicates; a one-element set
     * (our single stable key) compares as a plain equality.
     */
    static boolean sameSigners(byte[][] a, byte[][] b) {
        if (a == null || b == null || a.length == 0 || b.length == 0) return false;
        if (a.length != b.length) return false;
        List<byte[]> sa = sorted(a);
        List<byte[]> sb = sorted(b);
        for (int i = 0; i < sa.size(); i++) {
            if (!Arrays.equals(sa.get(i), sb.get(i))) return false;
        }
        return true;
    }

    private static List<byte[]> sorted(byte[][] in) {
        List<byte[]> out = new ArrayList<>(Arrays.asList(in));
        out.sort(new Comparator<byte[]>() {
            @Override public int compare(byte[] x, byte[] y) {
                int n = Math.min(x.length, y.length);
                for (int i = 0; i < n; i++) {
                    int d = (x[i] & 0xFF) - (y[i] & 0xFF);
                    if (d != 0) return d;
                }
                return x.length - y.length;
            }
        });
        return out;
    }
}
