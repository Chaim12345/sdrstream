#!/usr/bin/env bash
# Discriminating test for login_state_verdict() extracted from the SHIPPED
# android-ui-qa.sh (not a copy). Run: bash qa/test-login-state-verdict.sh
set -u; cd "$(dirname "$0")"; T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
log() { echo "[qa] $*"; }  # same sink shape as the harness
awk '/^login_state_verdict\(\)/,/^}/' android-ui-qa.sh > "$T/fn.sh"
grep -q 'verdict UNKNOWN' "$T/fn.sh" || { echo "FAIL: function not extracted"; exit 1; }
. "$T/fn.sh"
: > "$T/empty.xml"
echo '<node class="android.widget.EditText" text=""/>' > "$T/form.xml"
echo '<node class="android.widget.TextView" text="Overview"/>' > "$T/app.xml"
OLD_accept_on_empty=$(if grep -q 'class="android.widget.EditText"' "$T/empty.xml"; then echo rejected; else echo ACCEPTED; fi)
[ "$OLD_accept_on_empty" = ACCEPTED ] && echo "RED (old logic): empty dump -> ACCEPTED (the false-acceptance being fixed)" || echo "unexpected: old logic did not false-accept"
o1=$(login_state_verdict "$T/empty.xml" new); o2=$(login_state_verdict "$T/form.xml" old); o3=$(login_state_verdict "$T/app.xml" new)
echo "$o1"; echo "$o2"; echo "$o3"
case "$o1" in *"verdict UNKNOWN (UI dump empty - device lost?)"*) : ;; *) echo "FAIL empty: $o1"; exit 1;; esac
case "$o2" in *"still on login form (rejected)"*) : ;; *) echo "FAIL form: $o2"; exit 1;; esac
case "$o3" in *"ACCEPTED (left login form)"*) : ;; *) echo "FAIL app: $o3"; exit 1;; esac
echo "GREEN (new logic): empty->UNKNOWN, form->rejected, app->ACCEPTED - PASS"
