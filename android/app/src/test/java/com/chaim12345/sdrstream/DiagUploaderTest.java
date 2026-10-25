package com.chaim12345.sdrstream;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertTrue;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

import org.junit.Before;
import org.junit.Test;

/**
 * DiagUploader core logic, pure JVM: opt-in gate, crash-hash dedupe, daily
 * cap, pending-retry, connectivity gating, bundle pass-through. The bundle
 * arrives already redacted from Diag.report(); the uploader must transport
 * it byte-exact. Transport is direct SMTPS to the diagnostics inbox.
 */
public class DiagUploaderTest {

    private MapStore store;
    private FakeTransport transport;
    private FakeNet net;
    private String bundle;
    private DiagUploader up;
    private DiagUploader.UploadTarget target;
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

    static final class SentMail {
        DiagUploader.UploadTarget target; String subject; String text;
    }

    static final class FakeTransport implements DiagUploader.Transport {
        final List<SentMail> sent = new ArrayList<>();
        boolean throwNext;
        public void send(DiagUploader.UploadTarget target, String subject, String text) {
            if (throwNext) { throwNext = false; throw new RuntimeException("boom"); }
            SentMail m = new SentMail();
            m.target = target; m.subject = subject; m.text = text;
            sent.add(m);
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
        target = new DiagUploader.UploadTarget(
                "smtps", "mail.cock.li", 465, "chaim12345@cock.li", "pw",
                "chaim12345@cock.li", "b92sq3@mail.instinct.com");
        up = new DiagUploader(store, transport, () -> bundle, net, target);
        up.setOptIn(true);
    }

    @Test
    public void optInOff_neverSends() {
        MapStore fresh = new MapStore();
        DiagUploader off = new DiagUploader(fresh, transport, () -> bundle, net, target);
        assertEquals(DiagUploader.Result.DISABLED, off.maybeAutoSend("crash A", T0));
        assertEquals(0, transport.sent.size());
    }

    @Test
    public void unconfiguredTarget_noop() {
        // No password at build time = transport disabled, never a send.
        DiagUploader.UploadTarget noPw = new DiagUploader.UploadTarget(
                "smtps", "mail.cock.li", 465, "u", "", "f", "t");
        DiagUploader noCfg = new DiagUploader(store, transport, () -> bundle, net, noPw);
        noCfg.setOptIn(true);
        assertEquals(DiagUploader.Result.NO_ENDPOINT, noCfg.maybeAutoSend("crash A", T0));
        assertEquals(0, transport.sent.size());
    }

    @Test
    public void nonTlsScheme_neverSends() {
        // The old https-only guard's SMTP form: anything but implicit-TLS
        // smtps is treated as unconfigured - diagnostics never go plaintext.
        for (String bad : new String[]{"smtp", "http", "https", ""}) {
            DiagUploader.UploadTarget plain = new DiagUploader.UploadTarget(
                    bad, "mail.cock.li", 25, "u", "pw", "f", "t");
            DiagUploader insecure = new DiagUploader(store, transport, () -> bundle, net, plain);
            insecure.setOptIn(true);
            assertEquals(bad, DiagUploader.Result.NO_ENDPOINT, insecure.maybeAutoSend("crash A", T0));
        }
        assertEquals(0, transport.sent.size());
    }

    @Test
    public void sendsWhenOptedIn_andRecordsHashAndDay() {
        assertEquals(DiagUploader.Result.SENT, up.maybeAutoSend("crash A", T0));
        assertEquals(1, transport.sent.size());
        assertEquals(target.host, transport.sent.get(0).target.host);
        assertEquals("b92sq3@mail.instinct.com", transport.sent.get(0).target.to);
        assertEquals(DiagUploader.sha256Hex("crash A"),
                store.getString(DiagUploader.KEY_LAST_HASH, ""));
        assertEquals(DiagUploader.dayOf(T0), store.getString(DiagUploader.KEY_LAST_DAY, ""));
    }

    @Test
    public void duplicateHash_notResent_evenNextDay() {
        assertEquals(DiagUploader.Result.SENT, up.maybeAutoSend("crash A", T0));
        assertEquals(DiagUploader.Result.DUPLICATE, up.maybeAutoSend("crash A", T0 + DAY_MS));
        assertEquals(1, transport.sent.size());
    }

    @Test
    public void dailyCap_secondDistinctCrashSameDay_sendsNextDay() {
        assertEquals(DiagUploader.Result.SENT, up.maybeAutoSend("crash A", T0));
        assertEquals(DiagUploader.Result.DAILY_CAP, up.maybeAutoSend("crash B", T0 + 60_000));
        assertEquals(1, transport.sent.size());
        assertEquals(DiagUploader.Result.SENT, up.maybeAutoSend("crash B", T0 + DAY_MS));
        assertEquals(2, transport.sent.size());
    }

    @Test
    public void offline_marksPending_sendsWhenBack() {
        net.online = false;
        assertEquals(DiagUploader.Result.OFFLINE, up.maybeAutoSend("crash A", T0));
        assertEquals(0, transport.sent.size());
        net.online = true;
        assertEquals(DiagUploader.Result.SENT, up.maybeAutoSend("crash A", T0));
        assertEquals(1, transport.sent.size());
    }

    @Test
    public void failure_marksPending_retrySucceeds() {
        transport.throwNext = true;
        assertEquals(DiagUploader.Result.FAILED, up.maybeAutoSend("crash A", T0));
        assertEquals(DiagUploader.Result.SENT, up.maybeAutoSend("crash A", T0));
    }

    @Test
    public void bundlePassThroughUnmodified() {
        String tricky = "line with \"quotes\" and user:secret@host.example\nsecond line\n";
        DiagUploader u = new DiagUploader(store, transport, () -> tricky, net, target);
        u.setOptIn(true);
        assertEquals(DiagUploader.Result.SENT, u.maybeAutoSend("crash", T0));
        assertEquals("bundle must reach the transport byte-exact", tricky, transport.sent.get(0).text);
    }

    @Test
    public void sendNow_bypassesCapAndDedupe() {
        assertEquals(DiagUploader.Result.SENT, up.maybeAutoSend("crash A", T0));
        assertEquals(DiagUploader.Result.SENT, up.sendNow("crash A", T0));
        assertEquals(2, transport.sent.size());
    }

    @Test
    public void noCrashAndNoPending_nothingToDo() {
        assertEquals(DiagUploader.Result.NOTHING_NEW, up.maybeAutoSend("", T0));
        assertEquals(0, transport.sent.size());
    }

    @Test
    public void subjectIsAsciiAndCarriesDayAndHash() {
        assertEquals(DiagUploader.Result.SENT, up.maybeAutoSend("crash A", T0));
        String subj = transport.sent.get(0).subject;
        assertTrue(subj, subj.startsWith("sdrstream diag " + DiagUploader.dayOf(T0)));
        assertTrue(subj, subj.chars().allMatch(c -> c < 128));
    }
}
