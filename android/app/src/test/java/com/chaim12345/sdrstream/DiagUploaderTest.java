package com.chaim12345.sdrstream;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertTrue;

import java.util.HashMap;
import java.util.Map;

import org.junit.Before;
import org.junit.Test;

/**
 * DiagUploader core logic, pure JVM: opt-in gate, crash-hash dedupe, daily
 * cap, pending-retry, connectivity gating, bundle pass-through. The bundle
 * arrives already redacted from Diag.report(); the uploader must transport
 * it byte-exact.
 */
public class DiagUploaderTest {

    private MapStore store;
    private FakeTransport transport;
    private FakeNet net;
    private String bundle;
    private DiagUploader up;
    private static final long T0 = 1761000000000L; // fixed day
    private static final long DAY_MS = 24L * 3600 * 1000;

    static final class MapStore implements DiagUploader.Store {
        final Map<String, String> m = new HashMap<>();
        public String getString(String k, String def) { return m.getOrDefault(k, def); }
        public void putString(String k, String v) { m.put(k, v); }
        public boolean getBool(String k, boolean def) {
            return m.containsKey(k) ? Boolean.parseBoolean(m.get(k)) : def;
        }
        public void putBool(String k, boolean v) { m.put(k, Boolean.toString(v)); }
    }

    static final class FakeTransport implements DiagUploader.Transport {
        int calls; int nextCode = 200; String lastUrl; String lastClientId; String lastBody;
        boolean throwNext;
        public int post(String url, String clientId, String bodyJson) {
            calls++;
            if (throwNext) { throwNext = false; throw new RuntimeException("boom"); }
            lastUrl = url; lastClientId = clientId; lastBody = bodyJson;
            return nextCode;
        }
    }

    static final class FakeNet implements DiagUploader.NetCheck {
        boolean online = true;
        public boolean online() { return online; }
    }

    @Before
    public void setUp() {
        store = new MapStore();
        transport = new FakeTransport();
        net = new FakeNet();
        bundle = "sdrstream diag\ndevice: pixel\n";
        up = new DiagUploader(store, transport, () -> bundle, net,
                "https://diag.example.workers.dev/diag", "client-1");
        up.setOptIn(true);
    }

    @Test
    public void optInOff_neverSends() {
        DiagUploader off = new DiagUploader(store, transport, () -> bundle, net,
                "https://diag.example.workers.dev/diag", "client-1");
        // fresh store, opt-in never set: default off
        MapStore fresh = new MapStore();
        off = new DiagUploader(fresh, transport, () -> bundle, net,
                "https://diag.example.workers.dev/diag", "client-1");
        assertEquals(DiagUploader.Result.DISABLED, off.maybeAutoSend("crash A", T0));
        assertEquals(0, transport.calls);
    }

    @Test
    public void emptyEndpoint_noop() {
        DiagUploader noEp = new DiagUploader(store, transport, () -> bundle, net, "", "");
        noEp.setOptIn(true);
        assertEquals(DiagUploader.Result.NO_ENDPOINT, noEp.maybeAutoSend("crash A", T0));
        assertEquals(0, transport.calls);
    }

    @Test
    public void nonHttpsEndpoint_neverSends() {
        for (String bad : new String[]{"http://diag.example/diag", "ftp://x", "diag.example/diag"}) {
            DiagUploader insecure = new DiagUploader(store, transport, () -> bundle, net, bad, "c");
            insecure.setOptIn(true);
            assertEquals(bad, DiagUploader.Result.NO_ENDPOINT, insecure.maybeAutoSend("crash A", T0));
        }
        assertEquals(0, transport.calls);
    }

    @Test
    public void sendsWhenOptedIn_andRecordsHashAndDay() {
        assertEquals(DiagUploader.Result.SENT, up.maybeAutoSend("crash A", T0));
        assertEquals(1, transport.calls);
        assertEquals("https://diag.example.workers.dev/diag", transport.lastUrl);
        assertEquals("client-1", transport.lastClientId);
        assertEquals(DiagUploader.sha256Hex("crash A"),
                store.getString(DiagUploader.KEY_LAST_HASH, ""));
        assertEquals(DiagUploader.dayOf(T0), store.getString(DiagUploader.KEY_LAST_DAY, ""));
    }

    @Test
    public void duplicateHash_notResent_evenNextDay() {
        assertEquals(DiagUploader.Result.SENT, up.maybeAutoSend("crash A", T0));
        assertEquals(DiagUploader.Result.DUPLICATE, up.maybeAutoSend("crash A", T0 + DAY_MS));
        assertEquals(1, transport.calls);
    }

    @Test
    public void dailyCap_secondDistinctCrashSameDay_sendsNextDay() {
        assertEquals(DiagUploader.Result.SENT, up.maybeAutoSend("crash A", T0));
        assertEquals(DiagUploader.Result.DAILY_CAP, up.maybeAutoSend("crash B", T0 + 60_000));
        assertEquals(1, transport.calls);
        // Next day the pending capped crash goes out.
        assertEquals(DiagUploader.Result.SENT, up.maybeAutoSend("crash B", T0 + DAY_MS));
        assertEquals(2, transport.calls);
    }

    @Test
    public void offline_marksPending_sendsWhenBack() {
        net.online = false;
        assertEquals(DiagUploader.Result.OFFLINE, up.maybeAutoSend("crash A", T0));
        assertEquals(0, transport.calls);
        net.online = true;
        assertEquals(DiagUploader.Result.SENT, up.maybeAutoSend("crash A", T0));
        assertEquals(1, transport.calls);
    }

    @Test
    public void failure_marksPending_retrySucceeds() {
        transport.throwNext = true;
        assertEquals(DiagUploader.Result.FAILED, up.maybeAutoSend("crash A", T0));
        assertEquals(DiagUploader.Result.SENT, up.maybeAutoSend("crash A", T0));
        assertEquals(2, transport.calls);
    }

    @Test
    public void serverError_marksPending() {
        transport.nextCode = 500;
        assertEquals(DiagUploader.Result.FAILED, up.maybeAutoSend("crash A", T0));
        transport.nextCode = 200;
        assertEquals(DiagUploader.Result.SENT, up.maybeAutoSend("crash A", T0));
    }

    @Test
    public void bundlePassThroughUnmodified() {
        String tricky = "line with \"quotes\" and user:secret@host.example\nsecond line\\n";
        DiagUploader u = new DiagUploader(store, transport, () -> tricky, net,
                "https://diag.example.workers.dev/diag", "client-1");
        u.setOptIn(true);
        assertEquals(DiagUploader.Result.SENT, u.maybeAutoSend("crash", T0));
        assertTrue("bundle must reach the transport unmodified",
                transport.lastBody.contains(DiagUploader.escapeJson(tricky)));
    }

    @Test
    public void sendNow_bypassesCapAndDedupe() {
        assertEquals(DiagUploader.Result.SENT, up.maybeAutoSend("crash A", T0));
        assertEquals(DiagUploader.Result.SENT, up.sendNow("crash A", T0));
        assertEquals(2, transport.calls);
    }

    @Test
    public void noCrashAndNoPending_nothingToDo() {
        assertEquals(DiagUploader.Result.NOTHING_NEW, up.maybeAutoSend("", T0));
        assertEquals(0, transport.calls);
    }

    @Test
    public void jsonEscapesQuotesAndNewlines() {
        assertEquals("a\\\"b\\nc", DiagUploader.escapeJson("a\"b\nc"));
    }
}
