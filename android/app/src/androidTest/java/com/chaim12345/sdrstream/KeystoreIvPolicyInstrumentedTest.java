package com.chaim12345.sdrstream;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;

import androidx.test.ext.junit.runners.AndroidJUnit4;

import java.security.GeneralSecurityException;
import java.security.KeyStore;

import javax.crypto.Cipher;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;

import org.junit.After;
import org.junit.Test;
import org.junit.runner.RunWith;

/**
 * Platform evidence independent of SecretStore: on a real AndroidKeyStore, a
 * default (IND-CPA) AES/GCM key rejects a caller-provided IV on encrypt and
 * accepts a provider-generated one. Test-only alias, dummy plaintext only.
 */
@RunWith(AndroidJUnit4.class)
public class KeystoreIvPolicyInstrumentedTest {
    private static final String ALIAS = "qa_iv_policy_test_key";

    private static SecretKey newKey() throws Exception {
        KeyGenerator kg = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore");
        kg.init(new KeyGenParameterSpec.Builder(ALIAS,
                KeyProperties.PURPOSE_ENCRYPT | KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build());
        return kg.generateKey();
    }

    @After
    public void cleanup() throws Exception {
        KeyStore ks = KeyStore.getInstance("AndroidKeyStore");
        ks.load(null);
        if (ks.containsAlias(ALIAS)) ks.deleteEntry(ALIAS);
    }

    @Test
    public void callerIvRejectedByKeystore() throws Exception {
        SecretKey k = newKey();
        Cipher c = Cipher.getInstance("AES/GCM/NoPadding");
        try {
            c.init(Cipher.ENCRYPT_MODE, k, new GCMParameterSpec(128, new byte[12]));
            c.doFinal("dummy".getBytes("UTF-8"));
            fail("caller-provided GCM IV was accepted; the doc-based claim does not hold on this platform");
        } catch (GeneralSecurityException expected) {
            System.out.println("IV_POLICY caller IV rejected: " + expected.getClass().getName());
        }
    }

    @Test
    public void providerIvAccepted() throws Exception {
        SecretKey k = newKey();
        Cipher c = Cipher.getInstance("AES/GCM/NoPadding");
        c.init(Cipher.ENCRYPT_MODE, k);
        byte[] iv = c.getIV();
        assertEquals(12, iv.length);
        byte[] enc = c.doFinal("dummy".getBytes("UTF-8"));
        Cipher d = Cipher.getInstance("AES/GCM/NoPadding");
        d.init(Cipher.DECRYPT_MODE, k, new GCMParameterSpec(128, iv));
        assertTrue(new String(d.doFinal(enc), "UTF-8").equals("dummy"));
    }
}
