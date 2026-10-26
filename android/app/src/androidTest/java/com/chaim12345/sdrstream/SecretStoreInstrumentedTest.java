package com.chaim12345.sdrstream;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import android.content.Context;

import androidx.test.core.app.ApplicationProvider;
import androidx.test.ext.junit.runners.AndroidJUnit4;

import org.junit.After;
import org.junit.Test;
import org.junit.runner.RunWith;

/**
 * Real AndroidKeyStore round trip with a DUMMY value under a test-only pref
 * key. Never uses a real provider key, never touches the consent flags, and
 * cleans up after itself. Proposal only: not wired into any workflow.
 */
@RunWith(AndroidJUnit4.class)
public class SecretStoreInstrumentedTest {
    private static final String TEST_KEY = "qa_dummy_secret_test";
    private final Context ctx = ApplicationProvider.getApplicationContext();

    @After
    public void cleanup() {
        SecretStore.clear(ctx, TEST_KEY);
    }

    @Test
    public void keystoreIsAvailable() {
        assertTrue(SecretStore.isAvailable());
    }

    @Test
    public void putGetClearRoundTrip() {
        assertNull(SecretStore.get(ctx, TEST_KEY));
        assertTrue("put must succeed on a real AndroidKeyStore", SecretStore.put(ctx, TEST_KEY, "dummy-not-a-real-key-0001"));
        assertTrue(SecretStore.has(ctx, TEST_KEY));
        assertEquals("dummy-not-a-real-key-0001", SecretStore.get(ctx, TEST_KEY));
        assertTrue("put must succeed on a real AndroidKeyStore", SecretStore.put(ctx, TEST_KEY, "dummy-not-a-real-key-0002"));
        assertEquals("dummy-not-a-real-key-0002", SecretStore.get(ctx, TEST_KEY));
        SecretStore.clear(ctx, TEST_KEY);
        assertFalse(SecretStore.has(ctx, TEST_KEY));
        assertNull(SecretStore.get(ctx, TEST_KEY));
    }

    @Test
    public void plaintextNeverInPrefs() {
        assertTrue("put must succeed on a real AndroidKeyStore", SecretStore.put(ctx, TEST_KEY, "dummy-not-a-real-key-0003"));
        for (Object v : Prefs.prefs(ctx).getAll().values()) {
            assertFalse(String.valueOf(v).contains("dummy-not-a-real-key-0003"));
        }
    }
}
