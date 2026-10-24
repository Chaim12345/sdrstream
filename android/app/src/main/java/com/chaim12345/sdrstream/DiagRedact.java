package com.chaim12345.sdrstream;

import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * Java port of the engine's redactLogLine (go/internal/web/web.go): strips
 * credentials, cookies, URL queries/userinfo and keyed coordinates from a
 * log line before it lands in a shareable file. javalog.txt never passes
 * through the Go redactor, so it is applied here on capture. Lossy by
 * design: diagnostics need hosts and paths, never secrets or positions.
 */
final class DiagRedact {
    private static final Pattern URL = Pattern.compile("https?://[^\\s\"']+");
    private static final Pattern CRED = Pattern.compile(
            "(?i)\\b(password|passwd|token|secret|api[-_]?key|session[-_]?id|cookie|authorization)"
                    + "(\\s*[:=]\\s*)(\"?)[^\\s,\"']*(\"?)");
    private static final Pattern COORD = Pattern.compile(
            "(?i)\\b(lat|lon|latitude|longitude)(\\s*[:=]\\s*)-?\\d+(\\.\\d+)?");

    private DiagRedact() {}

    static String redact(String line) {
        Matcher m = URL.matcher(line);
        StringBuffer sb = new StringBuffer();
        while (m.find()) {
            String u = m.group();
            int scheme = u.indexOf("://");
            if (scheme >= 0) {
                String rest = u.substring(scheme + 3);
                int at = rest.indexOf('@');
                if (at >= 0 && !rest.substring(0, at).contains("/")) {
                    u = u.substring(0, scheme + 3) + rest.substring(at + 1);
                }
            }
            int q = u.indexOf('?');
            if (q >= 0) {
                u = u.substring(0, q) + "?[redacted]";
            }
            m.appendReplacement(sb, Matcher.quoteReplacement(u));
        }
        m.appendTail(sb);
        String out = CRED.matcher(sb.toString()).replaceAll("$1$2[redacted]");
        out = COORD.matcher(out).replaceAll("$1$2[redacted]");
        return out;
    }
}
