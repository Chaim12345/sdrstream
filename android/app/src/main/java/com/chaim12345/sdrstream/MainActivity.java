package com.chaim12345.sdrstream;

import android.app.Activity;
import android.app.AlertDialog;
import android.content.Intent;
import android.net.Uri;
import android.os.Bundle;
import android.os.PowerManager;
import android.provider.Settings;
import android.view.Menu;
import android.view.MenuItem;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.EditText;
import android.widget.Toast;

import java.io.IOException;
import java.net.InetSocketAddress;
import java.net.Socket;

/**
 * Full-stack sdrstream on the phone: starts the Go engine in a foreground
 * service and shows its web UI in a WebView pointed at loopback.
 *
 * sdrstream is free software under the GNU AGPL-3.0; the corresponding
 * source is https://github.com/Chaim12345/sdrstream (linked from the menu,
 * which satisfies the in-app source offer).
 */
public class MainActivity extends Activity {
    private static final String UI_URL = "http://127.0.0.1:" + EngineService.PORT + "/";
    private WebView webView;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        startService(new Intent(this, EngineService.class)
                .setAction(EngineService.ACTION_START));
        maybeAskBatteryExemption();

        webView = new WebView(this);
        webView.getSettings().setJavaScriptEnabled(true);
        webView.getSettings().setDomStorageEnabled(true);
        webView.getSettings().setMediaPlaybackRequiresUserGesture(false);
        webView.setWebViewClient(new WebViewClient());
        setContentView(webView);
        loadWhenUp(0);
    }

    /** Poll /healthz: the engine takes a moment to bind its port. */
    private void loadWhenUp(final int attempt) {
        webView.postDelayed(() -> {
            if (isFinishing()) return;
            if (portOpen()) {
                webView.loadUrl(UI_URL);
            } else if (attempt < 60) {
                loadWhenUp(attempt + 1);
            } else {
                webView.loadData("<html><body style='background:#070a12;color:#eee;"
                        + "font-family:sans-serif;padding:2em'>The sdrstream engine did not "
                        + "start. Check the notification, then reopen the app.</body></html>",
                        "text/html", "utf-8");
            }
        }, attempt == 0 ? 300 : 1000);
    }

    private boolean portOpen() {
        try (Socket s = new Socket()) {
            s.connect(new InetSocketAddress("127.0.0.1", EngineService.PORT), 400);
            return true;
        } catch (IOException e) {
            return false;
        }
    }

    private void maybeAskBatteryExemption() {
        PowerManager pm = (PowerManager) getSystemService(POWER_SERVICE);
        if (pm != null && !pm.isIgnoringBatteryOptimizations(getPackageName())) {
            new AlertDialog.Builder(this)
                    .setTitle("Keep monitoring in the background")
                    .setMessage("Android battery saving can suspend the monitor. " +
                            "Exempt sdrstream so scanning continues with the screen off.")
                    .setPositiveButton("Exempt", (d, w) -> startActivity(new Intent(
                            Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
                            Uri.parse("package:" + getPackageName()))))
                    .setNegativeButton("Not now", null)
                    .show();
        }
    }

    @Override
    public boolean onCreateOptionsMenu(Menu menu) {
        menu.add(0, 1, 0, "Show UI password");
        menu.add(0, 2, 0, "Set UI password");
        menu.add(0, 3, 0, "Source code (AGPL-3.0)");
        menu.add(0, 4, 0, "Stop engine");
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
        if (webView != null) webView.destroy();
        super.onDestroy();
    }
}
