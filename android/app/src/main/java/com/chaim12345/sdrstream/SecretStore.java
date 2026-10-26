package com.chaim12345.sdrstream;

import android.content.Context;
import android.content.SharedPreferences;
import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;
import android.util.Base64;

import java.security.KeyStore;

import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;

/**
 * Android-Keystore-backed storage for hosted-provider API keys (NVIDIA NIM,
 * Gemini). The plaintext key is never written to SharedPreferences, files,
 * logs or the APK: only AES/GCM ciphertext + IV land in prefs, wrapped by a
 * non-exportable key in AndroidKeyStore (hardware-backed where the device
 * provides it).
 *
 * Honest residuals this does NOT remove: the plaintext exists as a Java
 * String while entered and lives in the engine process environment while
 * the engine runs - Java strings are not reliably zeroable, and process
 * memory is readable on rooted devices or with a debugger attached. GCM is
 * encryption at rest, not attestation: it cannot prove which build or
 * caller decrypted the value. Engine env vars are also readable by the
 * engine's own diagnostics paths - DiagRedact scrubs registered values as
 * defense in depth, but the primary boundary is: no key entered, no key
 * anywhere.
 */
final class SecretStore {
    static final String KEY_NIM = "nim_api_key";
    static final String KEY_GEMINI = "gemini_api_key";

    private static final String ANDROID_KEYSTORE = "AndroidKeyStore";
    private static final String WRAP_KEY = "sdrstream_secret_wrap";

    private SecretStore() {}

    private static SecretKey getOrCreateWrapKey() throws Exception {
        KeyStore ks = KeyStore.getInstance(ANDROID_KEYSTORE);
        ks.load(null);
        if (ks.containsAlias(WRAP_KEY)) {
            return ((KeyStore.SecretKeyEntry) ks.getEntry(WRAP_KEY, null)).getSecretKey();
        }
        KeyGenerator kg = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, ANDROID_KEYSTORE);
        kg.init(new KeyGenParameterSpec.Builder(WRAP_KEY,
                KeyProperties.PURPOSE_ENCRYPT | KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build());
        return kg.generateKey();
    }

    /** True when the Keystore wrap key can be created/read on this device. */
    static boolean isAvailable() {
        try {
            return getOrCreateWrapKey() != null;
        } catch (Throwable t) {
            return false;
        }
    }

    /** Encrypts and persists the secret. Returns false (stores nothing) on failure. */
    static boolean put(Context ctx, String prefKey, String secret) {
        if (secret == null || secret.isEmpty()) return false;
        try {
            SecretCrypto.Sealed sealed = SecretCrypto.encrypt(getOrCreateWrapKey(), secret);
            SharedPreferences.Editor e = Prefs.prefs(ctx).edit();
            e.putString(prefKey + "_enc", Base64.encodeToString(sealed.ciphertext, Base64.NO_WRAP));
            e.putString(prefKey + "_iv", Base64.encodeToString(sealed.iv, Base64.NO_WRAP));
            e.apply();
            DiagRedact.addSecretValue(secret);
            return true;
        } catch (Throwable t) {
            return false;
        }
    }

    /** Returns the decrypted secret, or null when absent or undecryptable. */
    static String get(Context ctx, String prefKey) {
        SharedPreferences p = Prefs.prefs(ctx);
        String encB64 = p.getString(prefKey + "_enc", null);
        String ivB64 = p.getString(prefKey + "_iv", null);
        if (encB64 == null || ivB64 == null) return null;
        try {
            String secret = SecretCrypto.decrypt(getOrCreateWrapKey(),
                    Base64.decode(ivB64, Base64.NO_WRAP), Base64.decode(encB64, Base64.NO_WRAP));
            DiagRedact.addSecretValue(secret);
            return secret;
        } catch (Throwable t) {
            return null;
        }
    }

    static boolean has(Context ctx, String prefKey) {
        return Prefs.prefs(ctx).contains(prefKey + "_enc");
    }

    static void clear(Context ctx, String prefKey) {
        Prefs.prefs(ctx).edit().remove(prefKey + "_enc").remove(prefKey + "_iv").apply();
    }
}
