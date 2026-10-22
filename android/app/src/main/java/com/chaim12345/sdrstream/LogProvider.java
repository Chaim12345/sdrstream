package com.chaim12345.sdrstream;

import android.content.ContentProvider;
import android.content.ContentValues;
import android.database.Cursor;
import android.database.MatrixCursor;
import android.net.Uri;
import android.os.ParcelFileDescriptor;
import android.provider.OpenableColumns;

import java.io.File;
import java.io.FileNotFoundException;

/**
 * Serves the app's log files to share targets (mail, chat, drive) through
 * content:// URIs. Read-only, whitelisted file names, not exported.
 */
public class LogProvider extends ContentProvider {
    public static final String AUTHORITY = "com.chaim12345.sdrstream.logs";

    public static Uri uriFor(String name) {
        return new Uri.Builder().scheme("content").authority(AUTHORITY).path(name).build();
    }

    private File resolve(Uri uri) throws FileNotFoundException {
        String path = uri.getPath();
        String name = (path != null && path.startsWith("/")) ? path.substring(1) : path;
        if (!Diag.ENGINE_LOG.equals(name) && !Diag.CRASH_LOG.equals(name) && !Diag.DIAG_TXT.equals(name)) {
            throw new FileNotFoundException("not shared: " + name);
        }
        File f = new File(getContext().getFilesDir(), name);
        if (!f.exists()) throw new FileNotFoundException(name);
        return f;
    }

    @Override
    public boolean onCreate() {
        return true;
    }

    @Override
    public ParcelFileDescriptor openFile(Uri uri, String mode) throws FileNotFoundException {
        return ParcelFileDescriptor.open(resolve(uri), ParcelFileDescriptor.MODE_READ_ONLY);
    }

    @Override
    public Cursor query(Uri uri, String[] projection, String selection, String[] selectionArgs,
                        String sortOrder) {
        try {
            File f = resolve(uri);
            MatrixCursor cursor = new MatrixCursor(
                    new String[]{OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE});
            cursor.addRow(new Object[]{f.getName(), f.length()});
            return cursor;
        } catch (FileNotFoundException e) {
            return null;
        }
    }

    @Override
    public String getType(Uri uri) {
        return "text/plain";
    }

    @Override
    public Uri insert(Uri uri, ContentValues values) {
        return null;
    }

    @Override
    public int delete(Uri uri, String selection, String[] selectionArgs) {
        return 0;
    }

    @Override
    public int update(Uri uri, ContentValues values, String selection, String[] selectionArgs) {
        return 0;
    }
}
