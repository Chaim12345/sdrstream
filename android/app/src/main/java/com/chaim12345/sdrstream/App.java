package com.chaim12345.sdrstream;

import android.app.Application;

/** Installs the crash log handler as early as the process allows. */
public class App extends Application {
    @Override
    public void onCreate() {
        super.onCreate();
        Diag.installCrashHandler(this);
    }
}
