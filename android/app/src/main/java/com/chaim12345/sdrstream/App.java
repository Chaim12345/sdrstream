package com.chaim12345.sdrstream;

import android.app.Application;

/** Installs the crash log handler as early as the process allows. */
public class App extends Application {
    @Override
    public void onCreate() {
        super.onCreate();
        Diag.installCrashHandler(this);
        // Snapshot this app's own logcat so a later diagnostics share has
        // the Java side without ADB; refreshed again on every Share.
        JavaLog.captureAsync(this);
    }
}
