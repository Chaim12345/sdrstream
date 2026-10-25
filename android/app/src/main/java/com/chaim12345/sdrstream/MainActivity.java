package com.chaim12345.sdrstream;

import android.app.Activity;
import android.app.AlertDialog;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.graphics.Color;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.PowerManager;
import android.provider.Settings;
import android.text.Html;
import android.util.TypedValue;
import android.view.Gravity;
import android.view.Menu;
import android.view.MenuItem;
import android.view.View;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.PopupMenu;
import android.widget.TextView;
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
 * Menu: the app theme is NoActionBar and phones no longer have a menu key,
 * so a legacy options menu alone is unreachable (the 0.1.1 bug: "Show UI
 * password" existed in code but could not be opened). The top bar's
 * overflow button opens the same actions through a PopupMenu, which works
 * without an action bar on every supported Android version.
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

    private static final int MENU_SHOW_PASSWORD = 1;
    private static final int MENU_TAILNET = 8;
    private static final int MENU_SET_PASSWORD = 2;
    private static final int MENU_SOURCE = 3;
    private static final int MENU_STOP_ENGINE = 4;
    private static final int MENU_DIAGNOSTICS = 5;
    private static final int MENU_SHARE_LOGS = 6;
    private static final int MENU_BACKGROUND = 7;
    private static final int MENU_AUTOSEND = 9;
    private static final int MENU_SEND_DIAG = 10;

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
        webView.setWebViewClient(new WebViewClient() {
            @Override
            public boolean shouldOverrideUrlLoading(WebView view, String url) {
                // One-tap "Send diagnostics" rendered on the error screen.
                if (url != null && url.startsWith("sdrstream-diag://share")) {
                    shareLogs();
                    return true;
                }
                return false;
            }
        });

        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.addView(buildTopBar(), new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, dp(48)));
        root.addView(webView, new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, 0, 1f));
        setContentView(root);

        showCrashReportIfAny();
        // Opted-in users only: retry any pending/one-per-day diagnostics
        // upload. Hard no-op while the relay endpoint is unconfigured.
        DiagUploadAndroid.maybeAutoSendAsync(this);
        loadWhenUp();
    }

    /** Title bar with an always-visible overflow button that opens the menu. */
    private View buildTopBar() {
        LinearLayout bar = new LinearLayout(this);
        bar.setOrientation(LinearLayout.HORIZONTAL);
        bar.setGravity(Gravity.CENTER_VERTICAL);
        bar.setBackgroundColor(Color.parseColor("#10141F"));
        bar.setPadding(dp(12), 0, dp(4), 0);

        TextView title = new TextView(this);
        title.setText(R.string.app_name);
        title.setTextColor(Color.WHITE);
        title.setTextSize(TypedValue.COMPLEX_UNIT_SP, 16);
        bar.addView(title, new LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f));

        Button overflow = new Button(this);
        overflow.setText("⋮");
        overflow.setTextSize(TypedValue.COMPLEX_UNIT_SP, 20);
        overflow.setContentDescription("Menu");
        overflow.setMinWidth(0);
        overflow.setMinimumWidth(0);
        overflow.setOnClickListener(v -> showMenuPopup(v));
        bar.addView(overflow, new LinearLayout.LayoutParams(dp(48), dp(40)));
        return bar;
    }

    private int dp(int value) {
        return (int) TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP,
                value, getResources().getDisplayMetrics());
    }

    /** The reachable menu: a PopupMenu anchored to the overflow button. */
    private void showMenuPopup(View anchor) {
        PopupMenu popup = new PopupMenu(this, anchor);
        popup.getMenu().add(0, MENU_SHOW_PASSWORD, 0, "Show UI password");
        popup.getMenu().add(0, MENU_SET_PASSWORD, 1, "Set UI password");
        popup.getMenu().add(0, MENU_SOURCE, 2, "Source code (AGPL-3.0)");
        popup.getMenu().add(0, MENU_STOP_ENGINE, 3, "Stop engine");
        popup.getMenu().add(0, MENU_DIAGNOSTICS, 4, "Diagnostics");
        popup.getMenu().add(0, MENU_SHARE_LOGS, 5, "Share logs");
        popup.getMenu().add(0, MENU_TAILNET, 6, "Tailscale remote access");
        popup.getMenu().add(0, MENU_BACKGROUND, 6, "Allow background running");
        popup.getMenu().add(0, MENU_AUTOSEND, 7, "Auto-send crash reports")
                .setCheckable(true)
                .setChecked(DiagUploadAndroid.uploader(this).optedIn());
        popup.getMenu().add(0, MENU_SEND_DIAG, 8, "Send diagnostics now");
        popup.setOnMenuItemClickListener(item -> {
            handleMenuAction(item.getItemId());
            return true;
        });
        popup.show();
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
            final long mtime = crash.lastModified();
            Runnable markShown = () -> getSharedPreferences("sdrstream_diag", MODE_PRIVATE)
                    .edit().putLong("crash_shown_mtime", mtime).apply();
            DiagUploader up = DiagUploadAndroid.uploader(this);
            if (!up.asked()) {
                // First-crash consent: names exactly what leaves the device.
                new AlertDialog.Builder(this)
                        .setTitle("sdrstream crashed last time")
                        .setMessage("Send crash reports automatically?\n\n"
                                + "What leaves this device: the redacted diagnostics summary only - "
                                + "device model, Android and app versions, engine state, file names "
                                + "in the app folder, recent process exit reasons, and log tails with "
                                + "URLs, passwords, tokens and locations stripped. Nothing else.\n\n"
                                + "Auto-send transmits only that summary text: the same diagnostics "
                                + "summary that menu -> Share logs produces (Share additionally attaches "
                                + "raw log files, which auto-send never sends).\n\n"
                                + "At most one automatic report per day; the same crash is never sent twice. "
                                + "You can turn this off in the menu at any time.")
                        .setPositiveButton("Auto-send", (d, w) -> {
                            up.setOptIn(true);
                            markShown.run();
                            DiagUploadAndroid.maybeAutoSendAsync(this);
                        })
                        .setNeutralButton("Share once", (d, w) -> {
                            up.markAsked();
                            markShown.run();
                            shareLogs();
                        })
                        .setNegativeButton("Not now", (d, w) -> {
                            up.markAsked();
                            markShown.run();
                        })
                        .show();
            } else {
                new AlertDialog.Builder(this)
                        .setTitle("sdrstream crashed last time")
                        .setMessage("A crash log was saved. Sharing it with the report helps fix the problem.")
                        .setPositiveButton("Share log", (d, w) -> shareLogs())
                        .setNegativeButton("Dismiss", (d, w) -> markShown.run())
                        .show();
            }
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

    /**
     * Renders the diagnostics report inside the WebView with a one-tap
     * "Send diagnostics" button. The engine-ring fetch and javalog refresh
     * are network/exec work, so they run on a worker thread; only the
     * WebView render hops back to the main thread.
     */
    private void showDiagnostics(String headline) {
        new Thread(() -> {
            JavaLog.capture(this, new File(getFilesDir(), Diag.JAVA_LOG));
            String ring = Diag.fetchRingTail(this);
            String report = Diag.report(this, ring);
            webView.post(() -> {
                if (isFinishing()) return;
                String escaped = Html.escapeHtml(report);
                String html = "<html><body style='background:#070a12;color:#eee;font-family:sans-serif;padding:2em'>"
                        + "<h3>" + Html.escapeHtml(headline) + "</h3>"
                        + "<p><a href='sdrstream-diag://share' style='display:inline-block;"
                        + "padding:0.8em 1.4em;background:#5cc8ff;color:#04121f;border-radius:8px;"
                        + "text-decoration:none;font-weight:700'>Send diagnostics</a></p>"
                        + "<p>or use the menu &rarr; <b>Share logs</b>.</p>"
                        + "<pre style='white-space:pre-wrap;color:#9fd'>" + escaped + "</pre>"
                        + "</body></html>";
                webView.loadDataWithBaseURL(null, html, "text/html", "utf-8", null);
            });
        }, "diag-render").start();
    }

    /**
     * Shares diag.txt plus whatever logs exist through the system sheet.
     * javalog.txt is refreshed and the engine ring fetched first so the
     * share is current; that exec/network work runs on a worker thread and
     * only the intent fires on the main thread.
     */
    private void shareLogs() {
        new Thread(() -> {
            try {
                JavaLog.capture(this, new File(getFilesDir(), Diag.JAVA_LOG));
                String ring = Diag.fetchRingTail(this);
                Diag.writeReportFile(this, ring);
                ArrayList<Uri> uris = new ArrayList<>();
                uris.add(LogProvider.uriFor(this, Diag.DIAG_TXT));
                if (new File(getFilesDir(), Diag.ENGINE_LOG).exists()) {
                    uris.add(LogProvider.uriFor(this, Diag.ENGINE_LOG));
                }
                if (new File(getFilesDir(), Diag.ENGINE_LOG_OLD).exists()) {
                    uris.add(LogProvider.uriFor(this, Diag.ENGINE_LOG_OLD));
                }
                if (new File(getFilesDir(), Diag.JAVA_LOG).exists()) {
                    uris.add(LogProvider.uriFor(this, Diag.JAVA_LOG));
                }
                if (new File(getFilesDir(), Diag.CRASH_LOG).exists()) {
                    uris.add(LogProvider.uriFor(this, Diag.CRASH_LOG));
                }
                Intent send = new Intent(Intent.ACTION_SEND_MULTIPLE);
                send.setType("text/plain");
                send.putExtra(Intent.EXTRA_SUBJECT, "sdrstream diagnostics");
                send.putParcelableArrayListExtra(Intent.EXTRA_STREAM, uris);
                send.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION);
                runOnUiThread(() -> {
                    try {
                        startActivity(Intent.createChooser(send, "Share sdrstream logs"));
                    } catch (Throwable t) {
                        Diag.writeCrash(this, "share logs (launch)", t);
                    }
                });
            } catch (Throwable t) {
                Diag.writeCrash(this, "share logs", t);
                runOnUiThread(() -> Toast.makeText(this, "Could not share logs: " + t, Toast.LENGTH_LONG).show());
            }
        }, "share-logs").start();
    }

    /** On-demand battery exemption route from the menu. */
    private void requestBatteryExemption() {
        try {
            PowerManager pm = (PowerManager) getSystemService(POWER_SERVICE);
            if (pm != null && pm.isIgnoringBatteryOptimizations(getPackageName())) {
                Toast.makeText(this, "Background running already allowed.",
                        Toast.LENGTH_SHORT).show();
                return;
            }
            startActivity(new Intent(
                    Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
                    Uri.parse("package:" + getPackageName())));
        } catch (Throwable t) {
            Diag.writeCrash(this, "battery exemption request", t);
            Toast.makeText(this, "Could not open battery settings: " + t,
                    Toast.LENGTH_LONG).show();
        }
    }

    /**
     * Asks for the battery exemption ONCE: before the once-only flag this
     * dialog reappeared on every cold start until granted - a modal nag on
     * every app open. After the first showing (either button), it never
     * auto-shows again; the on-demand route is menu -> "Allow background
     * running".
     */
    private void maybeAskBatteryExemption() {
        try {
            PowerManager pm = (PowerManager) getSystemService(POWER_SERVICE);
            boolean seen = getSharedPreferences("sdrstream", MODE_PRIVATE)
                    .getBoolean("battery_prompt_seen", false);
            if (pm != null && !pm.isIgnoringBatteryOptimizations(getPackageName()) && !seen) {
                getSharedPreferences("sdrstream", MODE_PRIVATE).edit()
                        .putBoolean("battery_prompt_seen", true).apply();
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

    /**
     * Applies a new UI password: the engine reads SDRSTREAM_UI_PASSWORD only
     * at process start, so a running engine keeps accepting the OLD password
     * (and rejecting the new one) until it restarts. Stop it, wait for the
     * port to actually close, start it again, then reload the UI.
     */
    private void restartEngine() {
        Toast.makeText(this, "Password saved - restarting engine to apply.",
                Toast.LENGTH_LONG).show();
        try {
            startService(new Intent(this, EngineService.class)
                    .setAction(EngineService.ACTION_STOP));
        } catch (Throwable t) {
            Diag.writeCrash(this, "stop engine for password change", t);
        }
        if (waitThread != null) waitThread.interrupt();
        waitThread = new Thread(() -> {
            for (int i = 0; i < 20 && !isFinishing(); i++) {
                if (!portOpen()) break;
                try {
                    Thread.sleep(500);
                } catch (InterruptedException e) {
                    return;
                }
            }
            try {
                startEngineService();
            } catch (Throwable t) {
                Diag.writeCrash(this, "restart engine service", t);
                Diag.recordEngineError(this, "could not restart the engine service: " + t);
            }
            loadWhenUp();
        }, "engine-restart");
        waitThread.start();
    }

    /** Legacy options menu for devices that still have a menu key. */
    @Override
    public boolean onCreateOptionsMenu(Menu menu) {
        menu.add(0, MENU_SHOW_PASSWORD, 0, "Show UI password");
        menu.add(0, MENU_SET_PASSWORD, 1, "Set UI password");
        menu.add(0, MENU_SOURCE, 2, "Source code (AGPL-3.0)");
        menu.add(0, MENU_STOP_ENGINE, 3, "Stop engine");
        menu.add(0, MENU_DIAGNOSTICS, 4, "Diagnostics");
        menu.add(0, MENU_SHARE_LOGS, 5, "Share logs");
        menu.add(0, MENU_TAILNET, 6, "Tailscale remote access");
        menu.add(0, MENU_BACKGROUND, 6, "Allow background running");
        menu.add(0, MENU_AUTOSEND, 7, "Auto-send crash reports")
                .setCheckable(true)
                .setChecked(DiagUploadAndroid.uploader(this).optedIn());
        menu.add(0, MENU_SEND_DIAG, 8, "Send diagnostics now");
        return true;
    }

    @Override
    public boolean onOptionsItemSelected(MenuItem item) {
        return handleMenuAction(item.getItemId()) || super.onOptionsItemSelected(item);
    }

    /** One handler behind both the overflow PopupMenu and the options menu. */
    private boolean handleMenuAction(int id) {
        switch (id) {
            case MENU_AUTOSEND: {
                DiagUploader up = DiagUploadAndroid.uploader(this);
                boolean on = !up.optedIn();
                up.setOptIn(on);
                Toast.makeText(this,
                        on ? "Crash reports will be sent automatically (redacted, max 1/day)"
                           : "Automatic crash reports off",
                        Toast.LENGTH_LONG).show();
                if (on) DiagUploadAndroid.maybeAutoSendAsync(this);
                return true;
            }
            case MENU_SEND_DIAG:
                DiagUploadAndroid.sendNowAsync(this, r -> {
                    String msg;
                    switch (r) {
                        case SENT: msg = "Diagnostics sent"; break;
                        case NO_ENDPOINT: msg = "Auto-send relay not configured in this build"; break;
                        case OFFLINE: msg = "No connection - will retry on next launch"; break;
                        default: msg = "Send failed (" + r + ") - will retry on next launch"; break;
                    }
                    Toast.makeText(this, msg, Toast.LENGTH_LONG).show();
                });
                return true;
            case MENU_SHOW_PASSWORD:
                new AlertDialog.Builder(this)
                        .setTitle("Web UI password")
                        .setMessage("Log in to the UI with:\n\n" + Prefs.getUiPassword(this)
                                + "\n\nIt is stored only on this device.")
                        .setPositiveButton("OK", null)
                        .show();
                return true;
            case MENU_SET_PASSWORD: {
                final EditText input = new EditText(this);
                input.setHint("new password");
                new AlertDialog.Builder(this)
                        .setTitle("Set UI password")
                        .setView(input)
                        .setPositiveButton("Save", (d, w) -> {
                            String pw = input.getText().toString();
                            if (!pw.isEmpty()) {
                                Prefs.setUiPassword(this, pw);
                                restartEngine();
                            }
                        })
                        .setNegativeButton("Cancel", null)
                        .show();
                return true;
            }
            case MENU_TAILNET: {
                final EditText input = new EditText(this);
                input.setHint("100.x.y.z - empty disables remote access");
                String current = Prefs.getTailnetAddress(this);
                if (!current.isEmpty()) {
                    input.setText(current);
                } else {
                    String suggested = EngineService.detectVpnCgnatAddress(this);
                    if (suggested != null) input.setText(suggested);
                }
                new AlertDialog.Builder(this)
                        .setTitle("Tailscale remote access")
                        .setMessage("Enter this device's own Tailscale IPv4 (Tailscale app shows it, "
                                + "e.g. 100.x.y.z). The UI then also answers on that address with the "
                                + "UI password. The bind is verified at engine start: it must currently "
                                + "be assigned to a VPN network, otherwise the engine stays on this "
                                + "device only. Empty disables remote access.")
                        .setView(input)
                        .setPositiveButton("Save", (d, w) -> {
                            Prefs.setTailnetAddress(this, input.getText().toString());
                            restartEngine();
                        })
                        .setNegativeButton("Cancel", null)
                        .show();
                return true;
            }
            case MENU_SOURCE:
                startActivity(new Intent(Intent.ACTION_VIEW,
                        Uri.parse("https://github.com/Chaim12345/sdrstream")));
                return true;
            case MENU_STOP_ENGINE:
                startService(new Intent(this, EngineService.class)
                        .setAction(EngineService.ACTION_STOP));
                finish();
                return true;
            case MENU_DIAGNOSTICS:
                showDiagnostics("Diagnostics");
                return true;
            case MENU_SHARE_LOGS:
                shareLogs();
                return true;
            case MENU_BACKGROUND:
                requestBatteryExemption();
                return true;
            default:
                return false;
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
