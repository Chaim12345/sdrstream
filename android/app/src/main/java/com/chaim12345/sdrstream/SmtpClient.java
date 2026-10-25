package com.chaim12345.sdrstream;

import java.io.BufferedReader;
import java.io.IOException;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.net.Socket;
import java.nio.charset.StandardCharsets;
import java.text.SimpleDateFormat;
import java.util.Base64;
import java.util.Date;
import java.util.Locale;

import javax.net.ssl.SSLSocket;
import javax.net.ssl.SSLSocketFactory;

/**
 * Minimal SMTPS (implicit TLS) client: EHLO, AUTH PLAIN, one recipient,
 * plain-text body. No STARTTLS, no attachments, no pipelining - the message
 * shape is fixed (a redacted diagnostics bundle, one inbox). Pure Java, no
 * third-party deps; the socket factory is injected so the protocol runs
 * under plain-JVM tests against a loopback fake server, while production
 * always takes TLS_FACTORY (SSLSocket + startHandshake). There is no
 * plaintext path.
 *
 * Failure semantics: any unexpected reply code or I/O error throws
 * IOException; the caller (DiagUploader) treats that as a pending-retry
 * send failure. Some mobile carriers block outbound port 465 - that is a
 * connect timeout here, i.e. an ordinary failure, never a crash.
 */
final class SmtpClient {

    interface SocketFactory { Socket open(String host, int port) throws IOException; }

    private static final int CONNECT_TIMEOUT_MS = 15000;
    private static final int READ_TIMEOUT_MS = 15000;

    /** Production factory: implicit TLS, handshake verified before use. */
    static final SocketFactory TLS_FACTORY =
            tlsFactory((SSLSocketFactory) SSLSocketFactory.getDefault());

    /**
     * TLS over an already-connected plain socket. The layered
     * createSocket(socket, host, port, autoClose) form fixes the peer host
     * for both SNI and hostname verification (QA review) - the unconnected
     * createSocket() + connect() form can leave verification without the
     * intended peer name.
     */
    static SocketFactory tlsFactory(SSLSocketFactory ssl) {
        return (host, port) -> {
            Socket plain = new Socket();
            try {
                plain.connect(new InetSocketAddress(host, port), CONNECT_TIMEOUT_MS);
                SSLSocket s = (SSLSocket) ssl.createSocket(plain, host, port, true);
                s.setSoTimeout(READ_TIMEOUT_MS);
                requireHostnameVerification(s);
                s.startHandshake();
                return s;
            } catch (IOException | RuntimeException e) {
                try { plain.close(); } catch (IOException ignored) {}
                throw e;
            }
        };
    }

    /**
     * Raw SSLSocket validates the certificate CHAIN but not the HOSTNAME:
     * without this, any valid CA-issued cert (for any domain) could MITM
     * the relay login. "HTTPS" enables RFC 2818 hostname verification.
     */
    static void requireHostnameVerification(SSLSocket s) {
        javax.net.ssl.SSLParameters p = s.getSSLParameters();
        p.setEndpointIdentificationAlgorithm("HTTPS");
        s.setSSLParameters(p);
    }

    /** Unconnected SSLSocket; exists so tests can assert the TLS socket type offline. */
    static SSLSocket newTlsSocket() throws IOException {
        return (SSLSocket) SSLSocketFactory.getDefault().createSocket();
    }

    private final SocketFactory factory;

    SmtpClient() { this(TLS_FACTORY); }

    SmtpClient(SocketFactory factory) { this.factory = factory; }

    void send(String host, int port, String user, String password,
              String from, String to, String subject, String text) throws IOException {
        try (Socket sock = factory.open(host, port)) {
            BufferedReader in = new BufferedReader(
                    new InputStreamReader(sock.getInputStream(), StandardCharsets.US_ASCII));
            OutputStream out = sock.getOutputStream();

            expect(in, 220);                                   // greeting
            command(in, out, 250, "EHLO sdrstream.local");
            String token = Base64.getEncoder().encodeToString(
                    ("\0" + user + "\0" + password).getBytes(StandardCharsets.UTF_8));
            command(in, out, 235, "AUTH PLAIN " + token);
            command(in, out, 250, "MAIL FROM:<" + from + ">");
            int rcpt = command(in, out, -1, "RCPT TO:<" + to + ">");
            if (rcpt != 250 && rcpt != 251) {
                throw new IOException("RCPT rejected: " + rcpt);
            }
            command(in, out, 354, "DATA");
            out.write(message(from, to, subject, text).getBytes(StandardCharsets.UTF_8));
            out.flush();
            expect(in, 250);                                   // message accepted
            command(in, out, -1, "QUIT");                      // any reply is fine
        }
    }

    /** RFC 5322 message with dot-stuffed, CRLF-normalized body. Subject must be ASCII. */
    static String message(String from, String to, String subject, String text) {
        StringBuilder sb = new StringBuilder(text.length() + 256);
        sb.append("From: <").append(from).append(">\r\n");
        sb.append("To: <").append(to).append(">\r\n");
        sb.append("Subject: ").append(subject).append("\r\n");
        SimpleDateFormat f = new SimpleDateFormat("EEE, d MMM yyyy HH:mm:ss Z", Locale.US);
        sb.append("Date: ").append(f.format(new Date())).append("\r\n");
        sb.append("MIME-Version: 1.0\r\n");
        sb.append("Content-Type: text/plain; charset=utf-8\r\n");
        sb.append("\r\n");
        String normalized = text.replace("\r\n", "\n").replace('\r', '\n');
        for (String line : normalized.split("\n", -1)) {
            if (line.startsWith(".")) sb.append('.');          // dot-stuffing
            sb.append(line).append("\r\n");
        }
        sb.append(".\r\n");
        return sb.toString();
    }

    /** Sends one line and returns the reply code; throws unless it matches want (-1 = any). */
    private static int command(BufferedReader in, OutputStream out, int want, String line)
            throws IOException {
        out.write((line + "\r\n").getBytes(StandardCharsets.US_ASCII));
        out.flush();
        return expect(in, want);
    }

    /** Reads a (possibly multiline) reply; throws unless the code matches want (-1 = any). */
    private static int expect(BufferedReader in, int want) throws IOException {
        int code = -1;
        String last = "";
        for (;;) {
            String line = in.readLine();
            if (line == null) throw new IOException("SMTP connection closed by server");
            last = line;
            if (line.length() < 3) throw new IOException("malformed SMTP reply: " + line);
            try {
                code = Integer.parseInt(line.substring(0, 3));
            } catch (NumberFormatException e) {
                throw new IOException("malformed SMTP reply: " + line);
            }
            if (line.length() < 4 || line.charAt(3) != '-') break;  // single line or last of multiline
        }
        if (want >= 0 && code != want) {
            // The server reply may echo a command but never carries the
            // AUTH payload itself; include it for diagnosability.
            throw new IOException("SMTP expected " + want + " got " + code + ": " + last);
        }
        return code;
    }
}
