package com.chaim12345.sdrstream;

import android.content.SharedPreferences;
import android.util.Base64;

import java.nio.charset.StandardCharsets;
import java.security.KeyStore;
import java.security.SecureRandom;

import javax.crypto.Cipher;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;

/**
 * Crash-report relay password, entered once in the app UI and stored
 * encrypted under an AES/GCM key in AndroidKeyStore (hardware-backed where
 * the device supports it). Only the ciphertext (iv || ct, base64) touches
 * SharedPreferences; the key never leaves the Keystore. Nothing sensitive
 * is embedded in the APK or the repo.
 *
 * Thin Android-only wrapper: KeyStore "AndroidKeyStore" is unavailable on
 * the JVM, so this class is device-verified rather than unit-tested; all
 * policy around it lives in the JVM-tested DiagUploader core.
 */
final class DiagCredentials {

    private static final String KEYSTORE = "AndroidKeyStore";
    private static final String KEY_ALIAS = "sdrstream_diag_smtp";
    private static final String PREF_CT = "smtp_password_ct";
    private static final int GCM_TAG_BITS = 128;
    private static final int GCM_IV_BYTES = 12;

    private DiagCredentials() {}

    static boolean hasPassword(SharedPreferences prefs) {
        return !prefs.getString(PREF_CT, "").isEmpty();
    }

    /** Encrypts and stores the password. Throws if the Keystore is unusable. */
    static void store(SharedPreferences prefs, char[] password) throws Exception {
        Cipher c = Cipher.getInstance("AES/GCM/NoPadding");
        c.init(Cipher.ENCRYPT_MODE, key(true));
        byte[] ct = c.doFinal(new String(password).getBytes(StandardCharsets.UTF_8));
        byte[] iv = c.getIV();
        byte[] blob = new byte[iv.length + ct.length];
        System.arraycopy(iv, 0, blob, 0, iv.length);
        System.arraycopy(ct, 0, blob, iv.length, ct.length);
        prefs.edit().putString(PREF_CT, Base64.encodeToString(blob, Base64.NO_WRAP)).apply();
    }

    /** Decrypts the stored password; "" when unset or undecryptable (key wiped, etc). */
    static String load(SharedPreferences prefs) {
        try {
            byte[] blob = Base64.decode(prefs.getString(PREF_CT, ""), Base64.NO_WRAP);
            if (blob.length <= GCM_IV_BYTES) return "";
            Cipher c = Cipher.getInstance("AES/GCM/NoPadding");
            c.init(Cipher.DECRYPT_MODE, key(false),
                    new GCMParameterSpec(GCM_TAG_BITS, blob, 0, GCM_IV_BYTES));
            return new String(c.doFinal(blob, GCM_IV_BYTES, blob.length - GCM_IV_BYTES),
                    StandardCharsets.UTF_8);
        } catch (Exception e) {
            return "";
        }
    }

    static void clear(SharedPreferences prefs) {
        prefs.edit().remove(PREF_CT).apply();
    }

    private static SecretKey key(boolean create) throws Exception {
        KeyStore ks = KeyStore.getInstance(KEYSTORE);
        ks.load(null);
        if (!create) {
            java.security.Key k = ks.getKey(KEY_ALIAS, null);
            if (k instanceof SecretKey) return (SecretKey) k;
            throw new IllegalStateException("diag key missing");
        }
        java.security.Key existing = ks.getKey(KEY_ALIAS, null);
        if (existing instanceof SecretKey) return (SecretKey) existing;
        KeyGenerator g = KeyGenerator.getInstance("AES", KEYSTORE);
        g.init(new android.security.keystore.KeyGenParameterSpec.Builder(KEY_ALIAS,
                android.security.keystore.KeyProperties.PURPOSE_ENCRYPT
                        | android.security.keystore.KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(android.security.keystore.KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(android.security.keystore.KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build());
        return g.generateKey();
    }
}
