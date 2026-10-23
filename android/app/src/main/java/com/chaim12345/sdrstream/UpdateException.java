package com.chaim12345.sdrstream;

/** A user-presentable update failure: message is safe to show in a dialog. */
final class UpdateException extends Exception {
    UpdateException(String message) { super(message); }
    UpdateException(String message, Throwable cause) { super(message, cause); }
}
