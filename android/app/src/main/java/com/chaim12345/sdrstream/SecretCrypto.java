package com.chaim12345.sdrstream;

import java.nio.charset.StandardCharsets;

import javax.crypto.Cipher;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;

/**
 * Pure-JDK AES/GCM helper used by SecretStore. No Android imports, so the
 * round trip is unit-testable on the JVM.
 *
 * Encryption NEVER supplies an IV: AndroidKeyStore keys default to
 * setRandomizedEncryptionRequired(true) (IND-CPA), which rejects a
 * caller-provided IV for GCM. The provider generates the IV and we read it
 * back with Cipher.getIV(). Do not disable the randomized-encryption policy.
 */
final class SecretCrypto {
    static final String TRANSFORM = "AES/GCM/NoPadding";
    static final int GCM_TAG_BITS = 128;

    private SecretCrypto() {}

    /** Result of an encryption: provider-generated IV plus ciphertext (with GCM tag). */
    static final class Sealed {
        final byte[] iv;
        final byte[] ciphertext;

        Sealed(byte[] iv, byte[] ciphertext) {
            this.iv = iv;
            this.ciphertext = ciphertext;
        }
    }

    static Sealed encrypt(SecretKey key, String secret) throws Exception {
        Cipher c = Cipher.getInstance(TRANSFORM);
        c.init(Cipher.ENCRYPT_MODE, key); // no IV: the provider generates it
        byte[] iv = c.getIV();
        if (iv == null || iv.length == 0) {
            throw new IllegalStateException("provider returned no IV");
        }
        return new Sealed(iv, c.doFinal(secret.getBytes(StandardCharsets.UTF_8)));
    }

    static String decrypt(SecretKey key, byte[] iv, byte[] ciphertext) throws Exception {
        Cipher c = Cipher.getInstance(TRANSFORM);
        c.init(Cipher.DECRYPT_MODE, key, new GCMParameterSpec(GCM_TAG_BITS, iv));
        return new String(c.doFinal(ciphertext), StandardCharsets.UTF_8);
    }
}
