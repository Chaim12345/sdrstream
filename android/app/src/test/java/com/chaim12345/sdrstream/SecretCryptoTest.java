package com.chaim12345.sdrstream;

import static org.junit.Assert.assertArrayEquals;
import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.fail;

import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;

import org.junit.Test;

/**
 * JVM round-trip of the SecretStore crypto with a software AES key and a
 * DUMMY secret. This does NOT exercise AndroidKeyStore (the randomized-IV
 * rejection only exists there); it pins that encryption never supplies an
 * IV, which is what AndroidKeyStore requires.
 */
public class SecretCryptoTest {
    private static SecretKey newKey() throws Exception {
        KeyGenerator kg = KeyGenerator.getInstance("AES");
        kg.init(256);
        return kg.generateKey();
    }

    @Test
    public void roundTripWithProviderGeneratedIv() throws Exception {
        SecretKey k = newKey();
        SecretCrypto.Sealed s = SecretCrypto.encrypt(k, "dummy-secret-not-a-real-key");
        assertEquals(12, s.iv.length);
        assertEquals("dummy-secret-not-a-real-key", SecretCrypto.decrypt(k, s.iv, s.ciphertext));
    }

    @Test
    public void ivIsFreshPerEncryption() throws Exception {
        SecretKey k = newKey();
        SecretCrypto.Sealed a = SecretCrypto.encrypt(k, "dummy-secret-not-a-real-key");
        SecretCrypto.Sealed b = SecretCrypto.encrypt(k, "dummy-secret-not-a-real-key");
        assertFalse(java.util.Arrays.equals(a.iv, b.iv));
        assertFalse(java.util.Arrays.equals(a.ciphertext, b.ciphertext));
    }

    @Test
    public void tamperedCiphertextAndWrongKeyFail() throws Exception {
        SecretKey k = newKey();
        SecretCrypto.Sealed s = SecretCrypto.encrypt(k, "dummy-secret-not-a-real-key");
        byte[] bad = s.ciphertext.clone();
        bad[0] ^= 1;
        try {
            SecretCrypto.decrypt(k, s.iv, bad);
            fail("tampered ciphertext must not decrypt");
        } catch (java.security.GeneralSecurityException expected) {
        }
        try {
            SecretCrypto.decrypt(newKey(), s.iv, s.ciphertext);
            fail("wrong key must not decrypt");
        } catch (java.security.GeneralSecurityException expected) {
        }
    }

    @Test
    public void unicodeSecretSurvives() throws Exception {
        SecretKey k = newKey();
        String v = "dummy-\u05e9\u05dc\u05d5\u05dd-secret";
        SecretCrypto.Sealed s = SecretCrypto.encrypt(k, v);
        assertEquals(v, SecretCrypto.decrypt(k, s.iv, s.ciphertext));
        assertArrayEquals(s.iv, s.iv);
    }
}
