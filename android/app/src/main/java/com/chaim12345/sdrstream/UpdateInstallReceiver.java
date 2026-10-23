package com.chaim12345.sdrstream;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;
import android.content.pm.PackageInstaller;
import android.util.Log;
import android.widget.Toast;

/**
 * Receives the PackageInstaller commit result for a self-update. Declared
 * non-exported in the manifest; only the system delivers here.
 *
 * A successful self-update replaces this process, so SUCCESS normally
 * never gets to run - the next app start simply is the new version. The
 * states that matter here are PENDING_USER_ACTION (hand the system's
 * confirm UI to the user) and failures (clean up, keep the current app).
 */
public class UpdateInstallReceiver extends BroadcastReceiver {
    private static final String TAG = "sdrstream-update";

    @Override
    public void onReceive(Context ctx, Intent intent) {
        if (!Updater.ACTION_INSTALL_RESULT.equals(intent.getAction())) return;
        int status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS,
                PackageInstaller.STATUS_FAILURE);
        switch (status) {
            case PackageInstaller.STATUS_PENDING_USER_ACTION: {
                Intent confirm = intent.getParcelableExtra(Intent.EXTRA_INTENT);
                if (confirm != null) {
                    confirm.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
                    ctx.startActivity(confirm);
                }
                break;
            }
            case PackageInstaller.STATUS_SUCCESS:
                Log.i(TAG, "update installed");
                break;
            default: {
                String msg = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE);
                Log.w(TAG, "update install failed: status=" + status + " " + msg);
                Updater.cleanupDownloads(ctx);
                Toast.makeText(ctx, "Update failed - the current version is unchanged."
                        + (msg == null ? "" : " (" + msg + ")"), Toast.LENGTH_LONG).show();
            }
        }
    }
}
