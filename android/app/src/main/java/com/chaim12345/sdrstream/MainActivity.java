package com.chaim12345.sdrstream;

import android.app.Activity;
import android.app.AlertDialog;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.PowerManager;
import android.provider.Settings;
import android.text.Html;
import android.view.Menu;
import android.view.MenuItem;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.EditText;
import android.widget.Toast;

import java.io.File;
import java.io.IOException;
import java.net.InetSocketAddress;
import java.net.Socket;
import java.util.ArrayList;

/**
 * Full-stack sdrstream on the phone: starts the Go engine in a foreground
 * service and shows its web UI in a WebView pointed at loopback.
 *
 * sdrstream is free software under the GNU AGPL-3.0; the corresponding
 * source is https://github.com/Chaim12345/sdrstream (linked from the menu,
 * which satisfies the in-app source offer).
 *
 * Failure handling: nothing dies silently. The first directory fetch can
 * keep the engine from binding its port for up to about a minute, so the UI
 * waits two minutes before giving up, and every failure path ends in a
 * diagnostics page with logs that can be shared from the menu.
 */
public class MainActivity extends Activity {
    private static final String UI_URL = "http://127.0.0.1:" + EngineService.PORT + "/";
    private static final int MAX_WAIT_ATTEMPTS = 120;
    private static final int REQ_NOTIFICATIONS = 42;
    private WebView webView;
    private AlertDialog batteryDialog;
    private Thread waitThread;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        try {
            startEngineService();
        } catch (Throwable t) {
            Diag.writeCrash(this, "start engine service", t);
            Diag.recordEngineError(this, "could not start the engine service: " + t);
        }
        maybeAskNotificationPermission();
        maybeAskBatteryExemption();

        webView = new WebView(this);
        webView.getSettings().setJavaScriptEnabled(true);
        webView.getSettings().setDomStorageEnabled(true);
        webView.getSettings().setMediaPlaybackRequiresUserGesture(false);
        webView.setWebViewClient(new WebViewClient());
        setContentView(webView);
        showCrashReportIfAny();
        loadWhenUp();
    }

    private void startEngineService() {
        startForegroundService(new Intent(this, EngineService.class)
                .setAction(EngineService.ACTION_START));
    }

    private void maybeAskNotificationPermission() {
        if (Build.VERSION.SDK_INT < 33) return;
        try {
            if (checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS)
                    != PackageManager.PERMISSION_GRANTED) {
                requestPermissions(
                        new String[]{android.Manifest.permission.POST_NOTIFICATIONS},
                        REQ_NOTIFICATIONS);
            }
        } catch (Throwable t) {
            Diag.writeCrash(this, "request notification permission", t);
        }
    }

    /** If a previous run crashed, offer to share the log before continuing. */
    private void showCrashReportIfAny() {
        try {
            if (!Diag.hasCrashLog(this)) return;
            File crash = new File(getFilesDir(), Diag.CRASH_LOG);
            long shown = getSharedPreferences("sdrstream_diag", MODE_PRIVATE)
                    .getLong("crash_shown_mtime", -1);
            if (crash.lastModified() == shown) return;
            new AlertDialog.Builder(this)
                    .setTitle("sdrstream crashed last time")
                    .setMessage("A crash log was saved. Sharing it with the report helps fix the problem.")
                    .setPositiveButton("Share log", (d, w) -> shareLogs())
                    .setNegativeButton("Dismiss", (d, w) ->
                            getSharedPreferences("sdrstream_diag", MODE_PRIVATE).edit()
                                    .putLong("crash_shown_mtime", crash.lastModified()).apply())
                    .show();
        } catch (Throwable t) {
            Diag.writeCrash(this, "show crash report", t);
        }
    }

    /**
     * Poll /healthz until the engine binds its port. The socket probe must
     * run on a background thread: doing it on the main thread throws
     * NetworkOnMainThreadException and kills the app at launch (the 0.1.0
     * crash). Only the WebView calls hop back to the main thread.
     */
    private void loadWhenUp() {
        waitThread = new Thread(() -> {
            for (int attempt = 0; attempt < MAX_WAIT_ATTEMPTS; attempt++) {
                if (isFinishing()) return;
                try {
                    Thread.sleep(attempt == 0 ? 300 : 1000);
                } catch (InterruptedException e) {
                    return;
                }
                if (portOpen()) {
                    webView.post(() -> {
                        if (!isFinishing()) webView.loadUrl(UI_URL);
                    });
                    return;
                }
            }
            webView.post(() -> {
                if (!isFinishing()) {
                    showDiagnostics("The sdrstream engine did not start within two minutes.");
                }
            });
        }, "engine-wait");
        waitThread.start();
    }

    private boolean portOpen() {
        try (Socket s = new Socket()) {
            s.connect(new InetSocketAddress("127.0.0.1", EngineService.PORT), 400);
            return true;
        } catch (IOException e) {
            return false;
        }
    }

    /** Renders the diagnostics report inside the WebView. */
    private void showDiagnostics(String headline) {
        String report = Diag.report(this);
        String escaped = Html.escapeHtml(report);
        String html = "<html><body style='background:#070a12;color:#eee;font-family:sans-serif;padding:2em'>"
                + "<h3>" + Html.escapeHtml(headline) + "</h3>"
                + "<p>Use the menu &rarr; <b>Share logs</b> to send this report.</p>"
                + "<pre style='white-space:pre-wrap;color:#9fd'>" + escaped + "</pre>"
                + "</body></html>";
        webView.loadDataWithBaseURL(null, html, "text/html", "utf-8", null);
    }

    /** Shares diag.txt plus whatever logs exist through the system sheet. */
    private void shareLogs() {
        try {
            Diag.writeReportFile(this);
            ArrayList<Uri> uris = new ArrayList<>();
            uris.add(LogProvider.uriFor(Diag.DIAG_TXT));
            if (new File(getFilesDir(), Diag.ENGINE_LOG).exists()) {
                uris.add(LogProvider.uriFor(Diag.ENGINE_LOG));
            }
            if (new File(getFilesDir(), Diag.CRASH_LOG).exists()) {
                uris.add(LogProvider.uriFor(Diag.CRASH_LOG));
            }
            Intent send = new Intent(Intent.ACTION_SEND_MULTIPLE);
            send.setType("text/plain");
            send.putExtra(Intent.EXTRA_SUBJECT, "sdrstream diagnostics");
            send.putParcelableArrayListExtra(Intent.EXTRA_STREAM, uris);
            send.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION);
            startActivity(Intent.createChooser(send, "Share sdrstream logs"));
        } catch (Throwable t) {
            Diag.writeCrash(this, "share logs", t);
            Toast.makeText(this, "Could not share logs: " + t, Toast.LENGTH_LONG).show();
        }
    }

    private void maybeAskBatteryExemption() {
        try {
            PowerManager pm = (PowerManager) getSystemService(POWER_SERVICE);
            if (pm != null && !pm.isIgnoringBatteryOptimizations(getPackageName())) {
                batteryDialog = new AlertDialog.Builder(this)
                        .setTitle("Keep monitoring in the background")
                        .setMessage("Android battery saving can suspend the monitor. " +
                                "Exempt sdrstream so scanning continues with the screen off.")
                        .setPositiveButton("Exempt", (d, w) -> startActivity(new Intent(
                                Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
                                Uri.parse("package:" + getPackageName()))))
                        .setNegativeButton("Not now", null)
                        .show();
            }
        } catch (Throwable t) {
            Diag.writeCrash(this, "battery exemption dialog", t);
        }
    }

    @Override
    public boolean onCreateOptionsMenu(Menu menu) {
        menu.add(0, 1, 0, "Show UI password");
        menu.add(0, 2, 0, "Set UI password");
        menu.add(0, 3, 0, "Source code (AGPL-3.0)");
        menu.add(0, 4, 0, "Stop engine");
        menu.add(0, 5, 0, "Diagnostics");
        menu.add(0, 6, 0, "Share logs");
        return true;
    }

    @Override
    public boolean onOptionsItemSelected(MenuItem item) {
        switch (item.getItemId()) {
            case 1:
                new AlertDialog.Builder(this)
                        .setTitle("Web UI password")
                        .setMessage("Log in to the UI with:\n\n" + Prefs.getUiPassword(this)
                                + "\n\nIt is stored only on this device.")
                        .setPositiveButton("OK", null)
                        .show();
                return true;
            case 2: {
                final EditText input = new EditText(this);
                input.setHint("new password");
                new AlertDialog.Builder(this)
                        .setTitle("Set UI password")
                        .setView(input)
                        .setPositiveButton("Save", (d, w) -> {
                            String pw = input.getText().toString();
                            if (!pw.isEmpty()) {
                                Prefs.setUiPassword(this, pw);
                                Toast.makeText(this,
                                        "Saved. Restart the app to apply.", Toast.LENGTH_LONG).show();
                            }
                        })
                        .setNegativeButton("Cancel", null)
                        .show();
                return true;
            }
            case 3:
                startActivity(new Intent(Intent.ACTION_VIEW,
                        Uri.parse("https://github.com/Chaim12345/sdrstream")));
                return true;
            case 4:
                startService(new Intent(this, EngineService.class)
                        .setAction(EngineService.ACTION_STOP));
                finish();
                return true;
            case 5:
                showDiagnostics("Diagnostics");
                return true;
            case 6:
                shareLogs();
                return true;
            default:
                return super.onOptionsItemSelected(item);
        }
    }

    @Override
    public void onBackPressed() {
        if (webView != null && webView.canGoBack()) webView.goBack();
        else moveTaskToBack(true); // keep the engine running
    }

    @Override
    protected void onDestroy() {
        if (batteryDialog != null && batteryDialog.isShowing()) batteryDialog.dismiss();
        if (waitThread != null) waitThread.interrupt();
        if (webView != null) webView.destroy();
        super.onDestroy();
    }
}
