package com.chaim12345.sdrstream;

import org.junit.Test;
import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;

/** Pins the storage helper's existing redactor dependency with dummy data. */
public class SecretValueRedactionTest {
    @Test public void registeredDummyValueIsRemovedWithoutKeyword() {
        String dummy = "dummy-storage-registry-437a";
        DiagRedact.addSecretValue(dummy);
        String result = DiagRedact.redact("engine says " + dummy + " ok");
        assertFalse(result.contains(dummy));
        assertEquals("engine says [redacted] ok", result);
    }
    @Test public void shortValuesDoNotEraseOrdinaryWords() {
        DiagRedact.addSecretValue("wind");
        assertEquals("wind forecast", DiagRedact.redact("wind forecast"));
    }
}
