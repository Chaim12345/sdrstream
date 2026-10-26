#!/usr/bin/env python3
"""Negative/positive fixtures for keystore_verdict.py. Run: python3 android/ci/test_keystore_verdict.py"""
import os, subprocess, sys, tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
V = os.path.join(HERE, "keystore_verdict.py")
C = "com.chaim12345.sdrstream.SecretStoreInstrumentedTest"
P = "com.chaim12345.sdrstream.KeystoreIvPolicyInstrumentedTest"
ROWS = [(C, "keystoreIsAvailable"), (C, "putGetClearRoundTrip"), (C, "plaintextNeverInPrefs"),
        (P, "callerIvRejectedByKeystore"), (P, "providerIvAccepted")]
M = "java.lang.AssertionError: put must succeed on a real AndroidKeyStore"

def run(variant, rc, rows=ROWS, fails=None, skipped=(), raw_extra=""):
    fails = fails or {}
    with tempfile.TemporaryDirectory() as d:
        out = os.path.join(d, "android/app/build/outputs/androidTest-results/connected/debug")
        os.makedirs(out)
        x = "<testsuite>"
        for c, n in rows:
            if n in fails: x += f'<testcase classname="{c}" name="{n}"><failure message="{fails[n]}">t</failure></testcase>'
            elif n in skipped: x += f'<testcase classname="{c}" name="{n}"><skipped/></testcase>'
            else: x += f'<testcase classname="{c}" name="{n}"/>'
        open(os.path.join(out, "TEST-a.xml"), "w").write(x + raw_extra + "</testsuite>")
        r = subprocess.run([sys.executable, V, variant, str(rc)], cwd=d, capture_output=True, text=True)
        return r.returncode, r.stdout + r.stderr

BASE_FAILS = {"putGetClearRoundTrip": M, "plaintextNeverInPrefs": M}
cases = [
    ("fixed all pass", run("fixed", 0), True),
    ("baseline expected failures", run("baseline", 1, fails=BASE_FAILS), True),
    ("unknown variant rejected", run("fixd", 0), False),
    ("empty variant rejected", run("", 0), False),
    ("duplicate testcase rejected", run("fixed", 0, rows=ROWS + [ROWS[1]]), False),
    ("duplicate masks failure rejected", run("baseline", 1, rows=ROWS + [ROWS[1]], fails=BASE_FAILS), False),
    ("extra name in required class rejected", run("fixed", 0, rows=ROWS + [(C, "surprise")]), False),
    ("foreign class rejected", run("fixed", 0, rows=ROWS + [("com.other.Foo", "bar")]), False),
    ("fixed with failure", run("fixed", 1, fails={"putGetClearRoundTrip": "x"}), False),
    ("fixed nonzero rc", run("fixed", 1), False),
    ("baseline passed", run("baseline", 0), False),
    ("baseline wrong message", run("baseline", 1, fails={"putGetClearRoundTrip": "other"}), False),
    ("baseline policy test failed", run("baseline", 1, fails=dict(BASE_FAILS, callerIvRejectedByKeystore="y")), False),
    ("missing required test", run("fixed", 0, rows=[r for r in ROWS if r[1] != "providerIvAccepted"]), False),
    ("skipped required test", run("fixed", 0, skipped=("providerIvAccepted",)), False),
]
bad = 0
for name, (rc, out), want_ok in cases:
    ok = rc == 0
    flag = "ok " if ok == want_ok else "BAD"
    if ok != want_ok: bad += 1
    print(flag, name, "->", "accepted" if ok else "rejected")
# no XML at all
with tempfile.TemporaryDirectory() as d:
    r = subprocess.run([sys.executable, V, "baseline", "1"], cwd=d, capture_output=True, text=True)
    print("ok " if r.returncode != 0 else "BAD", "no XML rejected"); bad += r.returncode == 0
sys.exit(1 if bad else 0)
