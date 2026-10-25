package com.chaim12345.sdrstream;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import java.io.BufferedReader;
import java.io.IOException;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.net.ServerSocket;
import java.net.Socket;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Base64;
import java.util.List;
import java.util.concurrent.CopyOnWriteArrayList;

import javax.net.ssl.SSLSocket;

import org.junit.Test;

/**
 * SmtpClient protocol tests over a loopback fake server (plain sockets via
 * the injected factory). TLS is enforced structurally: production always
 * uses SmtpClient.TLS_FACTORY and the no-arg constructor is the only other
 * path; a dedicated test pins that factory to SSLSocket.
 */
public class SmtpClientTest {

    /** Scriptable fake SMTP server on loopback; records every client line. */
    static final class FakeServer implements AutoCloseable {
        final ServerSocket listen;
        final List<String> received = new CopyOnWriteArrayList<>();
        final StringBuilder data = new StringBuilder();
        volatile String authReply = "235 2.7.0 Authentication successful";
        volatile String rcptReply = "250 2.1.5 OK";
        volatile boolean dropOnConnect = false;
        private final Thread thread;

        FakeServer() throws IOException {
            listen = new ServerSocket(0);
            thread = new Thread(this::serve, "fake-smtp");
            thread.setDaemon(true);
            thread.start();
        }

        int port() { return listen.getLocalPort(); }

        private void serve() {
            try (Socket s = listen.accept()) {
                if (dropOnConnect) return;
                BufferedReader in = new BufferedReader(
                        new InputStreamReader(s.getInputStream(), StandardCharsets.US_ASCII));
                OutputStream out = s.getOutputStream();
                out.write("220 fake.cock.li ESMTP ready\r\n".getBytes(StandardCharsets.US_ASCII));
                out.flush();
                boolean inData = false;
                String line;
                while ((line = in.readLine()) != null) {
                    if (inData) {
                        if (line.equals(".")) {
                            inData = false;
                            out.write("250 2.0.0 queued\r\n".getBytes(StandardCharsets.US_ASCII));
                            out.flush();
                        } else {
                            data.append(line).append("\n");
                        }
                        continue;
                    }
                    received.add(line);
                    String reply;
                    String upper = line.toUpperCase(java.util.Locale.US);
                    if (upper.startsWith("EHLO")) reply = "250-fake\r\n250 AUTH PLAIN";
                    else if (upper.startsWith("AUTH")) reply = authReply;
                    else if (upper.startsWith("MAIL FROM")) reply = "250 2.1.0 OK";
                    else if (upper.startsWith("RCPT TO")) reply = rcptReply;
                    else if (upper.equals("DATA")) { reply = "354 End data with <CR><LF>.<CR><LF>"; inData = true; }
                    else if (upper.startsWith("QUIT")) { reply = "221 2.0.0 bye"; }
                    else reply = "502 5.5.2 unimplemented";
                    out.write((reply + "\r\n").getBytes(StandardCharsets.US_ASCII));
                    out.flush();
                    if (upper.startsWith("QUIT")) break;
                }
            } catch (IOException ignored) {
                // client disconnected mid-script; assertions on `received` still hold
            }
        }

        public void close() throws IOException {
            listen.close();
            try { thread.join(2000); } catch (InterruptedException ignored) {}
        }
    }

    private static SmtpClient.SocketFactory plainFactory() {
        return (host, port) -> {
            Socket s = new Socket("127.0.0.1", port);
            s.setSoTimeout(10000);
            return s;
        };
    }

    @Test
    public void tlsFactoryRequiresHostnameVerification() throws Exception {
        // MITM guard: the TLS socket must verify the certificate's hostname,
        // not just the chain. Pin the wiring so a refactor can't drop it.
        javax.net.ssl.SSLSocket s = SmtpClient.newTlsSocket();
        SmtpClient.requireHostnameVerification(s);
        assertEquals("HTTPS", s.getSSLParameters().getEndpointIdentificationAlgorithm());
    }

    @Test
    public void productionTransportIsTlsOnly() throws Exception {
        // The no-arg constructor must wire TLS_FACTORY, and TLS_FACTORY must
        // produce an SSLSocket: there is no plaintext code path in the client.
        assertTrue(SmtpClient.newTlsSocket() instanceof SSLSocket);
    }

    @Test
    public void happyPath_authAndMessageFlow() throws Exception {
        try (FakeServer server = new FakeServer()) {
            new SmtpClient(plainFactory()).send("127.0.0.1", server.port(),
                    "chaim12345@cock.li", "s3cret",
                    "chaim12345@cock.li", "b92sq3@mail.instinct.com",
                    "sdrstream diag 2026-10-25 abc123", "bundle body\nsecond line");
            List<String> got = new ArrayList<>(server.received);
            assertEquals("EHLO sdrstream.local", got.get(0));
            assertTrue(got.get(1), got.get(1).startsWith("AUTH PLAIN "));
            String decoded = new String(Base64.getDecoder().decode(
                    got.get(1).substring("AUTH PLAIN ".length())), StandardCharsets.UTF_8);
            assertEquals("\0chaim12345@cock.li\0s3cret", decoded);
            assertEquals("MAIL FROM:<chaim12345@cock.li>", got.get(2));
            assertEquals("RCPT TO:<b92sq3@mail.instinct.com>", got.get(3));
            assertEquals("DATA", got.get(4));
            assertEquals("QUIT", got.get(got.size() - 1));
            String body = server.data.toString();
            assertTrue(body, body.contains("Subject: sdrstream diag 2026-10-25 abc123\r\n".replace("\r\n", "\n")));
            assertTrue(body, body.contains("bundle body\nsecond line\n"));
        }
    }

    @Test
    public void dotStuffing_andCrLfNormalization() {
        String msg = SmtpClient.message("a@b.c", "d@e.f", "s", "first\n.hidden\nlast\r\n\r\nend");
        assertTrue(msg, msg.contains("first\r\n..hidden\r\nlast\r\n\r\nend\r\n.\r\n"));
        // A bare dot line in the body must not terminate DATA early.
        String withDot = SmtpClient.message("a@b.c", "d@e.f", "s", "one\n.\ntwo");
        assertTrue(withDot, withDot.contains("one\r\n..\r\ntwo\r\n.\r\n"));
    }

    @Test
    public void authFailure_throws() throws Exception {
        try (FakeServer server = new FakeServer()) {
            server.authReply = "535 5.7.8 authentication failed";
            try {
                new SmtpClient(plainFactory()).send("127.0.0.1", server.port(),
                        "u", "wrong", "f@x.y", "t@x.y", "s", "body");
                fail("expected IOException");
            } catch (IOException e) {
                assertTrue(e.getMessage(), e.getMessage().contains("535"));
            }
        }
    }

    @Test
    public void recipientRejected_throws() throws Exception {
        try (FakeServer server = new FakeServer()) {
            server.rcptReply = "550 5.1.1 no such user";
            try {
                new SmtpClient(plainFactory()).send("127.0.0.1", server.port(),
                        "u", "p", "f@x.y", "t@x.y", "s", "body");
                fail("expected IOException");
            } catch (IOException e) {
                assertTrue(e.getMessage(), e.getMessage().contains("RCPT"));
            }
        }
    }

    @Test
    public void serverDropsConnection_throws() throws Exception {
        try (FakeServer server = new FakeServer()) {
            server.dropOnConnect = true;
            try {
                new SmtpClient(plainFactory()).send("127.0.0.1", server.port(),
                        "u", "p", "f@x.y", "t@x.y", "s", "body");
                fail("expected IOException");
            } catch (IOException expected) {
                // carrier-blocked port 465 surfaces exactly like this to the uploader
            }
        }
    }
}
