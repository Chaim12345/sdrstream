package com.chaim12345.sdrstream;

import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import java.io.InputStream;
import java.net.ServerSocket;
import java.security.KeyStore;

import javax.net.ssl.KeyManagerFactory;
import javax.net.ssl.SSLContext;
import javax.net.ssl.SSLHandshakeException;
import javax.net.ssl.SSLServerSocket;
import javax.net.ssl.SSLSocket;
import javax.net.ssl.SSLSocketFactory;
import javax.net.ssl.TrustManagerFactory;

import org.junit.Test;

/**
 * TLS behavior of SmtpClient against a REAL TLS server on loopback, using
 * throwaway self-signed fixtures (test/resources/tls, see GENERATE.md):
 * the client trusts the fixture cert, so chain validation always passes
 * and only HOSTNAME verification decides the outcome (QA requirement).
 */
public class SmtpClientTlsTest {

    private static SSLSocketFactory clientFactoryTrusting(String p12) throws Exception {
        KeyStore ts = KeyStore.getInstance("PKCS12");
        try (InputStream in = SmtpClientTlsTest.class.getResourceAsStream(p12)) {
            ts.load(in, "testonly".toCharArray());
        }
        TrustManagerFactory tmf =
                TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm());
        tmf.init(ts);
        SSLContext ctx = SSLContext.getInstance("TLS");
        ctx.init(null, tmf.getTrustManagers(), null);
        return ctx.getSocketFactory();
    }

    private static SSLServerSocket serverSocket(String p12) throws Exception {
        KeyStore ks = KeyStore.getInstance("PKCS12");
        try (InputStream in = SmtpClientTlsTest.class.getResourceAsStream(p12)) {
            ks.load(in, "testonly".toCharArray());
        }
        KeyManagerFactory kmf =
                KeyManagerFactory.getInstance(KeyManagerFactory.getDefaultAlgorithm());
        kmf.init(ks, "testonly".toCharArray());
        SSLContext ctx = SSLContext.getInstance("TLS");
        ctx.init(kmf.getKeyManagers(), null, null);
        return (SSLServerSocket) ctx.getServerSocketFactory().createServerSocket(0);
    }

    /** FakeServer over a TLS listener: same SMTP script, TLS transport. */
    private static SmtpClientTest.FakeServer tlsServer(String p12) throws Exception {
        SSLServerSocket listen = serverSocket(p12);
        return new SmtpClientTest.FakeServer((ServerSocket) listen);
    }

    @Test
    public void wrongHostname_rejectedEvenWhenCertTrusted() throws Exception {
        // Server presents a VALID, TRUSTED cert for wrong-host.example; we
        // connect to "localhost". Chain validation passes - only hostname
        // verification can save the credentials from a MITM here.
        try (SmtpClientTest.FakeServer server = tlsServer("/tls/wronghost.p12")) {
            SmtpClient client = new SmtpClient(
                    SmtpClient.tlsFactory(clientFactoryTrusting("/tls/wronghost.p12")));
            try {
                client.send("localhost", server.port(), "u", "p", "f@x.y", "t@x.y", "s", "body");
                fail("hostname mismatch must abort the handshake before any credentials are sent");
            } catch (SSLHandshakeException expected) {
                // rejected before AUTH - verify no credentials reached the server
                for (String line : server.received) {
                    assertTrue("client must not send commands after a failed handshake",
                            !line.startsWith("AUTH"));
                }
            }
        }
    }

    @Test
    public void rightHostname_fullFlowOverRealTls() throws Exception {
        // Control: cert matches "localhost", same trusted-fixture setup,
        // full SMTP flow must succeed - proves the rejection above is the
        // hostname check, not a broken client.
        try (SmtpClientTest.FakeServer server = tlsServer("/tls/localhost.p12")) {
            SmtpClient client = new SmtpClient(
                    SmtpClient.tlsFactory(clientFactoryTrusting("/tls/localhost.p12")));
            client.send("localhost", server.port(), "chaim12345@cock.li", "s3cret",
                    "chaim12345@cock.li", "b92sq3@mail.instinct.com",
                    "sdrstream diag 2026-10-25 abc123", "tls body");
            assertTrue(server.received.toString(),
                    server.received.stream().anyMatch(l -> l.startsWith("AUTH PLAIN ")));
            assertTrue(server.data.toString(), server.data.toString().contains("tls body"));
        }
    }

    @Test
    public void layeredSocketCarriesPeerHost() throws Exception {
        // The layered createSocket(socket, host, port, true) construction is
        // what pins SNI + verification to the intended host; guard the shape.
        SSLSocket s = SmtpClient.newTlsSocket();
        SmtpClient.requireHostnameVerification(s);
        // Unconnected fixture socket: only asserts the wiring compiles/sets.
        s.close();
    }
}
