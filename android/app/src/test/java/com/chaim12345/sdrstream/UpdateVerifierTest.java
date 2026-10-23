package com.chaim12345.sdrstream;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import org.junit.Test;

import java.io.File;
import java.io.FileOutputStream;

public class UpdateVerifierTest {

    @Test
    public void sha256MatchesKnownVector() throws Exception {
        File f = File.createTempFile("upd", ".bin");
        try (FileOutputStream out = new FileOutputStream(f)) {
            out.write("abc".getBytes("UTF-8"));
        }
        assertEquals("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
                UpdateVerifier.sha256Hex(f));
        f.delete();
    }

    @Test
    public void sameSignersEquality() {
        byte[] k1 = {1, 2, 3};
        byte[] k2 = {4, 5, 6};
        assertTrue(UpdateVerifier.sameSigners(new byte[][]{k1}, new byte[][]{k1.clone()}));
        assertFalse(UpdateVerifier.sameSigners(new byte[][]{k1}, new byte[][]{k2}));
        // order-insensitive for multi-signer
        assertTrue(UpdateVerifier.sameSigners(new byte[][]{k1, k2}, new byte[][]{k2, k1}));
        // count mismatch
        assertFalse(UpdateVerifier.sameSigners(new byte[][]{k1}, new byte[][]{k1, k2}));
        // empty or null never matches
        assertFalse(UpdateVerifier.sameSigners(new byte[0][], new byte[0][]));
        assertFalse(UpdateVerifier.sameSigners(null, new byte[][]{k1}));
    }
}
